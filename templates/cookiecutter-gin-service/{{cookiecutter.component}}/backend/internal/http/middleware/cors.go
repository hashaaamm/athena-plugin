package middleware

import (
	"net/http"
	"slices"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// CORS lets the configured browser origins call this API, with credentials, and nobody else.
//
// The SPA is served from a different origin than the API — another port locally, another host
// deployed — so without this every request from the browser fails preflight and the network tab is
// the only place that says why. The router installs it only when CORS_ORIGINS is non-empty; an empty
// list means no browser may call this API, the safe default for a service with no web client.
//
// Two deliberate departures from gin-contrib/cors's defaults:
//
//   - A request from an origin not on the list passes through untouched, without CORS headers,
//     rather than being refused with a bare 403. The browser then withholds the response from the
//     page, which is the protection; a server-to-server caller that happens to send Origin still
//     works; and the CSRF check on the cookie routes answers in the API's own error shape.
//   - Allowed headers are listed, not "*". With credentials, browsers treat a literal "*" as a header
//     *named* "*", so the wildcard would quietly refuse Authorization. A header the web client
//     starts sending — a tracing header, say — is added here, or its preflight fails.
func CORS(origins []string) gin.HandlerFunc {
	handler := cors.New(cors.Config{
		AllowOrigins:     origins,
		AllowCredentials: true,
		AllowMethods: []string{
			http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
			http.MethodDelete, http.MethodHead, http.MethodOptions,
		},
		// X-Requested-With is the CSRF header the cookie routes require (csrf.go).
		AllowHeaders: []string{"Authorization", "Content-Type", "Accept", "X-Requested-With"},
		// 200 rather than the library's 204: some older clients treat a 204 preflight as a failure.
		OptionsResponseStatusCode: http.StatusOK,
		MaxAge:                    10 * time.Minute,
	})
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); origin != "" && slices.Contains(origins, origin) {
			handler(c)
			return
		}
		c.Next()
	}
}
