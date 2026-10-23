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
		secrets, err = components.NewSecrets(ctx, cfg, components.SecretsArgs{
			AccessorEmail:  pulumi.String(runtimeEmail),
			ManualVersions: manual,
		})
		return err
	})
	return secrets
}

func TestTheSentryContainerIsCreatedEmptyAndMountedByNothing(t *testing.T) {
	// It holds a value that originates outside this stack, so the stack cannot create a version.
	// Nothing may mount it until a human has added one and set sentryDsnVersion.
	if !slices.Contains(components.ManualSecrets, "sentry-dsn") {
		t.Fatal("sentry-dsn must be a manual secret")
	}
	secrets := newSecrets(t, nil)
	if _, ok := secrets.IDs["sentry-dsn"]; !ok {
		t.Error("no sentry-dsn container")
	}
	if len(secrets.Versions) != 0 {
		t.Errorf("%d versions; the stack must not invent a Sentry DSN", len(secrets.Versions))
	}
	if _, ok := secrets.Refs["sentry-dsn"]; ok {
		t.Error("sentry-dsn has a reference before anybody populated it")
	}
	if cfg.SentryDSNVersion != "" {
		t.Errorf("sentryDsnVersion defaults to %q, want empty", cfg.SentryDSNVersion)
	}
}

func TestAPopulatedSentrySecretIsPinnedToItsConfiguredVersion(t *testing.T) {
	// Once a human has added the DSN and recorded its number, that number is what is mounted.
	secrets := newSecrets(t, map[string]string{"sentry-dsn": "3"})
	ref, ok := secrets.Refs["sentry-dsn"]
	if !ok {
		t.Fatal("no reference for a populated sentry-dsn")
	}
	if got, ok := ref.Version.(pulumi.String); !ok || got != "3" {
		t.Errorf("version = %v, want 3", ref.Version)
	}
}
