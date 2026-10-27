package app_test

import (
	"net/http"
	"strings"
	"testing"
)

// CORS: the difference between a working browser client and a blank page plus a console error.
//
// Worth its own file because the failure is invisible from the server's side — every assertion you
// would naturally write against the API still passes. curl gets a 200; the browser gets the same 200
// and refuses to hand the body to the page.

func corsAPI(t *testing.T, origins string) *api {
	t.Helper()
	return newAPI(t, testConfig(t, map[string]string{"CORS_ORIGINS": origins}))
}

func TestAConfiguredOriginIsEchoedBackWithCredentials(t *testing.T) {
	response := corsAPI(t, origin).call(http.MethodGet, "/health/live", nil, map[string]string{"Origin": origin})

	if response.Header().Get("Access-Control-Allow-Origin") != origin || response.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("headers %v", response.Header())
	}
}

// The OPTIONS request the browser sends before any non-trivial call. Unanswered, every POST from the
// SPA fails before it is sent — and the headers the web client sends must be allowed by name.
func TestAPreflightIsAnsweredAndAllowsTheHeadersTheClientSends(t *testing.T) {
	response := corsAPI(t, origin).call(http.MethodOptions, "/api/v1/auth/refresh", nil, map[string]string{
		"Origin":                         origin,
		"Access-Control-Request-Method":  http.MethodPost,
		"Access-Control-Request-Headers": "authorization,content-type,x-requested-with",
	})

	expectStatus(t, response, http.StatusOK)
	allowed := strings.ToLower(response.Header().Get("Access-Control-Allow-Headers"))
	for _, header := range []string{"authorization", "content-type", "x-requested-with"} {
		if !strings.Contains(allowed, header) {
			t.Errorf("preflight does not allow %s: %q", header, allowed)
		}
	}
	if response.Header().Get("Access-Control-Allow-Origin") != origin {
		t.Fatalf("headers %v", response.Header())
	}
}

// An origin not on the list gets no CORS headers — and still its normal answer, so a
// server-to-server caller that happens to send Origin is not broken by a browser policy.
func TestAnOriginWeDidNotConfigureGetsNothing(t *testing.T) {
	response := corsAPI(t, origin).call(http.MethodGet, "/health/live", nil, map[string]string{"Origin": "https://not-us.example"})

	expectStatus(t, response, http.StatusOK)
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q for an unlisted origin", got)
	}
}

// The default. A service with no web client should not be reachable from one.
func TestNoConfigurationMeansNoBrowserAccessAtAll(t *testing.T) {
	response := corsAPI(t, "").call(http.MethodGet, "/health/live", nil, map[string]string{"Origin": origin})

	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q with CORS off", got)
	}
}

// An error response carries the CORS headers too. Without them the browser reports a CORS failure
// instead of the 404 (or the 401), and the network tab is the only place that tells the truth.
func TestErrorsCarryTheCORSHeaders(t *testing.T) {
	response := corsAPI(t, origin).call(http.MethodGet, "/api/v1/nothing-here", nil, map[string]string{"Origin": origin})

	expectStatus(t, response, http.StatusNotFound)
	if response.Header().Get("Access-Control-Allow-Origin") != origin {
		t.Fatalf("headers %v", response.Header())
	}
}
