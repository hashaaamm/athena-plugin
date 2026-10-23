package service_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"{{ cookiecutter.go_module }}/internal/apperr"
	"{{ cookiecutter.go_module }}/internal/repository"
	"{{ cookiecutter.go_module }}/internal/security"
	"{{ cookiecutter.go_module }}/internal/service"
	"{{ cookiecutter.go_module }}/internal/testdb"
)

// The auth service's rules, called directly against a real Postgres. Every test runs in a
// transaction rolled back at the end (internal/testdb), and every Store.WithTx inside the service
// is a savepoint within it — so the commit-or-rollback logic under test is the production one.

const (
	password    = "correct horse battery staple"
	newPassword = "a different passphrase entirely"
)

// Cheap Argon2 parameters: what these tests check is the rules around hashing, not its cost, and the
// production parameters under the race detector would make this package take minutes.
var cheapParams = security.Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

type harness struct {
	svc    *service.AuthService
	store  *repository.Store
	tokens *security.Tokens
	// sql runs a statement on the test's transaction, for the state no use case can produce yet —
	// a deactivated user, a shortened session — and scalar reads one value back.
	sql    func(t *testing.T, statement string, args ...any)
	scalar func(t *testing.T, query string, args ...any) int
}

func newHarness(t *testing.T) harness {
	t.Helper()
	return newHarnessWithTTL(t, 15*time.Minute, 30*24*time.Hour)
}

func newHarnessWithTTL(t *testing.T, access, refresh time.Duration) harness {
	t.Helper()
	tx := testdb.Tx(t)
	store := repository.New(tx)
	tokens := security.NewTokens(security.TokenConfig{
		Secret:     "a-test-signing-key-that-is-long-enough-0123456789",
		Issuer:     "test",
		Audience:   "test",
		AccessTTL:  access,
		RefreshTTL: refresh,
	})
	logger := slog.New(slog.DiscardHandler)
	return harness{
		svc:    service.NewAuthService(store, security.NewHasher(cheapParams, 0), tokens, logger),
		store:  store,
		tokens: tokens,
		sql: func(t *testing.T, statement string, args ...any) {
			t.Helper()
			if _, err := tx.Exec(context.Background(), statement, args...); err != nil {
				t.Fatal(err)
			}
		},
		scalar: func(t *testing.T, query string, args ...any) int {
			t.Helper()
			var value int
			if err := tx.QueryRow(context.Background(), query, args...).Scan(&value); err != nil {
				t.Fatal(err)
			}
			return value
		},
	}
}

func address() string { return "ada-" + uuid.NewString()[:8] + "@example.com" }

