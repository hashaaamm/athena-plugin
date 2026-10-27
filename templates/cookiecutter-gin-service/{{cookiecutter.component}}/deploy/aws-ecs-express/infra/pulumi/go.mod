// The stack's own Go module, separate from backend/. Pulumi builds the program from this directory,
// and nothing in backend/ imports it or is imported by it: the service does not know where it
// runs, and the stack does not know how the service is built.
//
// go.sum is not templated, for the same reason backend/go.sum is not: a checksum file pinned when
// the template was written is stale on the day it ships. Run `just lock` in infra/ once after
// generating (it is `go mod tidy`), and commit the result. CI fails on a go.sum that is not tidy.
module {{ cookiecutter.go_module }}/infra/pulumi

go {{ cookiecutter.go_version }}

require (
	github.com/pulumi/pulumi-aws/sdk/v7 v7.51.1
	github.com/pulumi/pulumi/sdk/v3 v3.271.0
)
