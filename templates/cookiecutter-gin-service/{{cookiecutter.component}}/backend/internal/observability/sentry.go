// Package observability is the Sentry wiring.
//
// Optional by construction: an empty SENTRY_DSN means no error tracking, and that MUST be the local
// default — developer laptops reporting into a real project make environment filtering useless.
// Ask Athena for the Sentry error-tracking rules.
package observability

import (
	"fmt"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	sentrygin "github.com/getsentry/sentry-go/gin"
	"github.com/gin-gonic/gin"

	"{{ cookiecutter.go_module }}/internal/config"
)

// denyKeys are header, cookie and body keys whose values never leave the process. Sentry retains
// payloads, so a leaked token there is a rotation exercise, not a log line.
var denyKeys = []string{"authorization", "cookie", "set-cookie", "password", "token", "api_key", "api-key", "secret"}

// Init configures the SDK and returns the flush to run on shutdown. With no DSN it does nothing,
// and Report and Middleware are then harmless no-ops.
func Init(cfg config.Config) (flush func(), err error) {
	if cfg.SentryDSN == "" {
		return func() {}, nil
	}
	err = sentry.Init(sentry.ClientOptions{
		Dsn:         cfg.SentryDSN.Reveal(),
		Environment: string(cfg.Environment),
		// Without a release nothing can be attributed to a deploy, and suspect commits never work.
		Release:          cfg.GitSHA,
		EnableTracing:    cfg.SentryTracesSampleRate > 0,
		TracesSampleRate: cfg.SentryTracesSampleRate,
		BeforeSend:       scrub,
	})
	if err != nil {
		return nil, fmt.Errorf("init sentry: %w", err)
	}
	// Bounded: Cloud Run gives a stopping container ten seconds in all.
	return func() { sentry.Flush(2 * time.Second) }, nil
}

// Middleware gives every request its own hub, carrying the request, so an event reported while
// handling it says which request it was. It does not recover panics for us: the error middleware
// inside it does, and reports through Report with the trace id the client was given.
func Middleware() gin.HandlerFunc {
	return sentrygin.New(sentrygin.Options{Repanic: true})
}

// Report sends an unexpected error to Sentry, tagged with the trace id the client received.
func Report(c *gin.Context, err error, traceID string) {
	hub := sentrygin.GetHubFromContext(c)
	if hub == nil {
		hub = sentry.CurrentHub().Clone()
	}
	hub.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("trace_id", traceID)
		hub.CaptureException(err)
	})
}

// scrub removes credentials from an event before it is sent.
func scrub(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event.Request == nil {
		return event
	}
	for key := range event.Request.Headers {
		if denied(key) {
			event.Request.Headers[key] = "[Filtered]"
		}
	}
	if event.Request.Cookies != "" {
		event.Request.Cookies = "[Filtered]"
	}
	// The SDK scrubs sensitive request data by default; this is the second lock on the same door.
	if event.Request.Data != "" {
		event.Request.Data = "[Filtered]"
	}
	return event
}

func denied(key string) bool {
	key = strings.ToLower(key)
	for _, deny := range denyKeys {
		if strings.Contains(key, deny) {
			return true
		}
	}
	return false
}
