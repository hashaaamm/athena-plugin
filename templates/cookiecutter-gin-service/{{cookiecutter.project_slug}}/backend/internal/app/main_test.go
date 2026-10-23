package app_test

import (
	"testing"

	"{{ cookiecutter.go_module }}/internal/testdb"
)

// This package's tests get a migrated schema of their own; see internal/testdb.
func TestMain(m *testing.M) { testdb.Run(m) }
