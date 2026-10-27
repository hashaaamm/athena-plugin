// Removed by the post-generation hook when use_sentry is "no".
package components_test

import (
	"slices"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/components"
)

func newSecrets(t *testing.T, manual map[string]string) *components.Secrets {
	t.Helper()
	var secrets *components.Secrets
	run(t, func(ctx *pulumi.Context) error {
		var err error
		secrets, err = components.NewSecrets(ctx, cfg, components.SecretsArgs{ManualVersions: manual})
		return err
	})
	return secrets
}

func TestTheSentrySecretIsCreatedEmptyAndMountedByNothing(t *testing.T) {
	// It holds a value that originates outside this stack, so the stack cannot create a version.
	// Nothing may mount it until a human has added one and set sentryDsnVersion.
	if !slices.Contains(components.ManualSecrets, "sentry-dsn") {
		t.Fatal("sentry-dsn must be a manual secret")
	}
	secrets := newSecrets(t, nil)
	if _, ok := secrets.ARNs["sentry-dsn"]; !ok {
		t.Error("no sentry-dsn secret")
	}
	if len(secrets.Versions) != 0 {
		t.Errorf("%d versions; the stack must not invent a Sentry DSN", len(secrets.Versions))
	}
	if len(secrets.GeneratedARNs) != 0 {
		t.Error("the Sentry DSN is listed as generated, so the preview role could read it")
	}
	if _, ok := secrets.Refs["sentry-dsn"]; ok {
		t.Error("sentry-dsn has a reference before anybody populated it")
	}
	if cfg.SentryDSNVersion != "" {
		t.Errorf("sentryDsnVersion defaults to %q, want empty", cfg.SentryDSNVersion)
	}
}

func TestAPopulatedSentrySecretIsPinnedToItsConfiguredVersion(t *testing.T) {
	secrets := newSecrets(t, map[string]string{"sentry-dsn": "a1b2c3d4-0000-0000-0000-000000000000"})
	ref, ok := secrets.Refs["sentry-dsn"]
	if !ok {
		t.Fatal("no reference for a populated sentry-dsn")
	}
	if got, ok := ref.VersionID.(pulumi.String); !ok || got != "a1b2c3d4-0000-0000-0000-000000000000" {
		t.Errorf("version = %v", ref.VersionID)
	}
}
