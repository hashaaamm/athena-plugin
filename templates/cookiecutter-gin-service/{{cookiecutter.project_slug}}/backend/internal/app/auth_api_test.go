package app_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Register, login, me and change-password through the router, on the default (body) transport.
// What differs between the transports is covered by sessions_api_test.go and refresh_cookie_test.go.

func TestRegisterReturnsTheUserAndNeverTheDigest(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))
	payload := registerPayload()

	response := a.call(http.MethodPost, "/api/v1/auth/register", payload, nil)

	expectStatus(t, response, http.StatusCreated)
	body := object(t, response)
	if body["email"] != payload["email"] || body["is_active"] != true {
		t.Fatalf("register returned %v", body)
	}
	if _, err := uuid.Parse(body["id"].(string)); err != nil {
		t.Fatalf("id %v is not a UUID", body["id"])
	}
	for _, field := range []string{"id", "email", "is_active", "created_at", "updated_at"} {
		if _, ok := body[field]; !ok {
			t.Errorf("UserRead is missing %s", field)
		}
	}
	text := response.Body.String()
	if strings.Contains(text, "hashed_password") || strings.Contains(text, "argon2") || strings.Contains(text, password) {
		t.Fatalf("the response leaks the password or its digest: %s", text)
	}
}

func TestADuplicateEmailIsRefusedInTheDocumentedShape(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))
	payload := a.registered()

	response := a.call(http.MethodPost, "/api/v1/auth/register", payload, nil)

	expectStatus(t, response, http.StatusConflict)
	if code := errorCode(t, response); code != "conflict" {
		t.Fatalf("code %q, want conflict", code)
	}
}

// Two rows differing only in case is an account-takeover shape.
func TestTheEmailIsNormalisedAtTheBoundary(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))
	local := "Ada-" + uuid.NewString()[:8]

	created := a.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"email": "  " + local + "@Example.COM ", "password": password}, nil)
	expectStatus(t, created, http.StatusCreated)
	if got := object(t, created)["email"]; got != strings.ToLower(local)+"@example.com" {
		t.Fatalf("stored email %v", got)
	}

	again := a.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"email": strings.ToLower(local) + "@example.com", "password": password}, nil)
	expectStatus(t, again, http.StatusConflict)
	login := a.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"email": strings.ToUpper(local) + "@EXAMPLE.com", "password": password}, nil)
	expectStatus(t, login, http.StatusOK)
}

func TestRegistrationValidation(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))
	cases := []struct {
		name string
		body any
		want int
	}{
		{name: "password too short", body: map[string]any{"email": "ada@example.com", "password": "short"}, want: 422},
		{name: "password too long", body: map[string]any{"email": "ada@example.com", "password": strings.Repeat("x", 1025)}, want: 422},
		{name: "email has no @", body: map[string]any{"email": "not-an-email", "password": password}, want: 422},
		{name: "email empty", body: map[string]any{"email": "", "password": password}, want: 422},
		{name: "email missing", body: map[string]any{"password": password}, want: 422},
		{name: "password not a string", body: `{"email": "ada@example.com", "password": 12}`, want: 422},
		{name: "not JSON", body: `{"email":`, want: 422},
		{name: "no body", body: nil, want: 422},
		{name: "valid", body: registerPayload(), want: 201},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := a.call(http.MethodPost, "/api/v1/auth/register", tc.body, nil)

			expectStatus(t, response, tc.want)
			if tc.want == 422 {
				if code := errorCode(t, response); code != "validation_error" {
					t.Fatalf("code %q, want validation_error", code)
				}
				if strings.Contains(response.Body.String(), password) {
					t.Fatal("a validation message echoed the password")
				}
			}
		})
	}
}

// Without a body limit, the 1024-character password cap never gets the chance to refuse 2 MiB.
func TestAnOversizedBodyIsRefusedBeforeItIsRead(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))
	huge := `{"email": "ada@example.com", "password": "` + strings.Repeat("x", 2<<20) + `"}`

	response := a.call(http.MethodPost, "/api/v1/auth/register", huge, nil)

	expectStatus(t, response, http.StatusRequestEntityTooLarge)
}

// --- Login --------------------------------------------------------------------

func TestLoginReturnsAUsableBearerTokenAndSaysHowLongItLives(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))
	payload, login := a.loggedIn()

	body := object(t, login)
	if body["token_type"] != "bearer" || body["expires_in"] != float64(900) {
		t.Fatalf("login returned %v", body)
	}
	me := a.call(http.MethodGet, "/api/v1/auth/me", nil, bearer(accessToken(t, login)))
	expectStatus(t, me, http.StatusOK)
	if object(t, me)["email"] != payload["email"] {
		t.Fatalf("me returned %v", object(t, me))
	}
}

// Same status, same body. Anything else turns this endpoint into a user directory.
func TestAnUnknownEmailAndAWrongPasswordAreIndistinguishable(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))
	payload := a.registered()

	wrongPassword := a.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"email": payload["email"], "password": newPassword}, nil)
	unknownEmail := a.call(http.MethodPost, "/api/v1/auth/login", registerPayload(), nil)

	expectStatus(t, wrongPassword, http.StatusUnauthorized)
	expectStatus(t, unknownEmail, http.StatusUnauthorized)
	if wrongPassword.Body.String() != unknownEmail.Body.String() {
		t.Fatalf("the bodies differ:\n%s\n%s", wrongPassword.Body.String(), unknownEmail.Body.String())
	}
	if errorCode(t, wrongPassword) != "unauthorized" || wrongPassword.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatal("a refused login is not the documented 401")
	}
	if text := wrongPassword.Body.String(); strings.Contains(text, "argon2") || strings.Contains(text, payload["email"].(string)) {
		t.Fatalf("a failed login leaks: %s", text)
	}
}

