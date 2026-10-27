package observability

import (
	"testing"

	"github.com/getsentry/sentry-go"

	"{{ cookiecutter.go_module }}/internal/config"
)

// Sentry retains payloads, so a credential that reaches an event is a rotation exercise.
func TestCredentialsAreScrubbedBeforeAnEventLeaves(t *testing.T) {
	event := &sentry.Event{Request: &sentry.Request{
		Headers: map[string]string{
			"Authorization": "Bearer eyJ-a-real-token",
			"Cookie":        "svc.refresh_token=eyJ-another",
			"X-Api-Key":     "k",
			"Content-Type":  "application/json",
		},
		Cookies: "svc.refresh_token=eyJ-another",
		Data:    `{"password": "hunter2"}`,
	}}

	scrubbed := scrub(event, nil)

	for _, key := range []string{"Authorization", "Cookie", "X-Api-Key"} {
		if scrubbed.Request.Headers[key] != "[Filtered]" {
			t.Errorf("header %s = %q, want it filtered", key, scrubbed.Request.Headers[key])
		}
	}
	if scrubbed.Request.Headers["Content-Type"] != "application/json" {
		t.Error("a harmless header was filtered")
	}
	if scrubbed.Request.Cookies != "[Filtered]" || scrubbed.Request.Data != "[Filtered]" {
		t.Fatalf("cookies %q, data %q", scrubbed.Request.Cookies, scrubbed.Request.Data)
	}
}

// Developer machines must not report into a real project: an empty DSN initialises nothing.
func TestAnEmptyDSNIsANoOp(t *testing.T) {
	flush, err := Init(config.Config{})
	if err != nil || flush == nil {
		t.Fatalf("Init with no DSN = %v", err)
	}
	flush()
	if client := sentry.CurrentHub().Client(); client != nil && client.Options().Dsn != "" {
		t.Fatalf("a client was configured with DSN %q", client.Options().Dsn)
	}
}
