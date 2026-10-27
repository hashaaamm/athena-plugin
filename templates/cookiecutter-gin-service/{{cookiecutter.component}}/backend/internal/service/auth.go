package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"{{ cookiecutter.go_module }}/internal/apperr"
	"{{ cookiecutter.go_module }}/internal/repository"
	"{{ cookiecutter.go_module }}/internal/security"
)

// The authentication use cases, and every rule they apply. The only place either lives.
//
// **Sessions.** Login starts one: an access token and a refresh token, with a refresh_sessions row
// keyed by the refresh token's `jti` and a new family_id. Refresh spends the refresh token and
// returns the next pair in the same family; a spent token presented again revokes the whole family.
// Logout revokes the family. Changing a password revokes every family the user has and starts a new
// one for the caller. Where the refresh token travels — a JSON body or an httpOnly cookie — is the
// handler's business; nothing here knows.
//
// **Revocation latency is fifteen minutes**, and it is a number to publish rather than discover.
// Revoking a session stops it minting access tokens at once; the access token it already minted is
// checked against nothing and keeps working until it expires. Deactivating a user is the same
// sentence. A compliance position that cannot accept fifteen minutes needs an opaque access token
// and a lookup per request, which is the cost a JWT was chosen to avoid.
//
// **Every write runs in Store.WithTx**, which commits before the method returns — so before the
// handler writes a byte — and a failed commit comes back as an error the client sees as a 500.
// Password hashing runs *outside* the transaction: Argon2 takes tens of milliseconds on purpose, and
// a transaction held open across it is a pool connection and a row lock held for nothing.
//
// What this service does **not** do, so nobody assumes it does: no roles or permissions (every
// authenticated caller may do everything on this surface, which is true only because the surface is
// the caller's own account); no password reset or email verification (both need mail, a dependency
// this template does not choose for you); and no grace window on rotation — two refreshes racing
// with one token look exactly like a replay, so a client that fires them in parallel signs its user
// out. The fix is one in-flight refresh per client, not a window in which a stolen token also works.

// refusalMessage is the one message for every refused refresh token — malformed, expired, spent,
// revoked, or naming a user who can no longer sign in. Which check failed is for the log.
const refusalMessage = "Invalid or expired token"

// User is what every use case that returns a user returns. There is no digest on it, and adding
// one is how a hash reaches a log aggregator, a browser's network tab and a support ticket in the
// same afternoon.
type User struct {
	ID        uuid.UUID
	Email     string
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TokenPair is a new session, or the next step of one.
type TokenPair struct {
	AccessToken string
	// Single-use: spending it revokes it, and presenting it a second time revokes its family.
	RefreshToken string
	// Seconds until the access token expires.
	ExpiresIn int
}

// AuthService is the authentication use cases.
type AuthService struct {
	store  *repository.Store
	hasher *security.Hasher
	tokens *security.Tokens
	logger *slog.Logger
	now    func() time.Time
}

// NewAuthService builds the service. The composition root (internal/app) is its only caller.
func NewAuthService(store *repository.Store, hasher *security.Hasher, tokens *security.Tokens, logger *slog.Logger) *AuthService {
	return &AuthService{store: store, hasher: hasher, tokens: tokens, logger: logger, now: time.Now}
}

// Register creates an account and returns it — not a session; the caller logs in next.
//
// This does leak that an address is registered. The alternative, answering 201 to a duplicate,
// means the real owner gets no account while the caller believes they have one; and every
// registration form on the internet leaks the same thing through its error message anyway. Login,
// where it matters, does not distinguish.
func (s *AuthService) Register(ctx context.Context, email, password string) (User, error) {
	if _, err := s.store.Users().GetByEmail(ctx, email); err == nil {
		return User{}, apperr.Conflict("An account with that email already exists")
	} else if !errors.Is(err, repository.ErrNotFound) {
		return User{}, err
	}
	digest, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return User{}, err
	}
	var created repository.User
	err = s.store.WithTx(ctx, func(tx *repository.Store) error {
		created, err = tx.Users().Create(ctx, email, repository.PasswordDigest(digest))
		if errors.Is(err, repository.ErrDuplicate) {
			// Lost a race with a concurrent registration for the same address.
			return apperr.Conflict("An account with that email already exists")
		}
		return err
	})
	if err != nil {
		return User{}, err
	}
	// The id, never the email and never the digest: a log line is read by more people than a
	// database is.
	s.logger.InfoContext(ctx, "user_registered", "user_id", created.ID)
	return userFrom(created), nil
}

