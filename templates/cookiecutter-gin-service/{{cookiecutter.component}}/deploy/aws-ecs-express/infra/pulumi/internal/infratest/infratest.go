// Package infratest runs Pulumi code against mocks and records what it registered. No cloud
// credentials, no network, no state.
//
// Being able to assert on a stack without applying it is most of the argument for Pulumi over
// HCL. Every resource the program registers is recorded with its resolved inputs, and every invoke
// with the provider it went through, so a test reads what AWS would have been asked for — the
// trust policy, the health-check path, the secret reference — rather than poking at outputs.
package infratest

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// AccountID, Region and Stack are what every test runs as.
const (
	AccountID = "123456789012"
	Region    = "eu-west-1"
	Stack     = "dev"

	// OIDCProviderARN is what the mocked iam.getOpenIdConnectProvider returns: the provider the
	// bootstrap script creates once per account.
	OIDCProviderARN = "arn:aws:iam::" + AccountID + ":oidc-provider/token.actions.githubusercontent.com"

	// StateKeyARN is what the mocked kms.getKey returns for the state key's alias.
	StateKeyARN = "arn:aws:kms:" + Region + ":" + AccountID + ":key/00000000-0000-0000-0000-000000000000"

	// DatabaseResourceID and MasterSecretARN are what a mocked RDS instance reports: its `db-…`
	// resource id, and the secret RDS made for the master login — named the way RDS names it.
	DatabaseResourceID = "db-ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	MasterSecretARN    = "arn:aws:secretsmanager:" + Region + ":" + AccountID + ":secret:rds!db-11111111-2222-3333-4444-555555555555-AbCdEf"
)

// AvailabilityZones is what the mocked aws.getAvailabilityZones returns — deliberately out of
// order, because the stack sorts them and a test checks that it does.
var AvailabilityZones = []string{Region + "c", Region + "a", Region + "b"}

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
	// IgnoreChanges is the resource's ignoreChanges option, exactly as the engine receives it.
	IgnoreChanges []string
}

// Invoke is one data-source call the program made.
type Invoke struct {
	Token    string
	Provider string
	Args     map[string]any
}

// Recorder is a pulumi.MockResourceMonitor that remembers every resource and every invoke.
type Recorder struct {
	mu        sync.Mutex
	resources []Resource
	invokes   []Invoke
}

// NewResource answers a registration with the inputs echoed back, plus the outputs AWS would
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
	arn := func(service, rest string) string {
		return fmt.Sprintf("arn:aws:%s:%s:%s:%s", service, Region, AccountID, rest)
	}
	switch args.TypeToken {
	case "aws:ecr/repository:Repository":
		set("arn", arn("ecr", "repository/"+str("name")))
		set("repositoryUrl", fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com/%s", AccountID, Region, str("name")))
	case "aws:iam/role:Role":
		set("arn", "arn:aws:iam::"+AccountID+":role/"+str("name"))
	case "aws:secretsmanager/secret:Secret":
		// Secrets Manager appends six random characters to every secret ARN.
		set("arn", arn("secretsmanager", "secret:"+str("name")+"-AbCdEf"))
	case "aws:ecs/cluster:Cluster":
		set("arn", arn("ecs", "cluster/"+str("name")))
	case "aws:ecs/expressGatewayService:ExpressGatewayService":
		cluster := str("cluster")
		set("serviceArn", arn("ecs", "service/"+cluster+"/"+str("serviceName")))
		outputs["ingressPaths"] = resource.NewArrayProperty([]resource.PropertyValue{
			resource.NewObjectProperty(resource.PropertyMap{
				"accessType": resource.NewStringProperty("PUBLIC"),
				"endpoint":   resource.NewStringProperty(str("serviceName") + ".ecs." + Region + ".on.aws"),
			}),
		})
	case "aws:ecs/taskDefinition:TaskDefinition":
		set("arn", arn("ecs", "task-definition/"+str("family")+":1"))
		set("arnWithoutRevision", arn("ecs", "task-definition/"+str("family")))
	case "aws:cloudwatch/logGroup:LogGroup":
		set("arn", arn("logs", "log-group:"+str("name")))
	case "aws:rds/instance:Instance":
		set("address", str("identifier")+".abcdefghijkl."+Region+".rds.amazonaws.com")
		outputs["port"] = resource.NewNumberProperty(5432)
		set("resourceId", DatabaseResourceID)
		// What RDS reports once manageMasterUserPassword has made the master secret.
		if v, ok := args.Inputs["manageMasterUserPassword"]; ok && v.IsBool() && v.BoolValue() {
			outputs["masterUserSecrets"] = resource.NewArrayProperty([]resource.PropertyValue{
				resource.NewObjectProperty(resource.PropertyMap{
					"secretArn":    resource.NewStringProperty(MasterSecretARN),
					"secretStatus": resource.NewStringProperty("active"),
				}),
			})
		}
	}

	var deps, ignore []string
	if args.RegisterRPC != nil {
		deps = args.RegisterRPC.GetDependencies()
		ignore = args.RegisterRPC.GetIgnoreChanges()
	}
	var secret []string
	for key, value := range args.Inputs {
		if value.ContainsSecrets() {
			secret = append(secret, string(key))
		}
	}
	r.mu.Lock()
	r.resources = append(r.resources, Resource{
		Type:          args.TypeToken,
		Name:          args.Name,
		Custom:        args.Custom,
		Provider:      args.Provider,
		Inputs:        plain(args.Inputs),
		Secret:        secret,
		Dependencies:  deps,
		IgnoreChanges: ignore,
	})
	r.mu.Unlock()
	return args.Name + "-id", outputs, nil
}

