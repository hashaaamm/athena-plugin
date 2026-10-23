// Command openapi converts swag's Swagger 2.0 output into the OpenAPI 3 document the service serves.
//
// A build tool, not part of the service: `just gen` runs it (scripts/gen.sh) and the server binary
// never links it. swag only speaks Swagger 2.0, and the consumers of /openapi.json — the React
// app's openapi-typescript, Swagger UI, any client generator written this decade — expect OpenAPI 3.
// The conversion is kin-openapi's openapi2conv; this file only fixes the handful of things Swagger
// 2.0 cannot say, so they are said in one reviewed place rather than patched by hand:
//
//   - Bearer auth. Swagger 2.0 has only `apiKey` in a header, which makes Swagger UI send the raw
//     token without the `Bearer ` prefix; OpenAPI 3 has `http`/`bearer`, which is what the API takes.
//   - Cookie parameters. Swagger 2.0 has none, so the cookie-transport handlers declare theirs as an
//     `@x-cookie-parameter` extension and it becomes a real `in: cookie` parameter here.
//   - Closed request bodies (`additionalProperties: false`) for the bodies bound strictly.
//   - Transport markers. The handlers that exist under only one refresh-token transport carry a
//     `transport-body` or `transport-cookie` tag so scripts/gen.sh can build one document per
//     transport. The tag is a build detail, not documentation, and is removed here.
//   - `servers`. swag's basePath becomes `servers: [{url: "/"}]`, which tells a generated client to
//     call relative URLs; clients bring their own base URL, so it is dropped.
//
// Output is indented, HTML-unescaped and key-sorted, so a regeneration that changes nothing changes
// no bytes, and CI's drift check (`just gen-check`) compares like with like.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
)

const bearerScheme = "BearerAuth"

// closedSchemas are the request bodies bound with handler.BindStrictJSON, which refuses unknown
// fields. Swagger 2.0 annotations cannot say so per type, so it is said here; a body newly bound
// strictly is added to this list, or the document promises a leniency the server does not have.
var closedSchemas = []string{"RefreshRequest", "LogoutRequest"}

func main() {
	in := flag.String("in", "", "swag's swagger.json")
	out := flag.String("out", "", "where to write the OpenAPI 3 document")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: openapi -in docs/swagger.json -out docs/openapi.json")
		os.Exit(2)
	}
	if err := run(*in, *out); err != nil {
		fmt.Fprintln(os.Stderr, "openapi:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	raw, err := os.ReadFile(in) //nolint:gosec // a path the build passes in, not user input
	if err != nil {
		return err
	}
	var v2 openapi2.T
	if err := json.Unmarshal(raw, &v2); err != nil {
		return fmt.Errorf("read %s: %w", in, err)
	}
	v3, err := openapi2conv.ToV3(&v2)
	if err != nil {
		return fmt.Errorf("convert to OpenAPI 3: %w", err)
	}
	if err := normalise(v3); err != nil {
		return err
	}
	encoded, err := render(v3)
	if err != nil {
		return err
	}
	return os.WriteFile(out, encoded, 0o644) //nolint:gosec // a committed, public document
}

// render serialises the document indented and without HTML escaping. kin-openapi's MarshalJSON
// escapes `<` as `\u003c`, as encoding/json does by default — noise in a document nobody embeds in
// HTML. A round trip through a generic value (UseNumber, so no number is touched), re-encoded with
// escaping off, undoes it.
func render(doc *openapi3.T) ([]byte, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(generic); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func normalise(doc *openapi3.T) error {
	doc.Servers = nil
	// swag always emits `contact: {}`; an empty object documents nothing.
	if contact := doc.Info.Contact; contact != nil && contact.Name == "" && contact.URL == "" && contact.Email == "" {
		doc.Info.Contact = nil
	}

	if doc.Components != nil {
		for _, name := range closedSchemas {
			if schema, ok := doc.Components.Schemas[name]; ok && schema.Value != nil {
				schema.Value.AdditionalProperties = openapi3.AdditionalProperties{Has: new(bool)}
			}
		}
		if scheme, ok := doc.Components.SecuritySchemes[bearerScheme]; ok && scheme.Value != nil {
			description := scheme.Value.Description
			scheme.Value = openapi3.NewJWTSecurityScheme()
			scheme.Value.Description = description
		}
	}

	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			op.Tags = slices.DeleteFunc(op.Tags, func(tag string) bool {
				return strings.HasPrefix(tag, "transport-")
			})
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				// openapi2conv records the Swagger 2.0 body parameter's name; it means nothing in 3.
				delete(op.RequestBody.Value.Extensions, "x-originalParamName")
			}
			if err := cookieParameter(op); err != nil {
				return fmt.Errorf("%s %s: %w", method, path, err)
			}
		}
	}
	return nil
}

// cookieParameter turns `@x-cookie-parameter {"name": ..., "description": ...}` into an OpenAPI 3
// cookie parameter.
func cookieParameter(op *openapi3.Operation) error {
	raw, ok := op.Extensions["x-cookie-parameter"]
	if !ok {
		return nil
	}
	delete(op.Extensions, "x-cookie-parameter")
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var spec struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(encoded, &spec); err != nil {
		return fmt.Errorf("x-cookie-parameter: %w", err)
	}
	if spec.Name == "" {
		return errors.New("x-cookie-parameter needs a name")
	}
	param := openapi3.NewCookieParameter(spec.Name).WithSchema(openapi3.NewStringSchema()).WithDescription(spec.Description)
	op.Parameters = append(op.Parameters, &openapi3.ParameterRef{Value: param})
	return nil
}
