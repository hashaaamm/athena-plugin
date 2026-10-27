package app_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"{{ cookiecutter.go_module }}/internal/app"
)

// Cloud Logging reads `severity` and `message`; slog writes `level` and `msg`. A line it cannot read
// is a DEFAULT-severity blob nobody can filter on.
func TestJSONLogsUseCloudLoggingsFieldNames(t *testing.T) {
	var out bytes.Buffer
	logger := app.NewLogger(testConfig(t, map[string]string{"JSON_LOGS": "true"}), &out)

	logger.Warn("app_error", "message", "an attribute that would collide", "code", "conflict")

	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatalf("not one JSON object: %s", out.String())
	}
	if line["severity"] != "WARNING" || line["message"] != "app_error" {
		t.Fatalf("line %v; want severity WARNING and message app_error", line)
	}
	if _, ok := line["level"]; ok {
		t.Fatal("the line still carries slog's level key")
	}
	// The colliding attribute is kept, under a name of its own, and the line has one `message`.
	if line["attr_message"] != "an attribute that would collide" || strings.Count(out.String(), `"message"`) != 1 {
		t.Fatalf("line %s", out.String())
	}
}

func TestTextLogsLocally(t *testing.T) {
	var out bytes.Buffer
	app.NewLogger(testConfig(t, nil), &out).Info("startup")

	if !strings.Contains(out.String(), "msg=startup") {
		t.Fatalf("line %q is not slog's text format", out.String())
	}
}

// LOG_LEVEL is honoured: a debug line is dropped at INFO.
func TestTheConfiguredLevelFilters(t *testing.T) {
	var out bytes.Buffer
	app.NewLogger(testConfig(t, map[string]string{"LOG_LEVEL": "INFO"}), &out).Debug("noise")

	if out.Len() != 0 {
		t.Fatalf("a debug line was written at INFO: %q", out.String())
	}
}
