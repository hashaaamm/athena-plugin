// Package infratest runs Pulumi code against mocks and records what it registered. No cloud
// credentials, no network, no state.
//
// Being able to assert on a stack without applying it is most of the argument for Pulumi over
// HCL. Every resource the program registers is recorded with its resolved inputs, so a test reads
// what GCP would have been asked for — the attribute condition, the probe path, the environment —
// rather than poking at outputs.
package infratest

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Project, Stack and ProjectNumber are what every test runs as.
const (
	Project       = "test-project"
	Stack         = "dev"
	ProjectNumber = "123456789"
)

// Resource is one registration the program made.
type Resource struct {
	Type     string
	Name     string
	Custom   bool
	Provider string
	// Inputs are resolved to plain Go values: string, float64, bool, []any, map[string]any.
	Inputs map[string]any
	// Secret lists the top-level inputs that arrived marked secret.
	Secret []string
	// Dependencies are the URNs named by DependsOn (and by inputs that carry another resource's
	// outputs).
	Dependencies []string
}

// Recorder is a pulumi.MockResourceMonitor that remembers every resource.
type Recorder struct {
	mu        sync.Mutex
	resources []Resource
}

// NewResource answers a registration with the inputs echoed back, plus the outputs GCP would
// compute that the program reads.
func (r *Recorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	outputs := args.Inputs.Copy()
	str := func(key string) string {
		if v, ok := args.Inputs[resource.PropertyKey(key)]; ok && v.IsString() {
			return v.StringValue()
		}
		return ""
	}
	set := func(key, value string) {
		outputs[resource.PropertyKey(key)] = resource.NewStringProperty(value)
	}
	switch args.TypeToken {
	case "gcp:sql/databaseInstance:DatabaseInstance":
		set("connectionName", Project+":europe-west1:"+str("name"))
	case "gcp:secretmanager/secretVersion:SecretVersion":
		set("version", "1")
	case "gcp:serviceaccount/account:Account":
		set("email", str("accountId")+"@"+Project+".iam.gserviceaccount.com")
		set("name", "projects/"+Project+"/serviceAccounts/"+str("accountId"))
	case "gcp:cloudrunv2/service:Service":
		set("uri", "https://"+str("name")+"-abc123-ew.a.run.app")
	case "gcp:iam/workloadIdentityPoolProvider:WorkloadIdentityPoolProvider":
		set("name", fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
			ProjectNumber, str("workloadIdentityPoolId"), str("workloadIdentityPoolProviderId")))
	case "random:index/randomPassword:RandomPassword":
		set("result", strings.Repeat("x", 48))
	}

	var deps []string
	if args.RegisterRPC != nil {
		deps = args.RegisterRPC.GetDependencies()
	}
	var secret []string
	for key, value := range args.Inputs {
		if value.ContainsSecrets() {
			secret = append(secret, string(key))
		}
	}
	r.mu.Lock()
	r.resources = append(r.resources, Resource{
		Type:         args.TypeToken,
		Name:         args.Name,
		Custom:       args.Custom,
		Provider:     args.Provider,
		Inputs:       plain(args.Inputs),
		Secret:       secret,
		Dependencies: deps,
	})
	r.mu.Unlock()
	return args.Name + "-id", outputs, nil
}

// Call answers the one invoke the program makes.
func (r *Recorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	if args.Token == "gcp:organizations/getProject:getProject" {
		return resource.NewPropertyMapFromMap(map[string]any{
			"number":    ProjectNumber,
			"projectId": Project,
		}), nil
	}
	return resource.PropertyMap{}, nil
}

// Resources is everything registered so far.
func (r *Recorder) Resources() []Resource {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Resource(nil), r.resources...)
}

// OfType is every registration of one type token.
func (r *Recorder) OfType(typ string) []Resource {
	var out []Resource
	for _, res := range r.Resources() {
		if res.Type == typ {
			out = append(out, res)
		}
	}
	return out
}

