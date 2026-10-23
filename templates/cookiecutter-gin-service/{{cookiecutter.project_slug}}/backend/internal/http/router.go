// Package httpapi assembles the HTTP surface: middleware, route groups and the docs. One place to
// see every route the service answers, and which of them are open.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	swaggerfiles "github.com/swaggo/files"
	ginswagger "github.com/swaggo/gin-swagger"

	"{{ cookiecutter.go_module }}/internal/http/handler"
	"{{ cookiecutter.go_module }}/internal/http/middleware"
{%- if cookiecutter.use_postgres == "yes" %}
	"{{ cookiecutter.go_module }}/internal/security"
{%- endif %}
)

// maxBodyBytes is the largest request body any route reads. Generous for JSON, and far short of the
// memory a hostile client could otherwise make a bind allocate.
const maxBodyBytes = 1 << 20

// APIPrefix is the versioned prefix every resource is mounted under. The health probes are not: a
// load balancer does not know about /api/v1.
const APIPrefix = "/api/v1"

func init() {
	// Release mode: debug mode prints every route on start-up and warns on every request in
	// production logs. Set here, once, because it is process-global state.
	gin.SetMode(gin.ReleaseMode)
}

// Options is what the router needs that is not a handler.
type Options struct {
	Logger *slog.Logger
	// Reports unexpected errors to an error tracker. Nil logs only.
	Report middleware.Reporter
	// Middleware that must wrap error handling — Sentry's per-request hub, so an error reported
	// from inside carries the request it happened in.
	Outer []gin.HandlerFunc
	// Empty turns CORS off: no browser on another origin may call this API.
	CORSOrigins []string
	// /docs and /openapi.json. Off in production.
	DocsEnabled bool
	// The OpenAPI 3 document /openapi.json serves.
	OpenAPI []byte
{%- if cookiecutter.use_postgres == "yes" %}
	// Verifies bearer tokens on the private group.
	Tokens *security.Tokens
	// Mount the cookie-transport auth routes rather than the body ones. See handler/auth.go.
	CookieTransport bool
{%- endif %}
}

// Handlers is every handler the router mounts.
type Handlers struct {
	Health handler.Health
{%- if cookiecutter.use_postgres == "yes" %}
	Auth   *handler.Auth
{%- endif %}
}

// New builds the router.
//
// Middleware order is load-bearing, outermost first:
//
//  1. RequestLogger, so the line it writes carries the status the client actually got — including
//     a 500 from a panic.
//  2. Outer (Sentry), so the error handler below reports into a hub that knows the request.
//  3. Errors, the one place an error or a panic becomes a response.
//  4. CORS, inside Errors, so its headers are already on the response when an error is written; a
//     browser shown a 401 without them reports a CORS failure instead of the 401.
//  5. BodyLimit, before anything reads a body.
func New(opts Options, h Handlers) (*gin.Engine, error) {
	if err := handler.RegisterValidators(); err != nil {
		return nil, err
	}

	router := gin.New()
	// Gin trusts every proxy by default, which makes c.ClientIP() the left-most X-Forwarded-For entry
	// — a value any caller can write. Nothing here reads the client IP; the first thing that does (a
	// rate limiter) wants the entry Cloud Run's front end appended, the right-most one, not this.
	if err := router.SetTrustedProxies(nil); err != nil {
		return nil, err
	}
	router.HandleMethodNotAllowed = true
	router.NoRoute(middleware.NotFound)
	router.NoMethod(middleware.MethodNotAllowed)

	router.Use(middleware.RequestLogger(opts.Logger))
	router.Use(opts.Outer...)
	router.Use(middleware.Errors(opts.Logger, opts.Report))
	if len(opts.CORSOrigins) > 0 {
		router.Use(middleware.CORS(opts.CORSOrigins))
	}
	router.Use(middleware.BodyLimit(maxBodyBytes))

	router.GET("/health/live", h.Health.Live)
	router.GET("/health/ready", h.Health.Ready)

	if opts.DocsEnabled {
		document := opts.OpenAPI
		router.GET("/openapi.json", func(c *gin.Context) {
			c.Data(http.StatusOK, "application/json", document)
		})
		// Swagger UI reads the OpenAPI 3 document above, not swag's Swagger 2.0 one: one document,
		// so what the UI shows is what a generated client is built from.
		router.GET("/docs", func(c *gin.Context) {
			c.Redirect(http.StatusFound, "/docs/index.html")
		})
		router.GET("/docs/*any", ginswagger.WrapHandler(swaggerfiles.Handler,
			ginswagger.URL("/openapi.json"),
			ginswagger.DefaultModelsExpandDepth(-1),
			ginswagger.PersistAuthorization(true),
		))
	}

	api := router.Group(APIPrefix)
{%- if cookiecutter.use_postgres == "yes" %}

	// No bearer token required. Today: register, login, refresh and logout — the first two are how a
	// caller gets a credential, and the last two take a refresh token, which is never a bearer one.
	public := api.Group("")
	// A verified access token required, for every route on it. Put a new resource here unless you
	// can say out loud why an anonymous caller may have it.
	private := api.Group("", middleware.RequireActor(opts.Tokens))

	public.POST("/auth/register", h.Auth.Register)
	private.GET("/auth/me", h.Auth.Me)
	if opts.CookieTransport {
		csrf := middleware.RequireBrowserCaller(opts.CORSOrigins)
		public.POST("/auth/login", h.Auth.LoginCookie)
		public.POST("/auth/refresh", csrf, h.Auth.RefreshCookie)
		public.POST("/auth/logout", csrf, h.Auth.LogoutCookie)
		private.POST("/auth/change-password", h.Auth.ChangePasswordCookie)
	} else {
		public.POST("/auth/login", h.Auth.LoginBody)
		public.POST("/auth/refresh", h.Auth.RefreshBody)
		public.POST("/auth/logout", h.Auth.LogoutBody)
		private.POST("/auth/change-password", h.Auth.ChangePasswordBody)
	}

	// private.GET("/<resources>", h.<Resource>.List)
{%- else %}

	// Empty on a fresh project: with no database there is nothing to authenticate against and no
	// resource to serve. Every resource goes here, one line per route.
	//
	// api.GET("/<resources>", h.<Resource>.List)
	_ = api
{%- endif %}

	return router, nil
}