func (h harness) register(t *testing.T) service.User {
	t.Helper()
	user, err := h.svc.Register(context.Background(), address(), password)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func (h harness) login(t *testing.T, email string) service.TokenPair {
	t.Helper()
	pair, err := h.svc.Login(context.Background(), email, password)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func isStatus(err error, status int) bool {
	appErr, ok := apperr.As(err)
	return ok && appErr.Status == status
}

func (h harness) session(t *testing.T, refreshToken string) repository.RefreshSession {
	t.Helper()
	claims, err := h.tokens.Decode(refreshToken, security.Refresh)
	if err != nil {
		t.Fatal(err)
	}
	var row repository.RefreshSession
	err = h.store.WithTx(context.Background(), func(tx *repository.Store) error {
		row, err = tx.RefreshSessions().GetForUpdate(context.Background(), claims.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func (h harness) deactivate(t *testing.T, id uuid.UUID) {
	t.Helper()
	// The service has no deactivation use case yet, so the test reaches for the table directly.
	h.sql(t, "UPDATE users SET is_active = false WHERE id = $1", id)
}

// --- Register ---------------------------------------------------------------

func TestRegisterStoresADigestNotThePassword(t *testing.T) {
	h := newHarness(t)
	email := address()

	user, err := h.svc.Register(context.Background(), email, password)
	if err != nil {
		t.Fatal(err)
	}

	stored, err := h.store.Users().GetByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != user.ID || !user.IsActive {
		t.Fatalf("registered %+v, stored %+v", user, stored)
	}
	if digest := string(stored.HashedPassword); !strings.HasPrefix(digest, "$argon2id$") || strings.Contains(digest, password) {
		t.Fatal("the stored value is not an Argon2id digest of the password")
	}
}

func TestADuplicateEmailIsAConflict(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)

	_, err := h.svc.Register(context.Background(), user.Email, password)

	if !isStatus(err, 409) {
		t.Fatalf("Register(duplicate) = %v, want a 409", err)
	}
}

// --- Login ------------------------------------------------------------------

// Same error, same message: anything else turns login into a user directory.
func TestLoginRefusesAnUnknownEmailAndAWrongPasswordIdentically(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)

	_, wrongPassword := h.svc.Login(context.Background(), user.Email, newPassword)
	_, unknownEmail := h.svc.Login(context.Background(), address(), password)

	if !isStatus(wrongPassword, 401) || wrongPassword.Error() != unknownEmail.Error() {
		t.Fatalf("wrong password = %v, unknown email = %v; want the same 401", wrongPassword, unknownEmail)
	}
}

// "This account is disabled" confirms the account exists.
func TestADeactivatedUserCannotLogInAndIsNotToldWhy(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)
	h.deactivate(t, user.ID)

	_, deactivated := h.svc.Login(context.Background(), user.Email, password)
	_, unknown := h.svc.Login(context.Background(), address(), password)

	if !isStatus(deactivated, 401) || deactivated.Error() != unknown.Error() {
		t.Fatalf("deactivated = %v; want the same 401 as an unknown account", deactivated)
	}
}

// The row is keyed by the token's jti; the token itself is nowhere in the database.
func TestLoginRecordsTheRefreshTokenByJTIAndNeverTheToken(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)

	pair := h.login(t, user.Email)

	row := h.session(t, pair.RefreshToken)
	if row.UserID != user.ID || row.RevokedAt != nil || !row.ExpiresAt.After(time.Now().Add(29*24*time.Hour)) {
		t.Fatalf("session row %+v", row)
	}
	// No column of any row contains the token, or its signature.
	signature := pair.RefreshToken[strings.LastIndex(pair.RefreshToken, ".")+1:]
	if found := h.scalar(t, "SELECT count(*) FROM refresh_sessions r WHERE strpos(row_to_json(r)::text, $1) > 0", signature); found != 0 {
		t.Fatalf("the token itself is stored, in %d rows", found)
	}
}

func TestEachLoginIsAFamilyOfItsOwn(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)

	first := h.session(t, h.login(t, user.Email).RefreshToken)
	second := h.session(t, h.login(t, user.Email).RefreshToken)

	if first.FamilyID == second.FamilyID {
		t.Fatal("two sign-ins share a family: revoking one would sign out the other")
	}
}

// Login is the only moment the plaintext is in hand, so it is when an old digest is migrated.
func TestLoginUpgradesADigestMadeWithOldParameters(t *testing.T) {
	h := newHarness(t)
	email := address()
	weak := security.Params{Memory: 4 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	old, _ := security.NewHasher(weak, 0).Hash(context.Background(), password)
	if _, err := h.store.Users().Create(context.Background(), email, repository.PasswordDigest(old)); err != nil {
		t.Fatal(err)
	}

	h.login(t, email)

	stored, _ := h.store.Users().GetByEmail(context.Background(), email)
	if string(stored.HashedPassword) == old || !strings.Contains(string(stored.HashedPassword), "m=8192,t=1,p=1") {
		t.Fatalf("the digest was not migrated to the current parameters")
	}
}

// --- Refresh, rotation and reuse ----------------------------------------------

func TestRotationRevokesTheSpentRowAndContinuesTheFamily(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)
	first := h.login(t, user.Email)

	second, err := h.svc.Refresh(context.Background(), first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}

	spent, next := h.session(t, first.RefreshToken), h.session(t, second.RefreshToken)
	if spent.RevokedAt == nil || next.RevokedAt != nil || spent.FamilyID != next.FamilyID {
		t.Fatalf("spent %+v, next %+v; want the first revoked and the second live, in one family", spent, next)
	}
}

// The theft case. The legitimate client rotated first into second; first reappearing means somebody
// else holds a copy. The error is the one that keeps its writes, and the writes are the revocation —
// so the token the legitimate client holds is dead too, which is the point.
func TestAReplayIsRefusedWithTheErrorThatKeepsTheRevocation(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)
	first := h.login(t, user.Email)
	second, _ := h.svc.Refresh(context.Background(), first.RefreshToken)

	_, replay := h.svc.Refresh(context.Background(), first.RefreshToken)

	if !isStatus(replay, 401) || !apperr.KeepsWrites(replay) {
		t.Fatalf("replay = %v; want a 401 that keeps its writes", replay)
	}
	if h.session(t, second.RefreshToken).RevokedAt == nil {
		t.Fatal("the family survived the replay")
	}
	if _, err := h.svc.Refresh(context.Background(), second.RefreshToken); !isStatus(err, 401) {
		t.Fatalf("the descendant still refreshes after a replay: %v", err)
	}
}

func TestAReplayRevokesThatFamilyAndNoOther(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)
	here, elsewhere := h.login(t, user.Email), h.login(t, user.Email)
	_, _ = h.svc.Refresh(context.Background(), here.RefreshToken)
	_, _ = h.svc.Refresh(context.Background(), here.RefreshToken)

	if _, err := h.svc.Refresh(context.Background(), elsewhere.RefreshToken); err != nil {
		t.Fatalf("another device's session died with this one: %v", err)
	}
}

