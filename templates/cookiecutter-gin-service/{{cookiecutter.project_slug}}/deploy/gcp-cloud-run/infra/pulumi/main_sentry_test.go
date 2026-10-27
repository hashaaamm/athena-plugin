// Removed by the post-generation hook when use_sentry is "no".
package main

import (
	"testing"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

func TestSentryIsNotMountedUntilItsVersionIsConfigured(t *testing.T) {
	// Mounting a container with no versions fails the deploy outright. The default stack must
	// stay applyable, so the mount appears only once sentryDsnVersion names a real version.
	if _, ok := deployed(t, nil).backendEnv(t)["SENTRY_DSN"]; ok {
		t.Error("SENTRY_DSN is mounted from an empty container")
	}
	populated := deployed(t, func(c *stackconfig.StackConfig) { c.SentryDSNVersion = "2" })
	if got := populated.backendEnv(t)["SENTRY_DSN"]; got != "secret:{{ cookiecutter.project_slug }}-sentry-dsn-dev@2" {
		t.Errorf("SENTRY_DSN = %q", got)
	}
}