// Find is the one registration with this type and logical name. It fails the test when there is
// not exactly one.
func (r *Recorder) Find(t testing.TB, typ, name string) Resource {
	t.Helper()
	var found []Resource
	for _, res := range r.OfType(typ) {
		if res.Name == name {
			found = append(found, res)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one %s named %q, found %d", typ, name, len(found))
	}
	return found[0]
}

// Has reports whether anything of this type and name was registered.
func (r *Recorder) Has(typ, name string) bool {
	for _, res := range r.OfType(typ) {
		if res.Name == name {
			return true
		}
	}
	return false
}

// Run executes body against a fresh Recorder, as stack "dev" of the given Pulumi project, with
// the given stack configuration (keys without the project prefix). It fails the test if the
// program returns an error.
func Run(t testing.TB, project string, config map[string]string, body pulumi.RunFunc) *Recorder {
	t.Helper()
	rec, err := TryRun(project, config, body)
	if err != nil {
		t.Fatalf("program failed: %v", err)
	}
	return rec
}

// TryRun is Run for a test that expects the program to fail.
func TryRun(project string, config map[string]string, body pulumi.RunFunc) (*Recorder, error) {
	rec := &Recorder{}
	qualified := map[string]string{}
	for key, value := range config {
		qualified[project+":"+key] = value
	}
	err := pulumi.RunErr(body,
		pulumi.WithMocks(project, Stack, rec),
		func(info *pulumi.RunInfo) { info.Config = qualified },
	)
	return rec, err
}

// Capture resolves an output inside a running program into dst. The program does not return until
// every apply has run, so dst is set by the time Run returns.
func Capture[T any](output pulumi.Output, dst *T) {
	output.ApplyT(func(v any) (any, error) {
		value, ok := v.(T)
		if !ok {
			return nil, fmt.Errorf("captured %T, want %T", v, *new(T))
		}
		*dst = value
		return v, nil
	})
}

// Get walks a path of map keys and slice indexes through plain inputs. A missing step is nil.
func Get(value any, path ...any) any {
	for _, step := range path {
		switch key := step.(type) {
		case string:
			m, ok := value.(map[string]any)
			if !ok {
				return nil
			}
			value = m[key]
		case int:
			s, ok := value.([]any)
			if !ok || key >= len(s) {
				return nil
			}
			value = s[key]
		default:
			return nil
		}
	}
	return value
}

// EnvOf turns a Cloud Run container's `envs` list into name -> value, or name -> "secret:<id>@<v>"
// for a secret mount.
func EnvOf(container any) map[string]string {
	out := map[string]string{}
	envs, _ := Get(container, "envs").([]any)
	for _, env := range envs {
		name, _ := Get(env, "name").(string)
		if ref := Get(env, "valueSource", "secretKeyRef"); ref != nil {
			out[name] = fmt.Sprintf("secret:%v@%v", Get(ref, "secret"), Get(ref, "version"))
			continue
		}
		out[name], _ = Get(env, "value").(string)
	}
	return out
}

// plain converts a property map into ordinary Go values, unwrapping secrets.
func plain(props resource.PropertyMap) map[string]any {
	out := map[string]any{}
	for key, value := range props {
		out[string(key)] = plainValue(value)
	}
	return out
}

func plainValue(v resource.PropertyValue) any {
	switch {
	case v.IsSecret():
		return plainValue(v.SecretValue().Element)
	case v.IsOutput():
		return plainValue(v.OutputValue().Element)
	case v.IsObject():
		return plain(v.ObjectValue())
	case v.IsArray():
		items := []any{}
		for _, item := range v.ArrayValue() {
			items = append(items, plainValue(item))
		}
		return items
	case v.IsString():
		return v.StringValue()
	case v.IsNumber():
		return v.NumberValue()
	case v.IsBool():
		return v.BoolValue()
	case v.IsResourceReference():
		return v.ResourceReferenceValue().URN
	default:
		return nil
	}
}
