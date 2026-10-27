package app_test

import (
	"net/http"
	"testing"
)

// Everything the router answers is in the API's error shape, including the requests no handler
// claims: a client has one thing to parse, not one per framework default.

func TestAnUnknownPathIsA404InTheErrorShape(t *testing.T) {
	response := newAPI(t, testConfig(t, nil)).call(http.MethodGet, "/api/v1/nothing-here", nil, nil)

	expectStatus(t, response, http.StatusNotFound)
	if code := errorCode(t, response); code != "not_found" {
		t.Fatalf("code %q, want not_found", code)
	}
}

func TestAKnownPathWithTheWrongMethodIsA405InTheErrorShape(t *testing.T) {
	response := newAPI(t, testConfig(t, nil)).call(http.MethodDelete, "/health/live", nil, nil)

	expectStatus(t, response, http.StatusMethodNotAllowed)
	if code := errorCode(t, response); code != "method_not_allowed" {
		t.Fatalf("code %q, want method_not_allowed", code)
	}
}
