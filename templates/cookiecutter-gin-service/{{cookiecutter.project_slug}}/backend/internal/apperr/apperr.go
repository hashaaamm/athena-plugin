// Package apperr is the application's error vocabulary, and the one response shape every failure
// takes.
//
// Layers below the transport return these. Nothing below internal/http knows what a response
// writer is — a service must be callable from a worker, a CLI or a test with no HTTP stack anywhere
// — so an error carries its status as a plain number and the transport decides how to send it
// (internal/http/middleware/errors.go). The status lives with the constructor, not at the return
// site, so one rule cannot answer 404 from one caller and 400 from another.
//
// Ask Athena for how errors and logging are handled.
package apperr

import (
	"errors"
	"net/http"
)

// Error is every error this application returns deliberately. Anything else reaching the
// transport is a defect: it answers 500 with a trace id and is reported.
type Error struct {
	// Status is the HTTP status the transport answers with.
	Status int
	// Code is stable and machine-readable. Clients branch on it; never reword one.
	Code string
	// Message is human-readable and safe to show a developer. It never carries a credential, a
	// digest or the detail of which authentication check failed.
	Message string
	// KeepsWrites says the writes made before this error was returned are committed rather than
	// rolled back. False for every error but the one whose refusal *is* the write — see
	// CompromisedSession. Read by repository.Store.WithTx and nowhere else.
	KeepsWrites bool
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// As returns the *Error in err's chain, if there is one.
func As(err error) (*Error, bool) {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr, true
	}
	return nil, false
}

// KeepsWrites reports whether err asks for its transaction to be committed anyway.
func KeepsWrites(err error) bool {
	appErr, ok := As(err)
	return ok && appErr.KeepsWrites
}

func newError(status int, code, fallback, message string) *Error {
	if message == "" {
		message = fallback
	}
	return &Error{Status: status, Code: code, Message: message}
}

// BadRequest is a request the server understood and refuses for a reason no other code names.
func BadRequest(message string) *Error {
	return newError(http.StatusBadRequest, "bad_request", "Application error", message)
}

// NotFound is a resource that does not exist, or that the caller may not know exists.
func NotFound(message string) *Error {
	return newError(http.StatusNotFound, "not_found", "Resource not found", message)
}

// Conflict is a write that collides with existing state: a duplicate key, a stale version.
func Conflict(message string) *Error {
	return newError(http.StatusConflict, "conflict", "Resource conflict", message)
}

// Validation is a request that failed validation: a body that does not bind, or a rule the schema
// cannot express — uniqueness, ownership, a state machine.
func Validation(message string) *Error {
	return newError(http.StatusUnprocessableEntity, "validation_error", "Request failed validation", message)
}

// Unauthorized is a missing, malformed, expired or revoked credential. The transport adds
// `WWW-Authenticate: Bearer` to every 401: RFC 9110 requires it, and an HTTP client that does not
// get it will not attempt re-authentication because nothing told it which scheme to use.
func Unauthorized(message string) *Error {
	return newError(http.StatusUnauthorized, "unauthorized", "Not authenticated", message)
}

// CompromisedSession is a refresh that was refused *and* whose session family was revoked on the
// way out. It is the one error whose writes are kept.
//
// Every other error rolls its transaction back, and that is right for them; here the revocation is
// the point of the refusal. A rotated refresh token that comes back is a replay or a stolen copy,
// and a rollback would undo the family revocation that is the whole defence — the 401 would go out
// and every other token in the family would still work. The response is the same 401 as any other
// refused token, so a caller learns nothing about which check failed.
func CompromisedSession(message string) *Error {
	err := Unauthorized(message)
	err.KeepsWrites = true
	return err
}

// Forbidden is an authenticated caller who has not earned this operation. Never used for a missing
// credential: a client that receives 403 reads it as a permissions problem and does not try to
// authenticate.
func Forbidden(message string) *Error {
	return newError(http.StatusForbidden, "forbidden", "Not permitted", message)
}

// MethodNotAllowed is a known path called with a method it does not serve.
func MethodNotAllowed() *Error {
	return newError(http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
}

// ErrorDetail is the body of every failure. One shape for the whole API, so a generated client has
// one thing to handle instead of per-endpoint improvisation.
//
// The `binding:"required"` tags on these response fields are read by swag, which marks the field
// required in the OpenAPI document. Nothing ever binds a request into this type.
type ErrorDetail struct {
	// Stable, machine-readable error code.
	Code string `json:"code" binding:"required" example:"not_found"`
	// Human-readable explanation. Safe to show a developer.
	Message string `json:"message" binding:"required"`
	// Present on unhandled failures. A support ticket carrying this is one lookup from the event.
	TraceID *string `json:"trace_id" extensions:"x-nullable"`
} // @name ErrorDetail

// ErrorResponse wraps ErrorDetail: `{"error": {"code", "message", "trace_id"}}`.
type ErrorResponse struct {
	Error ErrorDetail `json:"error" binding:"required"`
} // @name ErrorResponse

// Body builds the envelope for err. traceID is empty for deliberate errors.
func Body(code, message, traceID string) ErrorResponse {
	detail := ErrorDetail{Code: code, Message: message}
	if traceID != "" {
		detail.TraceID = &traceID
	}
	return ErrorResponse{Error: detail}
}
