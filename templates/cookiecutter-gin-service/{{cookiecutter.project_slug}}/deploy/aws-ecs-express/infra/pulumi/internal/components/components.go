// Package components holds the stack's building blocks, one per concern.
//
// Each component is a Pulumi ComponentResource taking a stackconfig.StackConfig by value and the
// handful of outputs it genuinely depends on, so main.go reads as a dependency graph rather than a
// script. Nothing here calls config.New itself — that is what makes a component constructible in a
// test.
package components

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// BootstrapImage is the placeholder every container starts on, until the first CD run.
//
// ECR is empty when the stack is first applied — the pipeline that fills it needs the repository,
// the roles and the service this stack creates — and ECS will not create a service with no image.
// So the first `up` points every container at Google's public `hello` image: anonymous pull, a few
// megabytes, listens on $PORT (8080 when unset) and answers 200 on every path, /health/live
// included. It is used on AWS for exactly that and nothing else: CD replaces it on the first merge
// and the stack never asks for it again — see ServiceIgnoreChanges.
const BootstrapImage = "us-docker.pkg.dev/cloudrun/container/hello"

// child is the options every resource inside a component is created with: the component's own
// options, then the component as parent.
//
// The component's options are passed down rather than left on the component alone, because they
// carry the explicit AWS provider and any DependsOn the composition root added, and this program
// does not rely on a component's options reaching its children by inheritance — every child is
// given them directly. A resource from another package ignores a provider that is not its own, so
// the random provider is unaffected.
func child(parent pulumi.Resource, opts []pulumi.ResourceOption, extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
	out := make([]pulumi.ResourceOption, 0, len(opts)+1+len(extra))
	out = append(out, opts...)
	out = append(out, pulumi.Parent(parent))
	return append(out, extra...)
}

// sortedKeys returns a map's keys in order, so everything built from a map is built the same way
// on every run.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// --- IAM policy documents ---------------------------------------------------------------------
//
// Written as Go values and marshalled, rather than as iam.GetPolicyDocument invokes or JSON in
// string literals. A struct cannot be malformed JSON, a test can decode exactly what IAM would be
// sent, and a document whose ARNs are still outputs is one ApplyT away instead of a second
// round-trip to the provider during every preview.

// Statement is one IAM policy statement.
type Statement struct {
	Sid       string                    `json:"Sid,omitempty"`
	Effect    string                    `json:"Effect"`
	Principal map[string]string         `json:"Principal,omitempty"`
	Action    []string                  `json:"Action"`
	Resource  []string                  `json:"Resource,omitempty"`
	Condition map[string]map[string]any `json:"Condition,omitempty"`
}

// Policy is an IAM policy document.
type Policy struct {
	Version   string      `json:"Version"`
	Statement []Statement `json:"Statement"`
}

// Allow is the usual shape: these actions on these resources.
func Allow(sid string, actions []string, resources ...string) Statement {
	return Statement{Sid: sid, Effect: "Allow", Action: actions, Resource: resources}
}

// Document is a policy holding statements, as the JSON IAM takes.
func Document(statements ...Statement) string {
	data, err := json.Marshal(Policy{Version: "2012-10-17", Statement: statements})
	if err != nil {
		// Every field is a string, a slice of strings or a map of them; Marshal cannot fail on
		// that, and a policy that silently became "" would be worse than a crash.
		panic(err)
	}
	return string(data)
}

// DocumentOf builds a policy whose statements need resolved outputs — role ARNs, secret ARNs, a
// looked-up provider. build receives the resolved strings in the order they were passed.
func DocumentOf(build func(values []string) []Statement, inputs ...pulumi.StringInput) pulumi.StringOutput {
	all := make([]any, len(inputs))
	for i, input := range inputs {
		all[i] = input
	}
	return pulumi.All(all...).ApplyT(func(resolved []any) (string, error) {
		values := make([]string, len(resolved))
		for i, value := range resolved {
			values[i], _ = value.(string)
		}
		return Document(build(values)...), nil
	}).(pulumi.StringOutput)
}

// serviceTrust is the trust policy for a role an AWS service assumes. ecs-tasks roles are
// further scoped to this account with aws:SourceAccount, so a task in some other account that
// somehow named this role still could not assume it — the confused-deputy guard AWS documents for
// task roles.
func serviceTrust(service, accountID string) string {
	statement := Statement{
		Effect:    "Allow",
		Principal: map[string]string{"Service": service},
		Action:    []string{"sts:AssumeRole"},
	}
	if service == "ecs-tasks.amazonaws.com" {
		statement.Condition = map[string]map[string]any{
			"StringEquals": {"aws:SourceAccount": accountID},
		}
	}
	return Document(statement)
}
