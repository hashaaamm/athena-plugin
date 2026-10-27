package middleware_test

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"{{ cookiecutter.go_module }}/internal/apperr"
	"{{ cookiecutter.go_module }}/internal/http/middleware"
)

// The one place an error becomes a response. Deliberate errors keep their status and code;
// everything else is a 500 that tells the client nothing but a trace id, and is reported once with
// that same id.

type reported struct {
	err     error
	traceID string
}

func serve(t *testing.T, handler gin.HandlerFunc) (*httptest.ResponseRecorder, []reported) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var reports []reported
	router := gin.New()
	router.Use(middleware.Errors(slog.New(slog.DiscardHandler), func(_ *gin.Context, err error, traceID string) {
		reports = append(reports, reported{err: err, traceID: traceID})
	}))
	router.GET("/", handler)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	return recorder, reports
}

func envelope(t *testing.T, recorder *httptest.ResponseRecorder) apperr.ErrorDetail {
	t.Helper()
	var body apperr.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not the error envelope: %v", recorder.Body.String(), err)
	}
	return body.Error
}

func TestADeliberateErrorKeepsItsStatusAndCodeAndIsNotReported(t *testing.T) {
	recorder, reports := serve(t, func(c *gin.Context) { _ = c.Error(apperr.Conflict("taken")) })

	detail := envelope(t, recorder)
	if recorder.Code != http.StatusConflict || detail.Code != "conflict" || detail.Message != "taken" || detail.TraceID != nil {
		t.Fatalf("%d %+v", recorder.Code, detail)
	}
	if len(reports) != 0 {
		t.Fatal("a deliberate error was reported as a defect")
	}
}

// A wrapped apperr is still that apperr: a service may add context without losing the status.
func TestAWrappedDeliberateErrorIsStillRecognised(t *testing.T) {
	recorder, _ := serve(t, func(c *gin.Context) {
		_ = c.Error(errors.Join(errors.New("context"), apperr.Forbidden("no")))
	})

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", recorder.Code)
	}
}

// RFC 9110: every 401 says which scheme to use, or a client will not try to authenticate.
func TestEvery401CarriesWWWAuthenticate(t *testing.T) {
	recorder, _ := serve(t, func(c *gin.Context) { _ = c.Error(apperr.Unauthorized("")) })

	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("%d %v", recorder.Code, recorder.Header())
	}
	if detail := envelope(t, recorder); detail.Message != "Not authenticated" {
		t.Fatalf("message %q, want the default", detail.Message)
	}
}

func TestAnUnexpectedErrorIsA500WithATraceIDAndIsReportedWithIt(t *testing.T) {
	recorder, reports := serve(t, func(c *gin.Context) {
		_ = c.Error(errors.New("pq: password authentication failed for user admin at 10.0.0.2"))
	})

	detail := envelope(t, recorder)
	if recorder.Code != http.StatusInternalServerError || detail.Code != "internal_error" || detail.TraceID == nil {
		t.Fatalf("%d %+v", recorder.Code, detail)
	}
	if strings.Contains(recorder.Body.String(), "10.0.0.2") {
		t.Fatal("the 500 leaks the underlying error")
	}
	if len(reports) != 1 || reports[0].traceID != *detail.TraceID {
		t.Fatalf("reports %+v; want one, with the trace id the client got", reports)
	}
}

// A panicking handler is a 500 in the API's shape, not a dropped connection.
func TestAPanicIsA500WithATraceID(t *testing.T) {
	recorder, reports := serve(t, func(*gin.Context) { panic("nil map") })

	detail := envelope(t, recorder)
	if recorder.Code != http.StatusInternalServerError || detail.Code != "internal_error" || detail.TraceID == nil {
		t.Fatalf("%d %+v", recorder.Code, detail)
	}
	if len(reports) != 1 || !strings.Contains(reports[0].err.Error(), "nil map") {
		t.Fatalf("reports %+v", reports)
	}
}

// net/http's own "client went away" signal is not a defect, and must reach net/http.
func TestAbortHandlerIsNotSwallowed(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler { //nolint:errorlint // identity, as net/http compares it
			t.Fatalf("recovered %v, want http.ErrAbortHandler re-panicked", recovered)
		}
	}()
	serve(t, func(*gin.Context) { panic(http.ErrAbortHandler) })
}
