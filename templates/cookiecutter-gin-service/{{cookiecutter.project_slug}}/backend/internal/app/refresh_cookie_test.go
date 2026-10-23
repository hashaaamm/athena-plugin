package app_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"{{ cookiecutter.go_module }}/internal/http/handler"
	"{{ cookiecutter.go_module }}/internal/http/middleware"
)

// REFRESH_TRANSPORT=cookie: the cookie's attributes, its absence from every body, and the CSRF
// check in front of the two routes that read it. Each attribute is asserted from the raw Set-Cookie
// header: a cookie jar that silently drops a cookie it dislikes would make a missing HttpOnly look
// like a test setup problem; the header says exactly what the browser was told.

func cookieVars(overrides map[string]string) map[string]string {
	vars := map[string]string{"REFRESH_TRANSPORT": "cookie", "CORS_ORIGINS": origin}
	for key, value := range overrides {
		vars[key] = value
	}
	return vars
}

func browser(t *testing.T) (*api, transport) {
	t.Helper()
	return newAPI(t, testConfig(t, cookieVars(nil))), transports[1]
}

func cookieHeader(token string) string { return handler.RefreshCookieName + "=" + token }

func TestTheRefreshTokenIsSetHttpOnlySecureStrictAndScoped(t *testing.T) {
	a, _ := browser(t)
	_, login := a.loggedIn()

	_, attributes := refreshCookie(t, login)

	for _, flag := range []string{"httponly", "secure"} {
		if _, ok := attributes[flag]; !ok {
			t.Errorf("the cookie is not %s: %v", flag, attributes)
		}
	}
	if attributes["samesite"] != "Strict" || attributes["path"] != "/api/v1/auth" || attributes["max-age"] != "2592000" {
		t.Fatalf("attributes %v", attributes)
	}
	// Host-only: no Domain attribute means no other subdomain ever receives it.
	if _, ok := attributes["domain"]; ok {
		t.Fatal("the cookie names a Domain")
	}
}

func TestTheCookieAttributesFollowConfiguration(t *testing.T) {
	a := newAPI(t, testConfig(t, cookieVars(map[string]string{"REFRESH_COOKIE_SAMESITE": "lax", "REFRESH_COOKIE_SECURE": "false"})))
	_, login := a.loggedIn()

	_, attributes := refreshCookie(t, login)

	if _, secure := attributes["secure"]; secure || attributes["samesite"] != "Lax" {
		t.Fatalf("attributes %v; want Lax and not Secure", attributes)
	}
}

// A body that carried the refresh token as well would hand it to any script that can call the
// endpoint — exactly the read the cookie exists to remove.
func TestNoBodyEverCarriesTheRefreshToken(t *testing.T) {
	a, tr := browser(t)
	_, login := a.loggedIn()
	refreshed := a.refresh(tr, tr.tokenIn(t, login))
	changed := a.call(http.MethodPost, "/api/v1/auth/change-password",
		map[string]any{"current_password": password, "new_password": newPassword}, bearer(accessToken(t, refreshed)))

	for _, response := range []*httptest.ResponseRecorder{login, refreshed, changed} {
		expectStatus(t, response, http.StatusOK)
		keys := make([]string, 0, 3)
		for key := range object(t, response) {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		if strings.Join(keys, ",") != "access_token,expires_in,token_type" {
			t.Fatalf("body keys %v, want exactly AccessTokenRead's", keys)
		}
		if token, _ := refreshCookie(t, response); strings.Contains(response.Body.String(), token) {
			t.Fatal("the body contains the refresh token")
		}
	}
}

// What a cross-site form post looks like: the cookie, and no custom header. Refused before the
// service runs, so the token is still good for the real client.
func TestARefreshWithoutTheCSRFHeaderIsRefusedAndSpendsNothing(t *testing.T) {
	a, tr := browser(t)
	_, login := a.loggedIn()
	token := tr.tokenIn(t, login)

	forged := a.call(http.MethodPost, "/api/v1/auth/refresh", nil, map[string]string{"Cookie": cookieHeader(token)})

	expectStatus(t, forged, http.StatusForbidden)
	if errorCode(t, forged) != "forbidden" {
		t.Fatalf("code %q, want forbidden", errorCode(t, forged))
	}
	expectStatus(t, a.refresh(tr, token), http.StatusOK)
}

func TestARefreshFromAnOriginNotOnTheAllowlistIsRefused(t *testing.T) {
	a, tr := browser(t)
	_, login := a.loggedIn()
	token := tr.tokenIn(t, login)
	headers := func(from string) map[string]string {
		return map[string]string{"Cookie": cookieHeader(token), middleware.CSRFHeader: "fetch", "Origin": from}
	}

	expectStatus(t, a.call(http.MethodPost, "/api/v1/auth/refresh", nil, headers("https://not-us.example")), http.StatusForbidden)
	expectStatus(t, a.call(http.MethodPost, "/api/v1/auth/refresh", nil, headers("")), http.StatusForbidden)
	expectStatus(t, a.call(http.MethodPost, "/api/v1/auth/refresh", nil, headers(origin)), http.StatusOK)
}

// Logging somebody out from a third-party page is a nuisance attack, and it is refused.
func TestALogoutWithoutTheCSRFHeaderEndsNothing(t *testing.T) {
	a, tr := browser(t)
	_, login := a.loggedIn()
	token := tr.tokenIn(t, login)

	forged := a.call(http.MethodPost, "/api/v1/auth/logout", nil, map[string]string{"Cookie": cookieHeader(token)})

	expectStatus(t, forged, http.StatusForbidden)
	expectStatus(t, a.refresh(tr, token), http.StatusOK)
}

func TestLogoutClearsTheCookie(t *testing.T) {
	a, tr := browser(t)
	_, login := a.loggedIn()

	response := a.logout(tr, tr.tokenIn(t, login))

	expectStatus(t, response, http.StatusNoContent)
	value, attributes := refreshCookie(t, response)
	// Same path as when it was set, or the browser keeps the original.
	if value != "" || attributes["max-age"] != "0" || attributes["path"] != "/api/v1/auth" {
		t.Fatalf("value %q, attributes %v; want an empty, expired cookie on the same path", value, attributes)
	}
}

func TestLogoutWithNoCookieAtAllIsStillA204(t *testing.T) {
	a, _ := browser(t)

	expectStatus(t, a.call(http.MethodPost, "/api/v1/auth/logout", nil, map[string]string{middleware.CSRFHeader: "fetch"}), http.StatusNoContent)
}

// The web client's first request after a reload, when nobody is signed in.
func TestARefreshWithNoCookieIsA401(t *testing.T) {
	a, _ := browser(t)

	response := a.call(http.MethodPost, "/api/v1/auth/refresh", nil, map[string]string{middleware.CSRFHeader: "fetch"})

	expectStatus(t, response, http.StatusUnauthorized)
	if response.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatal("401 without WWW-Authenticate")
	}
}
