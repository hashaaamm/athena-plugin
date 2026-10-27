// Removed by the post-generation hook when use_sentry is "no".
package main

import (
	"strings"
	"testing"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

func TestSentryIsNotMountedUntilItsVersionIsConfigured(t *testing.T) {
	// A secret with no value is a task that cannot start. The default stack must stay applyable,
	// so the mount appears only once sentryDsnVersion names a real VersionId.
	if _, ok := deployed(t, nil).backendEnv(t)["SENTRY_DSN"]; ok {
		t.Error("SENTRY_DSN is mounted from an empty secret")
	}
	populated := deployed(t, func(c *stackconfig.StackConfig) { c.SentryDSNVersion = "v2" })
	got := populated.backendEnv(t)["SENTRY_DSN"]
	if !strings.Contains(got, ":secret:{{ cookiecutter.project_slug }}-sentry-dsn-dev-") || !strings.HasSuffix(got, ":::v2") {
		t.Errorf("SENTRY_DSN = %q", got)
	}
}
