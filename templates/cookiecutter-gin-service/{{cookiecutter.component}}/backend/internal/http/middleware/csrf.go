package middleware

import (
	"slices"

	"github.com/gin-gonic/gin"

	"{{ cookiecutter.go_module }}/internal/apperr"
)

// CSRFHeader is the header the web client sends on the two cookie-authenticated routes. Its value
// is not checked — its presence is the defence, because a cross-origin page cannot add a custom
// header without a CORS preflight that only the configured origins pass.
const CSRFHeader = "X-Requested-With"

// RequireBrowserCaller refuses a cookie-authenticated request a third-party page could have started:
// the custom header must be present, and an Origin, when the browser sends one, must be on the CORS
// allowlist. A cross-site form post carries the cookie and neither.
//
// 403 rather than 401: the session may be perfectly valid, and the web client must not read this
// as one that has ended.
func RequireBrowserCaller(allowedOrigins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader(CSRFHeader) == "" {
			_ = c.Error(apperr.Forbidden("The " + CSRFHeader + " header is required"))
			c.Abort()
			return
		}
		// Present-but-empty counts as sent: an Origin of "" is on nobody's allowlist.
		if origins, sent := c.Request.Header["Origin"]; sent && (len(origins) != 1 || !slices.Contains(allowedOrigins, origins[0])) {
			_ = c.Error(apperr.Forbidden("Origin not allowed"))
			c.Abort()
			return
		}
		c.Next()
	}
}
