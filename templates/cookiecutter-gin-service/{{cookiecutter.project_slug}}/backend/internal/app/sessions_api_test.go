package app_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"{{ cookiecutter.go_module }}/internal/http/handler"
	"{{ cookiecutter.go_module }}/internal/http/middleware"
	"{{ cookiecutter.go_module }}/internal/security"
)

// Refresh, rotation, reuse detection, logout and revocation, through the full stack — once per
// refresh-token transport. Every test runs under both: the rules are the service's and must not
// depend on where the token travels.
//
// Every request here runs through the real commit-or-rollback decision, which is what makes the
// reuse tests mean something. A family revoked on the way to a 401 is a write made by a request that
// failed; were it rolled back like any other failure, the 401 would still go out, and the test that
// asserts the *next* token is dead is the only thing that would notice.

// transport is how a test presents a refresh token and reads one back, for one REFRESH_TRANSPORT.
type transport struct {
	name string
	vars map[string]string
	// tokenIn reads the refresh token a response delivered.
	tokenIn func(t *testing.T, response *httptest.ResponseRecorder) string
	// present is the body and headers that send a refresh token back.
	present func(token string) (any, map[string]string)
}

var transports = []transport{
	{
		name: "body",
		vars: map[string]string{},
		tokenIn: func(t *testing.T, response *httptest.ResponseRecorder) string {
			t.Helper()
			token, _ := object(t, response)["refresh_token"].(string)
			if token == "" {
				t.Fatalf("no refresh_token in %s", response.Body.String())
			}
			return token
		},
		present: func(token string) (any, map[string]string) {
			return map[string]any{"refresh_token": token}, nil
		},
	},
	{
		name: "cookie",
		vars: map[string]string{"REFRESH_TRANSPORT": "cookie", "CORS_ORIGINS": origin},
		tokenIn: func(t *testing.T, response *httptest.ResponseRecorder) string {
			t.Helper()
			value, _ := refreshCookie(t, response)
			return value
		},
		// What a browser sends: the cookie, and the CSRF header the web client adds.
		present: func(token string) (any, map[string]string) {
			return nil, map[string]string{"Cookie": handler.RefreshCookieName + "=" + token, middleware.CSRFHeader: "fetch"}
		},
	},
}

// refreshCookie is the refresh cookie a response set: its value and its attributes, lower-cased.
// Read from the raw Set-Cookie header rather than a cookie jar, so a test can present any token it
// was ever given — including a spent one — and assert exactly what the browser was told.
func refreshCookie(t *testing.T, response *httptest.ResponseRecorder) (string, map[string]string) {
	t.Helper()
	for _, header := range response.Header().Values("Set-Cookie") {
		parts := strings.Split(header, ";")
		name, value, _ := strings.Cut(strings.TrimSpace(parts[0]), "=")
		if name != handler.RefreshCookieName {
			continue
		}
		attributes := map[string]string{}
		for _, part := range parts[1:] {
			key, attributeValue, _ := strings.Cut(strings.TrimSpace(part), "=")
			attributes[strings.ToLower(key)] = attributeValue
		}
		return value, attributes
	}
	t.Fatalf("no %s cookie in %v", handler.RefreshCookieName, response.Header().Values("Set-Cookie"))
	return "", nil
}

// forEachTransport runs a test once per transport, on a router configured for it.
func forEachTransport(t *testing.T, test func(t *testing.T, a *api, tr transport)) {
	t.Helper()
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			test(t, newAPI(t, testConfig(t, tr.vars)), tr)
		})
	}
}

func (a *api) refresh(tr transport, token string) *httptest.ResponseRecorder {
	a.t.Helper()
	body, headers := tr.present(token)
	return a.call(http.MethodPost, "/api/v1/auth/refresh", body, headers)
}

func (a *api) logout(tr transport, token string) *httptest.ResponseRecorder {
	a.t.Helper()
	body, headers := tr.present(token)
	return a.call(http.MethodPost, "/api/v1/auth/logout", body, headers)
}

// signedIn registers and logs in; returns the credentials, an access token and a refresh token.
func (a *api) signedIn(tr transport) (map[string]any, string, string) {
	a.t.Helper()
	payload, login := a.loggedIn()
	return payload, accessToken(a.t, login), tr.tokenIn(a.t, login)
}

// anotherDevice signs the same account in again and returns that session's refresh token.
func (a *api) anotherDevice(tr transport, payload map[string]any) string {
	a.t.Helper()
	login := a.call(http.MethodPost, "/api/v1/auth/login", payload, nil)
	expectStatus(a.t, login, http.StatusOK)
	return tr.tokenIn(a.t, login)
}

