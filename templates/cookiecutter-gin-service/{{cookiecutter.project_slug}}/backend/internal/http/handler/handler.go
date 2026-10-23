// Package handler is the HTTP layer: each handler binds input, makes exactly one service call, and
// writes the result. There is nothing here worth unit testing, and that is the point — the rules
// live in internal/service, and the handlers are tested through the router.
//
// Handlers never write an error themselves. They hand it to Fail, and the error middleware
// (internal/http/middleware/errors.go) is the one place an error becomes a response, so the envelope,
// the WWW-Authenticate header and the trace id cannot differ from one handler to the next.
//
// A handler imports internal/service and nothing below it (depguard enforces it): no repository, no
// pgx, no SQL. Ask Athena for the layered architecture rules.
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"

	"{{ cookiecutter.go_module }}/internal/apperr"
)

// Fail records err for the error middleware and stops the chain. Exported, like the binding
// helpers below, so a resource's handlers can live in a package of their own.
func Fail(c *gin.Context, err error) {
	_ = c.Error(err)
	c.Abort()
}

var registerValidators = sync.OnceValue(func() error {
	engine, ok := binding.Validator.Engine().(*validator.Validate)
	if !ok {
		return errors.New("gin's validator engine is not go-playground/validator")
	}
	// Report fields by their JSON names, which is what the client sent, rather than Go's.
	engine.RegisterTagNameFunc(func(field reflect.StructField) string {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			return ""
		}
		return name
	})
{%- if cookiecutter.use_postgres == "yes" %}
	return engine.RegisterValidation("email_shape", emailShape)
{%- else %}
	return nil
{%- endif %}
})

// RegisterValidators installs the custom validation rules on gin's validator. Gin's validator is
// process-wide, so this runs once however many routers are built; the router calls it.
func RegisterValidators() error {
	return registerValidators()
}

// BindJSON decodes and validates a request body. On failure it records a 422 (or 413) and returns
// false; the handler returns.
func BindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		Fail(c, bindingError(err))
		return false
	}
	return true
}

// BindStrictJSON is BindJSON that also refuses unknown fields. Used for the bodies that carry a
// refresh token: a client sending `{"refreshToken": ...}` should hear about the typo, not get a 401
// for a token it believes it sent.
func BindStrictJSON(c *gin.Context, dst any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		Fail(c, bindingError(err))
		return false
	}
	if err := binding.Validator.ValidateStruct(dst); err != nil {
		Fail(c, bindingError(err))
		return false
	}
	return true
}

// bindingError turns a decode or validation failure into the API's 422, with a message naming the
// fields and rules that failed. It names fields, never values: the value may be a password.
func bindingError(err error) error {
	var tooLarge *http.MaxBytesError
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var invalid validator.ValidationErrors
	switch {
	case errors.As(err, &tooLarge):
		return &apperr.Error{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large", Message: "Request body is too large"}
	case errors.Is(err, io.EOF):
		return apperr.Validation("Request body is required")
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		return apperr.Validation("Request body is not valid JSON")
	case errors.As(err, &typeErr):
		return apperr.Validation(fmt.Sprintf("%s: must be %s", typeErr.Field, typeErr.Type))
	case errors.As(err, &invalid):
		messages := make([]string, 0, len(invalid))
		for _, field := range invalid {
			messages = append(messages, describe(field))
		}
		return apperr.Validation(strings.Join(messages, "; "))
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		return apperr.Validation(strings.TrimPrefix(err.Error(), "json: "))
	default:
		return apperr.Validation("Request failed validation")
	}
}

func describe(field validator.FieldError) string {
	switch field.Tag() {
	case "required":
		return field.Field() + ": is required"
	case "min":
		return fmt.Sprintf("%s: must be at least %s characters", field.Field(), field.Param())
	case "max":
		return fmt.Sprintf("%s: must be at most %s characters", field.Field(), field.Param())
{%- if cookiecutter.use_postgres == "yes" %}
	case "email_shape":
		return field.Field() + ": must look like name@example.com"
{%- endif %}
	default:
		return fmt.Sprintf("%s: failed the %q rule", field.Field(), field.Tag())
	}
}
