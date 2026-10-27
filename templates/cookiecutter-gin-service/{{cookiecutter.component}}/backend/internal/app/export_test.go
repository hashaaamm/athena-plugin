package app

// The connection seams, for the tests in app_test. Exported here rather than in the package, so the
// service itself has exactly one way to build a pool — openPool — and the tests can still hand each
// mode a fake credential source instead of a cloud.
var (
	WithPerConnectionPassword = withPerConnectionPassword
	RDSIAMPoolConfig          = rdsIAMPoolConfig
	CloudSQLPoolConfig        = cloudSQLPoolConfig
	BootstrapAppRole          = bootstrapAppRole
)
