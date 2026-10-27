package app_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// /openapi.json is the document every client is generated from — the React app builds its types from
// it — so it must be OpenAPI 3, carry the schema names clients import, and describe the API this
// configuration actually serves. And none of it is served in production.

type openAPIDocument struct {
	OpenAPI    string                    `json:"openapi"`
	Paths      map[string]map[string]any `json:"paths"`
	Components struct {
		Schemas map[string]any `json:"schemas"`
	} `json:"components"`
}

func fetchDocument(t *testing.T, a *api) openAPIDocument {
	t.Helper()
	response := a.call(http.MethodGet, "/openapi.json", nil, nil)
	expectStatus(t, response, http.StatusOK)
	var document openAPIDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func TestTheServedDocumentIsOpenAPI3WithTheContractsSchemaNames(t *testing.T) {
	document := fetchDocument(t, newAPI(t, testConfig(t, nil)))

	if !strings.HasPrefix(document.OpenAPI, "3.") {
		t.Fatalf("openapi %q, want 3.x", document.OpenAPI)
	}
	for _, name := range []string{
		"ReadinessStatus",
{%- if cookiecutter.use_postgres == "yes" %}
		"ErrorResponse", "ErrorDetail", "UserRead", "TokenPairRead", "RegisterRequest", "LoginRequest", "ChangePasswordRequest", "RefreshRequest", "LogoutRequest",
{%- endif %}
	} {
		if _, ok := document.Components.Schemas[name]; !ok {
			t.Errorf("schema %s is missing", name)
		}
	}
	for _, path := range []string{"/health/live", "/health/ready"} {
		if _, ok := document.Paths[path]; !ok {
			t.Errorf("path %s is missing", path)
		}
	}
}
{%- if cookiecutter.use_postgres == "yes" %}

// Under the cookie transport login returns AccessTokenRead and refresh takes no body. Handing the
// body transport's document to a cookie deployment would give the web client's type generator an API
// the server does not have.
func TestTheCookieTransportServesItsOwnDocument(t *testing.T) {
	document := fetchDocument(t, newAPI(t, testConfig(t, cookieVars(nil))))

	if _, ok := document.Components.Schemas["AccessTokenRead"]; !ok {
		t.Fatal("AccessTokenRead is missing from the cookie document")
	}
	for _, absent := range []string{"TokenPairRead", "RefreshRequest", "LogoutRequest"} {
		if _, ok := document.Components.Schemas[absent]; ok {
			t.Errorf("the cookie document describes %s, which the cookie transport never sends or takes", absent)
		}
	}
	refresh, _ := document.Paths["/api/v1/auth/refresh"]["post"].(map[string]any)
	if _, hasBody := refresh["requestBody"]; hasBody {
		t.Fatal("the cookie document gives refresh a request body")
	}
}
{%- endif %}

func TestSwaggerUIIsServedAndReadsTheOpenAPI3Document(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))

	redirect := a.call(http.MethodGet, "/docs", nil, nil)
	expectStatus(t, redirect, http.StatusFound)
	if redirect.Header().Get("Location") != "/docs/index.html" {
		t.Fatalf("Location %q", redirect.Header().Get("Location"))
	}
	expectStatus(t, a.call(http.MethodGet, "/docs/index.html", nil, nil), http.StatusOK)
	initializer := a.call(http.MethodGet, "/docs/swagger-initializer.js", nil, nil)
	if !strings.Contains(initializer.Body.String(), "/openapi.json") {
		t.Fatal("Swagger UI is not pointed at /openapi.json")
	}
}

// Interactive docs are free reconnaissance in production.
func TestDocsAreNotServedInProduction(t *testing.T) {
	vars := map[string]string{"ENVIRONMENT": "production"}
{%- if cookiecutter.use_postgres == "yes" %}
	vars["POSTGRES_PASSWORD"] = "a-real-one"
	vars["JWT_SECRET"] = strings.Repeat("k", 48)
{%- endif %}
	cfg := testConfig(t, vars)
	a := newAPI(t, cfg)

	for _, path := range []string{"/openapi.json", "/docs", "/docs/index.html"} {
		if response := a.call(http.MethodGet, path, nil, nil); response.Code != http.StatusNotFound {
			t.Errorf("GET %s in production = %d, want 404", path, response.Code)
		}
	}
}