// --- Login ----------------------------------------------------------------------

func TestLoginIssuesARefreshTokenThatIsARefreshToken(t *testing.T) {
	forEachTransport(t, func(t *testing.T, a *api, tr transport) {
		_, access, refresh := a.signedIn(tr)

		verified, err := tokensFor(testConfig(t, tr.vars)).Decode(refresh, security.Refresh)
		if err != nil {
			t.Fatal(err)
		}
		if verified.ExpiresAt.Sub(verified.IssuedAt) != 30*24*time.Hour || refresh == access {
			t.Fatalf("refresh token %+v", verified)
		}
	})
}

// --- Refresh and rotation -----------------------------------------------------------

func TestRefreshReturnsANewPairThatWorks(t *testing.T) {
	forEachTransport(t, func(t *testing.T, a *api, tr transport) {
		payload, _, first := a.signedIn(tr)

		response := a.refresh(tr, first)

		expectStatus(t, response, http.StatusOK)
		if second := tr.tokenIn(t, response); second == first {
			t.Fatal("refresh returned the token it was given")
		}
		if object(t, response)["expires_in"] != float64(900) {
			t.Fatalf("expires_in = %v", object(t, response)["expires_in"])
		}
		me := a.call(http.MethodGet, "/api/v1/auth/me", nil, bearer(accessToken(t, response)))
		expectStatus(t, me, http.StatusOK)
		if object(t, me)["email"] != payload["email"] {
			t.Fatal("the refreshed access token names somebody else")
		}
	})
}

func TestARefreshTokenIsSpentByUsingIt(t *testing.T) {
	forEachTransport(t, func(t *testing.T, a *api, tr transport) {
		_, _, first := a.signedIn(tr)
		expectStatus(t, a.refresh(tr, first), http.StatusOK)

		again := a.refresh(tr, first)

		expectStatus(t, again, http.StatusUnauthorized)
		if errorCode(t, again) != "unauthorized" || again.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatal("a spent token is not the documented 401")
		}
	})
}

// The theft case. The legitimate client rotated first into second; first reappearing means somebody
// else holds a copy, and from then on nothing in that family works — including the token the
// legitimate client holds, which is the point: neither side can be trusted.
func TestASpentTokenComingBackRevokesItsWholeFamily(t *testing.T) {
	forEachTransport(t, func(t *testing.T, a *api, tr transport) {
		_, _, first := a.signedIn(tr)
		second := tr.tokenIn(t, a.refresh(tr, first))

		replay := a.refresh(tr, first)
		after := a.refresh(tr, second)

		expectStatus(t, replay, http.StatusUnauthorized)
		expectStatus(t, after, http.StatusUnauthorized)
	})
}

// Another device's sign-in is a different family; a replay on this one says nothing about it.
func TestReuseRevokesThatFamilyAndNoOther(t *testing.T) {
	forEachTransport(t, func(t *testing.T, a *api, tr transport) {
		payload, _, first := a.signedIn(tr)
		elsewhere := a.anotherDevice(tr, payload)
		a.refresh(tr, first)
		a.refresh(tr, first)

		expectStatus(t, a.refresh(tr, elsewhere), http.StatusOK)
	})
}

// Minted through the real login with the lifetime turned negative, so this proves the expiry is set
// and enforced rather than that a test can craft a bad token.
func TestAnExpiredRefreshTokenIsRefused(t *testing.T) {
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			cfg := testConfig(t, tr.vars)
			cfg.RefreshTokenTTLDays = -1
			a := newAPI(t, cfg)
			_, _, expired := a.signedIn(tr)

			expectStatus(t, a.refresh(tr, expired), http.StatusUnauthorized)
		})
	}
}

func TestSomethingThatIsNotARefreshTokenIsRefused(t *testing.T) {
	forEachTransport(t, func(t *testing.T, a *api, tr transport) {
		_, access, _ := a.signedIn(tr)

		for _, token := range []string{"not-a-token", "", access} {
			response := a.refresh(tr, token)
			if response.Code != http.StatusUnauthorized {
				t.Errorf("refresh(%.12q) = %d, want 401", token, response.Code)
			}
		}
	})
}

// Same key, same claims, thirty days instead of fifteen minutes. `typ` is the only thing stopping it
// opening every guarded route, and this is the test that fails if it stops.
func TestARefreshTokenIsRefusedAsABearerToken(t *testing.T) {
	forEachTransport(t, func(t *testing.T, a *api, tr transport) {
		_, _, refresh := a.signedIn(tr)

		response := a.call(http.MethodGet, "/api/v1/auth/me", nil, bearer(refresh))

		expectStatus(t, response, http.StatusUnauthorized)
		if response.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatal("401 without WWW-Authenticate")
		}
	})
}

