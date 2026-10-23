package security

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"{{ cookiecutter.go_module }}/internal/apperr"
)

// Minting and verifying tokens: golang-jwt v5, HS256, one encoder and one decoder.
//
// Two kinds of token come out of the one encoder, told apart by the `typ` claim. An access token is
// the bearer credential: fifteen minutes, checked against nothing but its signature. A refresh token
// mints the next pair: thirty days, no permissions in it, good only at POST /auth/refresh and only
// while its row in refresh_sessions says so. The decoder takes the kind it expects as an argument,
// which is what stops one being spent as the other.
//
// HS256 is right while one service both signs and verifies. The moment a second service must verify
// a token this one issued, HS256 means handing it the *signing* key; that is when this becomes RS256
// or EdDSA with a published JWKS, and it is a bigger change than editing this file.

// Algorithm is the only signing algorithm minted or accepted.
const Algorithm = "HS256"

// leeway absorbs clock skew between instances. It is not a grace period.
const leeway = 10 * time.Second

// errRefused is the one message for every refused token. Absent, malformed, expired, wrong issuer,
// wrong type: telling a caller which one it was tells an attacker which half of the guess was right.
const errRefused = "Invalid or expired token"

// TokenType is what a token is for, carried as `typ` and compared on every decode.
//
// The comparison is what stops a thirty-day refresh token being presented as a bearer token on every
// guarded route. Both kinds are signed with the same key and carry the same claims, so nothing else
// in the decoder tells them apart.
type TokenType string

// The two kinds.
const (
	Access  TokenType = "access"
	Refresh TokenType = "refresh"
)

// claims is the wire form. Unknown claims are ignored on decode, deliberately: a claim added by a
// newer issuer must not lock out every token already signed.
type claims struct {
	Typ string `json:"typ"`
	jwt.RegisteredClaims
}

// Verified is what a token that passed every check says.
type Verified struct {
	Subject   uuid.UUID
	ID        uuid.UUID
	Type      TokenType
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// IssuedRefreshToken is a minted refresh token and what its refresh_sessions row must record.
type IssuedRefreshToken struct {
	Token string
	// The token's `jti`, which is the row's primary key. The token itself is never stored.
	ID uuid.UUID
	// The token's `exp`, from the same clock read, so the row cannot outlive the token.
	ExpiresAt time.Time
}

// TokenConfig is everything the encoder and decoder need. Built from config.Config in internal/app.
type TokenConfig struct {
	Secret     string
	Issuer     string
	Audience   string
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

// Tokens mints and verifies this service's tokens.
type Tokens struct {
	cfg    TokenConfig
	key    []byte
	parser *jwt.Parser
	now    func() time.Time
}

// NewTokens builds the encoder and decoder.
//
// Every parser option below is a documented vulnerability class if it is left out:
//
//   - WithValidMethods is an allowlist, never the algorithm the token names. Without it a token
//     signed `alg: none`, or with HS512 over the same key, is the algorithm-confusion attack.
//   - WithExpirationRequired, because the library otherwise checks `exp` only when it is present —
//     a token minted without one verifies for ever.
//   - WithIssuer and WithAudience stop a token from staging, or from another service sharing the
//     key, being accepted here.
//   - `typ` compared against the expected kind (in Decode) is the one people miss.
func NewTokens(cfg TokenConfig) *Tokens {
	return &Tokens{
		cfg: cfg,
		key: []byte(cfg.Secret),
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{Algorithm}),
			jwt.WithIssuer(cfg.Issuer),
			jwt.WithAudience(cfg.Audience),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(leeway),
		),
		now: time.Now,
	}
}

// IssueAccess mints an access token and returns it with its lifetime in seconds.
func (t *Tokens) IssueAccess(userID uuid.UUID) (string, int, error) {
	token, _, _, err := t.encode(userID, Access, t.cfg.AccessTTL)
	if err != nil {
		return "", 0, err
	}
	return token, int(t.cfg.AccessTTL / time.Second), nil
}

// IssueRefresh mints a refresh token: the same encoder, `typ=refresh`, the longer lifetime.
//
// It carries no permissions. It is never a bearer credential — RequireActor refuses it — so the
// only thing it can say is who it was issued to, and the row named by its `jti` says whether that is
// still true.
func (t *Tokens) IssueRefresh(userID uuid.UUID) (IssuedRefreshToken, error) {
	token, id, expiresAt, err := t.encode(userID, Refresh, t.cfg.RefreshTTL)
	if err != nil {
		return IssuedRefreshToken{}, err
	}
	return IssuedRefreshToken{Token: token, ID: id, ExpiresAt: expiresAt}, nil
}

// Decode verifies a token of the expected kind and returns what it says. Anything less is the same
// apperr.Unauthorized, whatever the reason.
func (t *Tokens) Decode(token string, expected TokenType) (Verified, error) {
	var parsed claims
	_, err := t.parser.ParseWithClaims(token, &parsed, func(tok *jwt.Token) (any, error) {
		// Redundant with WithValidMethods, and kept: the key is only ever handed to HMAC.
		if _, ok := tok.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", tok.Header["alg"])
		}
		return t.key, nil
	})
	if err != nil {
		return Verified{}, refused(err)
	}
	// The library verifies `iat` when present; this service never mints a token without one.
	if parsed.IssuedAt == nil || parsed.ExpiresAt == nil {
		return Verified{}, refused(errors.New("missing iat or exp"))
	}
	if TokenType(parsed.Typ) != expected {
		return Verified{}, refused(fmt.Errorf("typ %q, expected %q", parsed.Typ, expected))
	}
	// A signed token whose claims are the wrong shape. It verified, so it came from something holding
	// the key, but it is not a token this service issued.
	subject, err := uuid.Parse(parsed.Subject)
	if err != nil {
		return Verified{}, refused(err)
	}
	id, err := uuid.Parse(parsed.ID)
	if err != nil {
		return Verified{}, refused(err)
	}
	return Verified{
		Subject:   subject,
		ID:        id,
		Type:      expected,
		IssuedAt:  parsed.IssuedAt.Time,
		ExpiresAt: parsed.ExpiresAt.Time,
	}, nil
}

// encode signs a token and returns it with its `jti` and expiry, which a refresh row needs.
func (t *Tokens) encode(userID uuid.UUID, typ TokenType, ttl time.Duration) (string, uuid.UUID, time.Time, error) {
	// Whole seconds, because that is what a NumericDate carries: the expiry recorded on a row must be
	// the expiry in the token, not a nanosecond-precise time the token cannot express.
	now := t.now().UTC().Truncate(time.Second)
	expiresAt := now.Add(ttl)
	// Unique per token. A refresh token's is the key of its row, which is how a token is found again
	// without being stored. Nothing revokes an access token — it lives fifteen minutes — but a token
	// that cannot be named cannot be denied later either.
	id, err := uuid.NewRandom()
	if err != nil {
		return "", uuid.Nil, time.Time{}, fmt.Errorf("generate jti: %w", err)
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		Typ: string(typ),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			Issuer:    t.cfg.Issuer,
			Audience:  jwt.ClaimStrings{t.cfg.Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        id.String(),
		},
	}).SignedString(t.key)
	if err != nil {
		return "", uuid.Nil, time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, id, expiresAt, nil
}

// refused is the one 401 every failed check becomes. The cause stays on the error chain for a
// debugger; the transport renders only the apperr message, which never says which check failed.
func refused(cause error) error {
	return fmt.Errorf("%w (%w)", apperr.Unauthorized(errRefused), cause)
}