// Deactivation stops new access tokens at once. The one already issued runs out its fifteen minutes.
func TestADeactivatedUserCannotRefreshAndLosesTheFamily(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)
	pair := h.login(t, user.Email)
	h.deactivate(t, user.ID)

	_, err := h.svc.Refresh(context.Background(), pair.RefreshToken)

	if !isStatus(err, 401) || !apperr.KeepsWrites(err) {
		t.Fatalf("Refresh(deactivated) = %v; want a 401 that keeps its writes", err)
	}
	if h.session(t, pair.RefreshToken).RevokedAt == nil {
		t.Fatal("a deactivated user's family is still live")
	}
}

// The row is the authority: shortening a session is an UPDATE, not a re-issue.
func TestARowPastItsExpiryIsRefusedEvenIfTheTokenIsNot(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)
	pair := h.login(t, user.Email)
	row := h.session(t, pair.RefreshToken)
	h.sql(t, "UPDATE refresh_sessions SET expires_at = now() - interval '1 minute' WHERE id = $1", row.ID)

	if _, err := h.svc.Refresh(context.Background(), pair.RefreshToken); !isStatus(err, 401) {
		t.Fatalf("Refresh past the row's expiry = %v, want a 401", err)
	}
}

func TestAnExpiredRefreshTokenIsRefused(t *testing.T) {
	h := newHarnessWithTTL(t, 15*time.Minute, -time.Hour)
	user := h.register(t)
	pair := h.login(t, user.Email)

	if _, err := h.svc.Refresh(context.Background(), pair.RefreshToken); !isStatus(err, 401) {
		t.Fatalf("Refresh(expired) = %v, want a 401", err)
	}
}

func TestRefreshingWithNoTokenOrAnAccessTokenIsRefused(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)
	pair := h.login(t, user.Email)

	for _, token := range []string{"", "not-a-token", pair.AccessToken} {
		if _, err := h.svc.Refresh(context.Background(), token); !isStatus(err, 401) {
			t.Fatalf("Refresh(%.12q) = %v, want a 401", token, err)
		}
	}
}

// --- Logout -------------------------------------------------------------------

// Clients retry logout on a flaky network; an error from it protects nothing.
func TestLogoutRevokesTheFamilyAndToleratesAnything(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)
	first := h.login(t, user.Email)
	second, _ := h.svc.Refresh(context.Background(), first.RefreshToken)

	// Any token from the family names it — even a spent one.
	if err := h.svc.Logout(context.Background(), first.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if h.session(t, second.RefreshToken).RevokedAt == nil {
		t.Fatal("logout left the live token in the family usable")
	}
	for _, token := range []string{"", "not-a-token", first.RefreshToken, first.AccessToken} {
		if err := h.svc.Logout(context.Background(), token); err != nil {
			t.Fatalf("Logout(%.12q) = %v, want nil", token, err)
		}
	}
}

// --- Me -------------------------------------------------------------------------

// A signed token naming nobody is a stale credential, not a missing resource.
func TestMeRefusesATokenForAUserThatIsGoneOrDeactivated(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)

	if _, err := h.svc.Me(context.Background(), uuid.New()); !isStatus(err, 401) {
		t.Fatalf("Me(unknown) = %v, want a 401", err)
	}
	h.deactivate(t, user.ID)
	if _, err := h.svc.Me(context.Background(), user.ID); !isStatus(err, 401) {
		t.Fatalf("Me(deactivated) = %v, want a 401", err)
	}
}

// --- Change password --------------------------------------------------------------

func TestChangingAPasswordRevokesEveryFamilyAndReturnsANewOne(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)
	here, elsewhere := h.login(t, user.Email), h.login(t, user.Email)

	fresh, err := h.svc.ChangePassword(context.Background(), user.ID, password, newPassword)
	if err != nil {
		t.Fatal(err)
	}

	for _, old := range []string{here.RefreshToken, elsewhere.RefreshToken} {
		if h.session(t, old).RevokedAt == nil {
			t.Fatal("a session that knew the old password survived the change")
		}
	}
	row := h.session(t, fresh.RefreshToken)
	if row.RevokedAt != nil || row.FamilyID == h.session(t, here.RefreshToken).FamilyID {
		t.Fatal("the caller's new session is not a live, new family")
	}
	if _, err := h.svc.Login(context.Background(), user.Email, password); !isStatus(err, 401) {
		t.Fatal("the old password still works")
	}
	if _, err := h.svc.Login(context.Background(), user.Email, newPassword); err != nil {
		t.Fatalf("the new password does not work: %v", err)
	}
}

// 403, not 401: the session is valid, and a client that saw 401 would try to re-authenticate.
func TestChangingAPasswordNeedsTheCurrentOneAndEndsNothingWithoutIt(t *testing.T) {
	h := newHarness(t)
	user := h.register(t)
	pair := h.login(t, user.Email)

	_, err := h.svc.ChangePassword(context.Background(), user.ID, newPassword, newPassword)

	if !isStatus(err, 403) {
		t.Fatalf("ChangePassword(wrong current) = %v, want a 403", err)
	}
	if h.session(t, pair.RefreshToken).RevokedAt != nil {
		t.Fatal("a refused password change revoked a session")
	}
}