// --- Logout ---------------------------------------------------------------------------

func TestLogoutEndsTheSession(t *testing.T) {
	forEachTransport(t, func(t *testing.T, a *api, tr transport) {
		_, _, refresh := a.signedIn(tr)

		expectStatus(t, a.logout(tr, refresh), http.StatusNoContent)
		expectStatus(t, a.refresh(tr, refresh), http.StatusUnauthorized)
	})
}

// Any token from the family names it. The live one dies with the one that was presented.
func TestLogoutWithASpentTokenStillEndsTheFamily(t *testing.T) {
	forEachTransport(t, func(t *testing.T, a *api, tr transport) {
		_, _, first := a.signedIn(tr)
		second := tr.tokenIn(t, a.refresh(tr, first))

		a.logout(tr, first)

		expectStatus(t, a.refresh(tr, second), http.StatusUnauthorized)
	})
}

func TestLogoutEndsThisSessionAndNoOther(t *testing.T) {
	forEachTransport(t, func(t *testing.T, a *api, tr transport) {
		payload, _, here := a.signedIn(tr)
		elsewhere := a.anotherDevice(tr, payload)

		a.logout(tr, here)

		expectStatus(t, a.refresh(tr, elsewhere), http.StatusOK)
	})
}

// Clients retry logout on a flaky network. An error from it is a support ticket about being unable
// to sign out, and it protects nothing.
func TestLogoutNeverFails(t *testing.T) {
	forEachTransport(t, func(t *testing.T, a *api, tr transport) {
		_, _, refresh := a.signedIn(tr)
		a.logout(tr, refresh)

		for _, token := range []string{refresh, "not-a-token", ""} {
			if response := a.logout(tr, token); response.Code != http.StatusNoContent {
				t.Errorf("logout(%.12q) = %d, want 204", token, response.Code)
			}
		}
	})
}

// --- Changing the password -----------------------------------------------------------------

// Otherwise "change your password" does not end the session of whoever knew the old one.
func TestChangingThePasswordEndsEverySessionAndStartsANewOne(t *testing.T) {
	forEachTransport(t, func(t *testing.T, a *api, tr transport) {
		payload, access, here := a.signedIn(tr)
		elsewhere := a.anotherDevice(tr, payload)

		changed := a.call(http.MethodPost, "/api/v1/auth/change-password",
			map[string]any{"current_password": password, "new_password": newPassword}, bearer(access))

		expectStatus(t, changed, http.StatusOK)
		expectStatus(t, a.refresh(tr, here), http.StatusUnauthorized)
		expectStatus(t, a.refresh(tr, elsewhere), http.StatusUnauthorized)
		expectStatus(t, a.refresh(tr, tr.tokenIn(t, changed)), http.StatusOK)
		expectStatus(t, a.call(http.MethodGet, "/api/v1/auth/me", nil, bearer(accessToken(t, changed))), http.StatusOK)
	})
}

func TestARefusedPasswordChangeEndsNothing(t *testing.T) {
	forEachTransport(t, func(t *testing.T, a *api, tr transport) {
		_, access, refresh := a.signedIn(tr)

		refused := a.call(http.MethodPost, "/api/v1/auth/change-password",
			map[string]any{"current_password": "not-" + password, "new_password": newPassword}, bearer(access))

		expectStatus(t, refused, http.StatusForbidden)
		expectStatus(t, a.refresh(tr, refresh), http.StatusOK)
	})
}

// --- The body transport's own contract -------------------------------------------------------

// A client sending `{"refreshToken": ...}` hears about the typo instead of getting a 401 for a token
// it believes it sent.
func TestARefreshBodyWithAnUnknownOrMissingFieldIs422(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))

	for _, body := range []any{
		map[string]any{"refreshToken": "x"},
		map[string]any{"refresh_token": "x", "extra": true},
		map[string]any{},
		nil,
	} {
		for _, path := range []string{"/api/v1/auth/refresh", "/api/v1/auth/logout"} {
			response := a.call(http.MethodPost, path, body, nil)
			if response.Code != http.StatusUnprocessableEntity || errorCode(t, response) != "validation_error" {
				t.Errorf("%s %v = %d %s, want 422 validation_error", path, body, response.Code, response.Body.String())
			}
		}
	}
}
