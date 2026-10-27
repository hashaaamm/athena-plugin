// Package middleware is the cross-cutting half of the HTTP layer: errors, authentication, CSRF,
// CORS, request logging and body limits. Each is applied once, in internal/http/router.go, rather
// than remembered per handler — a property enforced at call sites is a property until somebody
// forgets.
package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"{{ cookiecutter.go_module }}/internal/apperr"
)

// Reporter sends an unexpected error to an error tracker. trace_id is the id the client was given,
// so a support ticket carrying it is one search away from the event. Nil means "log only".
type Reporter func(c *gin.Context, err error, traceID string)

// Errors is the one place an error becomes a response, and the recovery for a panicking handler.
//
// A deliberate error (an *apperr.Error anywhere in the chain) answers with its own status and code,
// logged at warning and never reported: it is control flow, not a defect. Every 401 carries
// `WWW-Authenticate: Bearer`. Anything else — a returned error that is not an apperr, or a panic —
// answers 500 `internal_error` with a fresh trace_id, logged at error and reported once with that id.
// The client never sees the error's text: it can carry a SQL fragment, a hostname, or a value.
func Errors(logger *slog.Logger, report Reporter) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			// net/http's own signal for "the client is gone, abort quietly". Swallowing it would
			// try to write a 500 to a closed connection and log a defect that is not one.
			if recovered == http.ErrAbortHandler { //nolint:errorlint // compared by identity, as net/http does
				panic(recovered)
			}
			err := fmt.Errorf("panic: %v", recovered)
			writeUnexpected(c, logger, report, err, "stack", string(debug.Stack()))
		}()

		c.Next()

		if len(c.Errors) == 0 {
			return
		}
		err := c.Errors.Last().Err
		if c.Writer.Written() {
			// Too late to change the status the client was given. Say so rather than pretend.
			logger.ErrorContext(c.Request.Context(), "error_after_response_written", "error", err.Error())
			return
		}
		if appErr, ok := apperr.As(err); ok {
			writeAppError(c, logger, appErr)
			return
		}
		writeUnexpected(c, logger, report, err)
	}
}

func writeAppError(c *gin.Context, logger *slog.Logger, err *apperr.Error) {
	logger.WarnContext(c.Request.Context(), "app_error", "code", err.Code, "detail", err.Message, "status", err.Status)
	if err.Status == http.StatusUnauthorized {
		// RFC 9110 requires it on every 401, and a client that does not get it will not attempt
		// re-authentication because nothing told it which scheme to use.
		c.Header("WWW-Authenticate", "Bearer")
	}
	c.AbortWithStatusJSON(err.Status, apperr.Body(err.Code, err.Message, ""))
}

func writeUnexpected(c *gin.Context, logger *slog.Logger, report Reporter, err error, extra ...any) {
	traceID := uuid.NewString()
	if report != nil {
		report(c, err, traceID)
	}
	// Go errors carry no stack. The message chain is what locates the failure, which is why every
	// layer wraps with what it was doing: `fmt.Errorf("create user: %w", err)`.
	args := append([]any{"trace_id", traceID, "error", err.Error(), "method", c.Request.Method, "path", c.FullPath()}, extra...)
	logger.ErrorContext(c.Request.Context(), "unhandled_error", args...)
	c.AbortWithStatusJSON(http.StatusInternalServerError, apperr.Body("internal_error", "Internal server error", traceID))
}

// NotFound answers a path no route matches, in the API's error shape rather than gin's plain text.
func NotFound(c *gin.Context) {
	_ = c.Error(apperr.NotFound("Not Found"))
}

// MethodNotAllowed answers a known path called with a method it does not serve.
func MethodNotAllowed(c *gin.Context) {
	_ = c.Error(apperr.MethodNotAllowed())
}