// Login verifies a password and starts a session: a new family, and its first token pair.
//
// Three different failures — no such email, wrong password, deactivated account — answer with one
// message, one status and one shape. Telling them apart is information the caller has not
// authenticated well enough to receive, and "this account is disabled" confirms the account exists.
//
// The verification runs before any branch on the user, so a miss costs what a hit costs. A response
// that is fast for unknown addresses and slow for known ones is an enumeration oracle no matter how
// identical the body is.
func (s *AuthService) Login(ctx context.Context, email, password string) (TokenPair, error) {
	user, err := s.store.Users().GetByEmail(ctx, email)
	found := err == nil
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return TokenPair{}, err
	}
	matched, upgraded, err := s.hasher.Verify(ctx, password, string(user.HashedPassword))
	if err != nil {
		return TokenPair{}, err
	}
	if !found || !matched || !user.IsActive {
		return TokenPair{}, apperr.Unauthorized("Invalid email or password")
	}
	var pair TokenPair
	err = s.store.WithTx(ctx, func(tx *repository.Store) error {
		if upgraded != "" {
			// Only when the stored digest was made with other parameters than the current ones.
			// Login is the only moment the plaintext is in hand.
			if err := tx.Users().SetPasswordHash(ctx, user.ID, repository.PasswordDigest(upgraded)); err != nil {
				return err
			}
		}
		pair, err = s.issuePair(ctx, tx, user.ID, uuid.New(), nil)
		return err
	})
	if err != nil {
		return TokenPair{}, err
	}
	s.logger.InfoContext(ctx, "user_logged_in", "user_id", user.ID)
	return pair, nil
}

// Refresh spends a refresh token for the next pair in its family. "" means none was presented.
//
// The row is read under a lock, so two refreshes racing with one token cannot both win: the second
// waits for the first, then finds the row revoked. That is indistinguishable from a replay, and it
// is treated as one.
//
// A revoked row presented again is the case the table exists for. The token verified, so this
// service issued it; it has been spent once already, so somebody else holds a copy — a replay, or a
// stolen token racing the real client. Either way the family is compromised and all of it is
// revoked. apperr.CompromisedSession is what keeps that revocation when the request answers 401;
// any other error would roll it back.
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	if refreshToken == "" {
		return TokenPair{}, apperr.Unauthorized(refusalMessage)
	}
	claims, err := s.tokens.Decode(refreshToken, security.Refresh)
	if err != nil {
		return TokenPair{}, err
	}
	var pair TokenPair
	err = s.store.WithTx(ctx, func(tx *repository.Store) error {
		now := s.now()
		session, err := tx.RefreshSessions().GetForUpdate(ctx, claims.ID)
		if errors.Is(err, repository.ErrNotFound) {
			return apperr.Unauthorized(refusalMessage)
		} else if err != nil {
			return err
		}
		if session.UserID != claims.Subject || !session.ExpiresAt.After(now) {
			return apperr.Unauthorized(refusalMessage)
		}
		if session.RevokedAt != nil {
			if err := tx.RefreshSessions().RevokeFamily(ctx, session.FamilyID, now); err != nil {
				return err
			}
			// Warning, not info: this is the event an incident starts from. Ids only, never the
			// token — a log line is read by more people than a database is.
			s.logger.WarnContext(ctx, "refresh_token_reused", "user_id", session.UserID, "family_id", session.FamilyID)
			return apperr.CompromisedSession(refusalMessage)
		}
		user, err := tx.Users().Get(ctx, session.UserID)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return err
		}
		if err != nil || !user.IsActive {
			// A user deactivated (or deleted) since this family began. Nothing more is minted for it,
			// and the family goes so the next attempt is refused without reaching this branch.
			if err := tx.RefreshSessions().RevokeFamily(ctx, session.FamilyID, now); err != nil {
				return err
			}
			return apperr.CompromisedSession(refusalMessage)
		}
		pair, err = s.issuePair(ctx, tx, user.ID, session.FamilyID, &session.ID)
		return err
	})
	if err != nil {
		return TokenPair{}, err
	}
	return pair, nil
}

// Logout ends the session a refresh token belongs to: its whole family, on this device.
//
// Never a refusal. Clients retry logout on a flaky network, and a 401 from it produces a support
// ticket about being unable to sign out. A token that does not decode — absent, expired, malformed —
// names no session that could still be extended, so there is nothing to end and the answer is the
// same. The access token the client holds is not revoked; it expires on its own, which is the
// revocation latency at the top of this file. A database failure is still an error: claiming a
// session ended when it did not is worse than a 500.
func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	claims, err := s.tokens.Decode(refreshToken, security.Refresh)
	if err != nil {
		return nil //nolint:nilerr // an undecodable token names no session to end; see above
	}
	var ended uuid.UUID
	err = s.store.WithTx(ctx, func(tx *repository.Store) error {
		// Locked for the reason Refresh locks it. A refresh racing this logout either finishes first
		// — and the revocation below, starting after it commits, sees the row it inserted — or waits,
		// and then finds its token revoked.
		session, err := tx.RefreshSessions().GetForUpdate(ctx, claims.ID)
		if errors.Is(err, repository.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if session.UserID != claims.Subject {
			return nil
		}
		ended = session.UserID
		return tx.RefreshSessions().RevokeFamily(ctx, session.FamilyID, s.now())
	})
	if err != nil {
		return err
	}
	if ended != uuid.Nil {
		s.logger.InfoContext(ctx, "user_logged_out", "user_id", ended)
	}
	return nil
}

