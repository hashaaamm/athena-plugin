package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The conversion's fixes, on a minimal Swagger 2.0 document shaped like swag's output.
const swagger = `{
  "swagger": "2.0",
  "info": {"title": "Test", "version": "0.1.0"},
  "basePath": "/",
  "paths": {
    "/refresh": {
      "post": {
        "tags": ["auth", "transport-cookie"],
        "x-cookie-parameter": {"name": "svc.refresh_token", "description": "The refresh token."},
        "responses": {"200": {"description": "Successful Response", "schema": {"$ref": "#/definitions/RefreshRequest"}}}
      }
    },
    "/me": {
      "get": {
        "security": [{"BearerAuth": []}],
        "parameters": [{"in": "body", "name": "body", "required": true, "schema": {"$ref": "#/definitions/RefreshRequest"}}],
        "responses": {"200": {"description": "Successful Response"}}
      }
    }
  },
  "definitions": {
    "RefreshRequest": {"type": "object", "properties": {"refresh_token": {"type": "string", "description": "Spent <here>."}}}
  },
  "securityDefinitions": {"BearerAuth": {"type": "apiKey", "in": "header", "name": "Authorization"}}
}`

func convert(t *testing.T) map[string]any {
	t.Helper()
	dir := t.TempDir()
	in, out := filepath.Join(dir, "swagger.json"), filepath.Join(dir, "openapi.json")
	if err := os.WriteFile(in, []byte(swagger), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(in, out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `\u003c`) {
		t.Fatal("the document is HTML-escaped")
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func dig(value any, keys ...string) any {
	for _, key := range keys {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[key]
	}
	return value
}

func TestTheDocumentIsOpenAPI3WithNoServers(t *testing.T) {
	document := convert(t)

	if version, _ := document["openapi"].(string); !strings.HasPrefix(version, "3.") {
		t.Fatalf("openapi %v", document["openapi"])
	}
	if _, ok := document["servers"]; ok {
		t.Fatal("servers survived the conversion")
	}
}

// Swagger 2.0's apiKey makes Swagger UI send the raw token without "Bearer ".
func TestBearerAuthIsHTTPBearer(t *testing.T) {
	scheme := dig(convert(t), "components", "securitySchemes", "BearerAuth")

	if dig(scheme, "type") != "http" || dig(scheme, "scheme") != "bearer" {
		t.Fatalf("BearerAuth = %v", scheme)
	}
}

func TestTheCookieExtensionBecomesACookieParameterAndMarkerTagsGo(t *testing.T) {
	operation := dig(convert(t), "paths", "/refresh", "post")

	parameters, _ := dig(operation, "parameters").([]any)
	if len(parameters) != 1 || dig(parameters[0], "in") != "cookie" || dig(parameters[0], "name") != "svc.refresh_token" {
		t.Fatalf("parameters %v", parameters)
	}
	if dig(operation, "x-cookie-parameter") != nil {
		t.Fatal("the extension survived")
	}
	if tags, _ := dig(operation, "tags").([]any); len(tags) != 1 || tags[0] != "auth" {
		t.Fatalf("tags %v; want the marker removed", tags)
	}
}

func TestStrictBodiesAreClosedAndTheBodyNameIsDropped(t *testing.T) {
	document := convert(t)

	if dig(document, "components", "schemas", "RefreshRequest", "additionalProperties") != false {
		t.Fatal("RefreshRequest is not closed")
	}
	if dig(document, "paths", "/me", "get", "requestBody", "x-originalParamName") != nil {
		t.Fatal("x-originalParamName survived")
	}
}