// Call answers the invokes the program makes, and records which provider each went through.
func (r *Recorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	r.mu.Lock()
	r.invokes = append(r.invokes, Invoke{Token: args.Token, Provider: args.Provider, Args: plain(args.Args)})
	r.mu.Unlock()

	switch args.Token {
	case "aws:index/getAvailabilityZones:getAvailabilityZones":
		return resource.NewPropertyMapFromMap(map[string]any{
			"id":    Region,
			"names": AvailabilityZones,
		}), nil
	case "aws:iam/getOpenIdConnectProvider:getOpenIdConnectProvider":
		return resource.NewPropertyMapFromMap(map[string]any{
			"arn": OIDCProviderARN,
			"url": "token.actions.githubusercontent.com",
		}), nil
	case "aws:kms/getKey:getKey":
		return resource.NewPropertyMapFromMap(map[string]any{
			"arn": StateKeyARN,
			"id":  "00000000-0000-0000-0000-000000000000",
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

// Invokes is every data-source call made so far.
func (r *Recorder) Invokes() []Invoke {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Invoke(nil), r.invokes...)
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

// Capture resolves an output inside a running program into dst.
//
// It exports the applied value rather than merely applying it: an export is awaited before the
// program returns, while an apply on a value that belongs to no resource — pulumi.String(...), or
// a Sprintf over one — may still be pending when it does. name must be unique within the run.
func Capture[T any](ctx *pulumi.Context, name string, output pulumi.Output, dst *T) {
	ctx.Export("capture:"+name, output.ApplyT(func(v any) (any, error) {
		value, ok := v.(T)
		if !ok {
			return nil, fmt.Errorf("captured %T, want %T", v, *new(T))
		}
		*dst = value
		return v, nil
	}))
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

// EnvOf turns a container's `environments` and `secrets` lists into name -> value, or
// name -> "secret:<valueFrom>" for a secret mount. It reads the Express service's primaryContainer
// shape (`environments`) and an ECS container definition's (`environment`) alike.
func EnvOf(container any) map[string]string {
	out := map[string]string{}
	for _, key := range []string{"environments", "environment"} {
		envs, _ := Get(container, key).([]any)
		for _, env := range envs {
			name, _ := Get(env, "name").(string)
			out[name], _ = Get(env, "value").(string)
		}
	}
	secrets, _ := Get(container, "secrets").([]any)
	for _, secret := range secrets {
		name, _ := Get(secret, "name").(string)
		out[name] = fmt.Sprintf("secret:%v", Get(secret, "valueFrom"))
	}
	return out
}

// JSON decodes a JSON-string input — an IAM policy document, a task definition's container
// definitions — into plain values. It fails the test on anything that is not JSON.
func JSON(t testing.TB, value any) any {
	t.Helper()
	text, ok := value.(string)
	if !ok {
		t.Fatalf("want a JSON string, got %T", value)
	}
	var out any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, text)
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