// --- Me -------------------------------------------------------------------------

// 401 and not 403, with WWW-Authenticate — or a client will not try to authenticate.
func TestMeWithoutATokenIs401AndSaysWhichScheme(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))

	response := a.call(http.MethodGet, "/api/v1/auth/me", nil, nil)

	expectStatus(t, response, http.StatusUnauthorized)
	if response.Header().Get("WWW-Authenticate") != "Bearer" || errorCode(t, response) != "unauthorized" {
		t.Fatalf("headers %v, body %s", response.Header(), response.Body.String())
	}
}

func TestMeWithACredentialThatIsNotOneIs401(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))
	_, login := a.loggedIn()
	token := accessToken(t, login)
	head := token[:strings.LastIndex(token, ".")+1]
	flipped := "A"
	if token[len(head)] == 'A' {
		flipped = "B"
	}

	for name, header := range map[string]string{
		"garbage token": "Bearer not-a-token",
		"wrong scheme":  "Basic YWRhOnNlY3JldA==",
		"empty token":   "Bearer ",
		"tampered":      "Bearer " + head + flipped + token[len(head)+1:],
	} {
		response := a.call(http.MethodGet, "/api/v1/auth/me", nil, map[string]string{"Authorization": header})
		if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: status %d, headers %v", name, response.Code, response.Header())
		}
	}
}

// Minted through the real encoder with the lifetime turned negative, so this proves the route
// rejects an expired token rather than that a test can craft a bad one.
func TestMeWithAnExpiredTokenIs401(t *testing.T) {
	cfg := testConfig(t, nil)
	a := newAPI(t, cfg)
	_, login := a.loggedIn()
	me := a.call(http.MethodGet, "/api/v1/auth/me", nil, bearer(accessToken(t, login)))
	userID := uuid.MustParse(object(t, me)["id"].(string))
	cfg.AccessTokenTTLSeconds = -60
	expired, _, _ := tokensFor(cfg).IssueAccess(userID)

	response := a.call(http.MethodGet, "/api/v1/auth/me", nil, bearer(expired))

	expectStatus(t, response, http.StatusUnauthorized)
}

// The token says who the caller is, not what is true about them now.
func TestMeRefusesADeletedUsersStillValidToken(t *testing.T) {
	cfg := testConfig(t, nil)
	a := newAPI(t, cfg)
	token, _, _ := tokensFor(cfg).IssueAccess(uuid.New())

	response := a.call(http.MethodGet, "/api/v1/auth/me", nil, bearer(token))

	expectStatus(t, response, http.StatusUnauthorized)
}

// --- Change password --------------------------------------------------------------

func TestChangingAPasswordRetiresTheOldOne(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))
	payload, login := a.loggedIn()

	changed := a.call(http.MethodPost, "/api/v1/auth/change-password",
		map[string]any{"current_password": password, "new_password": newPassword}, bearer(accessToken(t, login)))

	expectStatus(t, changed, http.StatusOK)
	withOld := a.call(http.MethodPost, "/api/v1/auth/login", payload, nil)
	expectStatus(t, withOld, http.StatusUnauthorized)
	withNew := a.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"email": payload["email"], "password": newPassword}, nil)
	expectStatus(t, withNew, http.StatusOK)
}

// 403, not 401: the session is fine, and the web client ignores 403 rather than signing out.
// "" is a wrong password, not a missing field.
func TestChangingAPasswordNeedsTheCurrentOne(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))
	_, login := a.loggedIn()

	for _, current := range []string{newPassword, ""} {
		response := a.call(http.MethodPost, "/api/v1/auth/change-password",
			map[string]any{"current_password": current, "new_password": newPassword}, bearer(accessToken(t, login)))
		expectStatus(t, response, http.StatusForbidden)
		if code := errorCode(t, response); code != "forbidden" {
			t.Fatalf("code %q, want forbidden", code)
		}
	}
}

func TestChangingAPasswordWithoutATokenIs401(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))

	response := a.call(http.MethodPost, "/api/v1/auth/change-password",
		map[string]any{"current_password": password, "new_password": newPassword}, nil)

	expectStatus(t, response, http.StatusUnauthorized)
}

func TestTheIssuedTokensLiveAsLongAsConfigured(t *testing.T) {
	cfg := testConfig(t, map[string]string{"ACCESS_TOKEN_TTL_SECONDS": "60"})
	a := newAPI(t, cfg)
	_, login := a.loggedIn()

	verified, err := tokensFor(cfg).Decode(accessToken(t, login), "access")
	if err != nil {
		t.Fatal(err)
	}
	if lifetime := verified.ExpiresAt.Sub(verified.IssuedAt); lifetime != time.Minute || object(t, login)["expires_in"] != float64(60) {
		t.Fatalf("lifetime %v, expires_in %v; want 1m and 60", lifetime, object(t, login)["expires_in"])
	}
}
