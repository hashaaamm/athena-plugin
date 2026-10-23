package app_test

import (
	"strings"
	"testing"

	"{{ cookiecutter.go_module }}/internal/app"
	"{{ cookiecutter.go_module }}/internal/config"
	"{{ cookiecutter.go_module }}/internal/testdb"
)

// The check is in app.NewRouter, so it runs for every process that can issue a token and for no
// process that cannot. A test that only called config.RequireSigningKey would pass while somebody
// deleted the call.
func TestBuildingTheRouterIsWhatEnforcesTheSigningKey(t *testing.T) {
	production := map[string]string{"ENVIRONMENT": "production", "POSTGRES_PASSWORD": "a-real-one"}

	for _, key := range []string{config.PlaceholderJWTSecret, strings.Repeat("s", config.MinJWTSecretLength-1)} {
		production["JWT_SECRET"] = key
		_, err := app.NewRouter(testConfig(t, production), app.Deps{Logger: discard(), DB: testdb.Tx(t)})
		if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
			t.Fatalf("NewRouter with a %d-character key = %v; want a refusal naming JWT_SECRET", len(key), err)
		}
	}

	production["JWT_SECRET"] = strings.Repeat("s", config.MinJWTSecretLength)
	if _, err := app.NewRouter(testConfig(t, production), app.Deps{Logger: discard(), DB: testdb.Tx(t)}); err != nil {
		t.Fatalf("NewRouter with a real key: %v", err)
	}
}
