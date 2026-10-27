package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestLogger writes one structured line per request: method, route, status and duration.
//
// The route template (`/api/v1/auth/me`), never the raw URL: a query string is where somebody will
// one day put a token, and a log line is read by more people than a database is. Health probes are
// logged at debug, because Cloud Run calls them every few seconds and they would otherwise be most of
// the log volume and none of its value.
func RequestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		level := slog.LevelInfo
		if route == "/health/live" || route == "/health/ready" {
			level = slog.LevelDebug
		}
		logger.Log(c.Request.Context(), level, "request",
			"method", c.Request.Method,
			"route", route,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
}

// BodyLimit caps a request body at limit bytes. Without it gin reads whatever it is sent into
// memory, and the password length cap on the schema never gets the chance to refuse a hundred
// megabytes of JSON. Reading past the limit fails the bind, which answers 413.
func BodyLimit(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
