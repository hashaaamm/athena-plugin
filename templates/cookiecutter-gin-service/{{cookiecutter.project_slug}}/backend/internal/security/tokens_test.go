package security

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"{{ cookiecutter.go_module }}/internal/apperr"
)

// The decoder, which is the part of the auth path with the vulnerability classes in it.
//
// Most tests below craft a token by hand and assert it is refused. That is deliberate: a suite that
// only decodes tokens this service minted proves the happy path and nothing about the parser
// options, which is where the CVEs live. One test per class.

const testSecret = "a-test-signing-key-that-is-long-enough-for-hs512-too-0123456789abcdef"

func testTokens(t *testing.T, accessTTL, refreshTTL time.Duration) *Tokens {
	t.Helper()
	return NewTokens(TokenConfig{
		Secret:     testSecret,
		Issuer:     "test-issuer",
		Audience:   "test-audience",
		AccessTTL:  accessTTL,
		RefreshTTL: refreshTTL,
	})
}

func defaultTokens(t *testing.T) *Tokens {
	t.Helper()
	return testTokens(t, 15*time.Minute, 30*24*time.Hour)
}

func validClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"sub": uuid.NewString(),
		"typ": string(Access),
		"iss": "test-issuer",
		"aud": "test-audience",
		"iat": now.Unix(),
		"exp": now.Add(15 * time.Minute).Unix(),
		"jti": uuid.NewString(),
	}
}

func sign(t *testing.T, claims jwt.MapClaims, method jwt.SigningMethod, key any) string {
	t.Helper()
	token, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return token
}

func assertRefused(t *testing.T, tokens *Tokens, token string, expected TokenType) {
	t.Helper()
	_, err := tokens.Decode(token, expected)
	appErr, ok := apperr.As(err)
	if !ok || appErr.Status != 401 || appErr.Message != "Invalid or expired token" {
		t.Fatalf("Decode = %v; want the one 401 every refused token gets", err)
	}
}

func TestAMintedAccessTokenRoundTripsAndSaysHowLongItLives(t *testing.T) {
	tokens := defaultTokens(t)
	userID := uuid.New()

	token, expiresIn, err := tokens.IssueAccess(userID)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := tokens.Decode(token, Access)
	if err != nil {
		t.Fatal(err)
	}

	if verified.Subject != userID || verified.Type != Access {
		t.Fatalf("decoded %+v, want subject %s and type access", verified, userID)
	}
	if expiresIn != 900 || verified.ExpiresAt.Sub(verified.IssuedAt) != 15*time.Minute {
		t.Fatalf("expires_in %d, lifetime %v; want 900 and 15m", expiresIn, verified.ExpiresAt.Sub(verified.IssuedAt))
	}
}

// Same encoder, typ=refresh, the longer lifetime, and the jti its row is keyed by.
func TestAMintedRefreshTokenRoundTripsAndNamesItsRow(t *testing.T) {
	tokens := defaultTokens(t)
	userID := uuid.New()

	issued, err := tokens.IssueRefresh(userID)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := tokens.Decode(issued.Token, Refresh)
	if err != nil {
		t.Fatal(err)
	}

	if verified.Subject != userID || verified.ID != issued.ID {
		t.Fatalf("decoded %+v, want subject %s and jti %s", verified, userID, issued.ID)
	}
	if !verified.ExpiresAt.Equal(issued.ExpiresAt) {
		t.Fatalf("token exp %v and row expiry %v differ", verified.ExpiresAt, issued.ExpiresAt)
	}
	if verified.ExpiresAt.Sub(verified.IssuedAt) != 30*24*time.Hour {
		t.Fatalf("refresh lifetime %v, want 30 days", verified.ExpiresAt.Sub(verified.IssuedAt))
	}
}

// The class people miss: same key, same claims, thirty days instead of fifteen minutes. Proven on a
// token the real encoder minted, so it also proves the encoder sets typ.
func TestARefreshTokenIsRefusedAsABearerToken(t *testing.T) {
	tokens := defaultTokens(t)
	issued, _ := tokens.IssueRefresh(uuid.New())

	assertRefused(t, tokens, issued.Token, Access)
}

func TestAnAccessTokenCannotBeSpentAsARefreshToken(t *testing.T) {
	tokens := defaultTokens(t)
	token, _, _ := tokens.IssueAccess(uuid.New())

	assertRefused(t, tokens, token, Refresh)
}