// Me is the signed-in user, read from the database rather than from the token's claims.
//
// The token says who the caller is; it does not say what is currently true about them. A user
// deactivated four minutes ago still holds a valid access token, and this is the endpoint where that
// must not read as "fine".
func (s *AuthService) Me(ctx context.Context, userID uuid.UUID) (User, error) {
	user, err := s.activeUser(ctx, userID)
	if err != nil {
		return User{}, err
	}
	return userFrom(user), nil
}

// ChangePassword replaces a password, ends every session the user has, and starts a new one for
// the caller.
//
// A valid access token is not enough. It is a bearer credential that may have been copied out of a
// log or a browser, and a password change is the one operation that would lock the real owner out.
//
// Every family is revoked, the caller's own included. Otherwise "change your password" does not end
// the session of whoever knew the old one — and a stolen refresh token from the caller's own device
// is in the caller's family. The caller gets a fresh pair in a new family, so the person who just
// proved they know the password stays signed in and nobody else does. Access tokens already issued
// still run out their fifteen minutes.
func (s *AuthService) ChangePassword(ctx context.Context, userID uuid.UUID, current, replacement string) (TokenPair, error) {
	user, err := s.activeUser(ctx, userID)
	if err != nil {
		return TokenPair{}, err
	}
	matched, _, err := s.hasher.Verify(ctx, current, string(user.HashedPassword))
	if err != nil {
		return TokenPair{}, err
	}
	if !matched {
		// Not 401: the session is valid, and a client that saw 401 would try to re-authenticate
		// rather than tell the user their current password was wrong. Not 422 either — the request
		// was well formed. The caller has not earned this operation.
		return TokenPair{}, apperr.Forbidden("Current password is incorrect")
	}
	digest, err := s.hasher.Hash(ctx, replacement)
	if err != nil {
		return TokenPair{}, err
	}
	var pair TokenPair
	err = s.store.WithTx(ctx, func(tx *repository.Store) error {
		if err := tx.Users().SetPasswordHash(ctx, user.ID, repository.PasswordDigest(digest)); err != nil {
			return err
		}
		if err := tx.RefreshSessions().RevokeAllForUser(ctx, user.ID, s.now()); err != nil {
			return err
		}
		pair, err = s.issuePair(ctx, tx, user.ID, uuid.New(), nil)
		return err
	})
	if err != nil {
		return TokenPair{}, err
	}
	s.logger.InfoContext(ctx, "user_changed_password", "user_id", user.ID)
	return pair, nil
}

// issuePair mints an access token and a refresh token, and records the refresh token's row.
//
// When replacing is given, that row is revoked in the same transaction as the new row is inserted.
// Committed separately, a crash between the two would leave the client holding a spent token and no
// new one — signed out, with nothing to show for it.
func (s *AuthService) issuePair(ctx context.Context, tx *repository.Store, userID, familyID uuid.UUID, replacing *uuid.UUID) (TokenPair, error) {
	if replacing != nil {
		if err := tx.RefreshSessions().Revoke(ctx, *replacing, s.now()); err != nil {
			return TokenPair{}, err
		}
	}
	access, expiresIn, err := s.tokens.IssueAccess(userID)
	if err != nil {
		return TokenPair{}, err
	}
	refresh, err := s.tokens.IssueRefresh(userID)
	if err != nil {
		return TokenPair{}, err
	}
	err = tx.RefreshSessions().Create(ctx, repository.NewRefreshSession{
		ID:        refresh.ID,
		UserID:    userID,
		FamilyID:  familyID,
		ExpiresAt: refresh.ExpiresAt,
	})
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: refresh.Token, ExpiresIn: expiresIn}, nil
}

// activeUser is the row behind a verified token, or a 401. Not a 404: the caller holds a signed
// token for a user that no longer exists or has been deactivated, which is a stale credential rather
// than a missing resource.
func (s *AuthService) activeUser(ctx context.Context, userID uuid.UUID) (repository.User, error) {
	user, err := s.store.Users().Get(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && !user.IsActive) {
		return repository.User{}, apperr.Unauthorized(refusalMessage)
	}
	if err != nil {
		return repository.User{}, fmt.Errorf("load caller: %w", err)
	}
	return user, nil
}

func userFrom(u repository.User) User {
	return User{ID: u.ID, Email: u.Email, IsActive: u.IsActive, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}
