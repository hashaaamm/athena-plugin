package app_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
{%- if cookiecutter.use_postgres == "yes" %}
	"time"

	"github.com/google/uuid"
{%- endif %}

	"{{ cookiecutter.go_module }}/internal/app"
	"{{ cookiecutter.go_module }}/internal/config"
{%- if cookiecutter.use_postgres == "yes" %}
	"{{ cookiecutter.go_module }}/internal/repository"
	"{{ cookiecutter.go_module }}/internal/security"
	"{{ cookiecutter.go_module }}/internal/testdb"
{%- endif %}
)

// The HTTP contract, through the whole stack: router -> middleware -> handler -> service ->
// repository -> Postgres. Nothing is mocked, and RequireActor is never bypassed — a suite that skips
// the authentication middleware has stopped testing the decoder, and the decoder is the part with
// the vulnerability classes in it. The router is built by app.NewRouter, the same function the
// server uses, so a route mounted on the wrong group fails here.

const origin = "http://localhost:3000"
{%- if cookiecutter.use_postgres == "yes" %}

const (
	password    = "correct horse battery staple"
	newPassword = "a different passphrase entirely"
)

// Cheap Argon2 parameters: these tests check the HTTP contract, not the cost of a hash, and the
// production parameters under the race detector would make the package take minutes. The hashing
// module's own tests use the production ones.
var cheapParams = security.Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
{%- endif %}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// testConfig is the test environment's configuration plus overrides. The ambient environment is
// ignored, so a test means the same thing in every shell.
func testConfig(t *testing.T, overrides map[string]string) config.Config {
	t.Helper()
	vars := map[string]string{"ENVIRONMENT": "test", "GIT_SHA": "abc1234"}
	for key, value := range overrides {
		vars[key] = value
	}
	cfg, err := config.FromMap(vars)
	if err != nil {
		t.Fatalf("configuration: %v", err)
	}
	return cfg
}

// api is an HTTP client over a router built for one test.
type api struct {
	t      *testing.T
	router http.Handler
}
{%- if cookiecutter.use_postgres == "yes" %}

// newAPI builds the router on this test's transaction, which is rolled back when the test ends.
func newAPI(t *testing.T, cfg config.Config) *api {
	t.Helper()
	return newAPIOn(t, cfg, testdb.Tx(t))
}

// newAPIOn builds the router on a given database handle, for the test that needs to break one.
func newAPIOn(t *testing.T, cfg config.Config, db repository.DB) *api {
	t.Helper()
	router, err := app.NewRouter(cfg, app.Deps{Logger: discard(), DB: db, Hasher: security.NewHasher(cheapParams, 0)})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	return &api{t: t, router: router}
}

// tokensFor mints and reads tokens exactly as the router built from cfg does.
func tokensFor(cfg config.Config) *security.Tokens {
	return security.NewTokens(security.TokenConfig{
		Secret:     cfg.JWTSecret.Reveal(),
		Issuer:     cfg.JWTIssuer,
		Audience:   cfg.JWTAudience,
		AccessTTL:  time.Duration(cfg.AccessTokenTTLSeconds) * time.Second,
		RefreshTTL: time.Duration(cfg.RefreshTokenTTLDays) * 24 * time.Hour,
	})
}
{%- else %}

func newAPI(t *testing.T, cfg config.Config) *api {
	t.Helper()
	router, err := app.NewRouter(cfg, app.Deps{Logger: discard()})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	return &api{t: t, router: router}
}
{%- endif %}

// call sends one request. body is JSON-encoded unless it is nil or already a string.
func (a *api) call(method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	a.t.Helper()
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(value)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			a.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	if reader != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	a.router.ServeHTTP(recorder, request)
	return recorder
}

// object decodes a JSON object body.
func object(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", recorder.Body.String(), err)
	}
	return decoded
}

// errorCode is the `error.code` of the API's error envelope.
func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	envelope, ok := object(t, recorder)["error"].(map[string]any)
	if !ok {
		t.Fatalf("body %q is not the error envelope", recorder.Body.String())
	}
	code, _ := envelope["code"].(string)
	return code
}

func expectStatus(t *testing.T, recorder *httptest.ResponseRecorder, want int) {
	t.Helper()
	if recorder.Code != want {
		t.Fatalf("status %d, want %d; body %s", recorder.Code, want, recorder.Body.String())
	}
}
{%- if cookiecutter.use_postgres == "yes" %}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func registerPayload() map[string]any {
	return map[string]any{"email": "ada-" + uuid.NewString()[:8] + "@example.com", "password": password}
}

// registered registers an account and returns its credentials.
func (a *api) registered() map[string]any {
	a.t.Helper()
	payload := registerPayload()
	expectStatus(a.t, a.call(http.MethodPost, "/api/v1/auth/register", payload, nil), http.StatusCreated)
	return payload
}

// loggedIn registers, logs in, and returns the credentials and the login response.
func (a *api) loggedIn() (map[string]any, *httptest.ResponseRecorder) {
	a.t.Helper()
	payload := a.registered()
	login := a.call(http.MethodPost, "/api/v1/auth/login", payload, nil)
	expectStatus(a.t, login, http.StatusOK)
	return payload, login
}

func accessToken(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	token, ok := object(t, recorder)["access_token"].(string)
	if !ok || token == "" {
		t.Fatalf("no access_token in %s", recorder.Body.String())
	}
	return token
}
{%- endif %}