// Minted through the real encoder with the lifetime turned negative, so this also proves exp is set.
func TestAnExpiredTokenIsRefused(t *testing.T) {
	expired := testTokens(t, -time.Minute, -24*time.Hour)
	verifier := defaultTokens(t)
	access, _, _ := expired.IssueAccess(uuid.New())
	refresh, _ := expired.IssueRefresh(uuid.New())

	assertRefused(t, verifier, access, Access)
	assertRefused(t, verifier, refresh.Token, Refresh)
}

func TestATamperedSignatureIsRefused(t *testing.T) {
	tokens := defaultTokens(t)
	token, _, _ := tokens.IssueAccess(uuid.New())
	head := token[:strings.LastIndex(token, ".")+1]
	signature := token[len(head):]
	flipped := "A"
	if signature[0] == 'A' {
		flipped = "B"
	}

	assertRefused(t, tokens, head+flipped+signature[1:], Access)
}

func TestATokenSignedWithAnotherKeyIsRefused(t *testing.T) {
	tokens := defaultTokens(t)

	assertRefused(t, tokens, sign(t, validClaims(), jwt.SigningMethodHS256, []byte("somebody-elses-key-of-a-decent-length-000")), Access)
}

// A claim set this service did not issue. Each case is one vulnerability class or one required
// claim; the token is otherwise valid and signed with the service's own key and algorithm.
func TestAClaimSetThisServiceDidNotIssueIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(jwt.MapClaims)
	}{
		// No expiry means a token that verifies for ever.
		{name: "missing exp", mutate: func(c jwt.MapClaims) { delete(c, "exp") }},
		{name: "missing iat", mutate: func(c jwt.MapClaims) { delete(c, "iat") }},
		// A token with no subject names nobody.
		{name: "missing sub", mutate: func(c jwt.MapClaims) { delete(c, "sub") }},
		// A token that cannot be named cannot be denied later.
		{name: "missing jti", mutate: func(c jwt.MapClaims) { delete(c, "jti") }},
		// An unlabelled token is usable as any kind.
		{name: "missing typ", mutate: func(c jwt.MapClaims) { delete(c, "typ") }},
		// Staging's tokens, or another service's that shares the key.
		{name: "wrong iss", mutate: func(c jwt.MapClaims) { c["iss"] = "somebody-else" }},
		{name: "wrong aud", mutate: func(c jwt.MapClaims) { c["aud"] = "somebody-else" }},
		{name: "wrong typ", mutate: func(c jwt.MapClaims) { c["typ"] = string(Refresh) }},
		{name: "expired", mutate: func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() }},
		// Issued in the future, beyond clock skew: a token minted by a clock somebody controls.
		{name: "iat in the future", mutate: func(c jwt.MapClaims) { c["iat"] = time.Now().Add(time.Hour).Unix() }},
		// It verified, so it came from something holding the key — and it is still not one of ours.
		{name: "sub not a uuid", mutate: func(c jwt.MapClaims) { c["sub"] = "not-a-uuid" }},
		{name: "jti not a uuid", mutate: func(c jwt.MapClaims) { c["jti"] = "not-a-uuid" }},
	}
	tokens := defaultTokens(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := validClaims()
			tc.mutate(claims)

			assertRefused(t, tokens, sign(t, claims, jwt.SigningMethodHS256, []byte(testSecret)), Access)
		})
	}
}

// The allowlist is an allowlist, never the algorithm the token names: signed with the service's own
// key, so the rejection can only be about the algorithm.
func TestATokenSignedWithAnotherHMACAlgorithmIsRefused(t *testing.T) {
	tokens := defaultTokens(t)

	for _, method := range []jwt.SigningMethod{jwt.SigningMethodHS384, jwt.SigningMethodHS512} {
		t.Run(method.Alg(), func(t *testing.T) {
			assertRefused(t, tokens, sign(t, validClaims(), method, []byte(testSecret)), Access)
		})
	}
}

// alg=none is the oldest JWT attack there is, and it still gets shipped.
func TestAnUnsignedTokenIsRefused(t *testing.T) {
	tokens := defaultTokens(t)

	assertRefused(t, tokens, sign(t, validClaims(), jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType), Access)
}

// An RS256 token. Against a decoder that trusted the header and fed it an HMAC secret as a "public
// key", this is the algorithm-confusion attack; here it is simply not HS256.
func TestAnRSASignedTokenIsRefused(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tokens := defaultTokens(t)

	assertRefused(t, tokens, sign(t, validClaims(), jwt.SigningMethodRS256, key), Access)
}

func TestSomethingThatIsNotATokenIsRefused(t *testing.T) {
	tokens := defaultTokens(t)

	for _, token := range []string{"", "not-a-token", "a.b.c", strings.Repeat("x", 4096)} {
		assertRefused(t, tokens, token, Access)
	}
}
