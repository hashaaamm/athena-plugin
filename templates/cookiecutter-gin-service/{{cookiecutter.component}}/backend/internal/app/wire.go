// Package app is the composition root: the only place the object graph is built.
//
// Construction happens here and nowhere else, because this is the single seam a test replaces. A
// handler that builds its own service cannot be given a different one, and a graph assembled in
// three files cannot be reasoned about. A new resource adds a few lines to NewRouter — its
// repository accessor already exists on the Store, so: its service, its handler, and its routes in
// internal/http/router.go — and nothing anywhere else constructs any of them.
//
// It is also the one place outside internal/repository that touches pgx (the pool, in server.go),
// and the only importer of every layer at once.
package app

import (
	"log/slog"
{%- if cookiecutter.use_postgres == "yes" %}
	"net/http"
	"time"
{%- endif %}

	"github.com/gin-gonic/gin"

	"{{ cookiecutter.go_module }}/docs"
	"{{ cookiecutter.go_module }}/internal/config"
	httpapi "{{ cookiecutter.go_module }}/internal/http"
	"{{ cookiecutter.go_module }}/internal/http/handler"
{%- if cookiecutter.use_sentry == "yes" %}
	"{{ cookiecutter.go_module }}/internal/observability"
{%- endif %}
{%- if cookiecutter.use_postgres == "yes" %}
	"{{ cookiecutter.go_module }}/internal/repository"
	"{{ cookiecutter.go_module }}/internal/security"
{%- endif %}
	"{{ cookiecutter.go_module }}/internal/service"
)

// Deps is what the graph is built from that configuration cannot provide.
type Deps struct {
	Logger *slog.Logger
{%- if cookiecutter.use_postgres == "yes" %}
	// The pgx pool in the service; a test's transaction in the suite (see repository.DB).
	DB repository.DB
	// Nil means Argon2id at the production parameters. Tests pass a cheaper Hasher so a suite of a
	// hundred logins does not spend a minute in key derivation.
	Hasher *security.Hasher
{%- endif %}
}

// NewRouter builds the whole HTTP surface from configuration.
//
{%- if cookiecutter.use_postgres == "yes" %}
// Before anything else it refuses to build a server around a guessable signing key. Here and not in
// config.Validate, because the migrate job is granted no key and signs nothing; every process that
// can issue a token comes through this function, and the test that proves it calls this function.
{%- else %}
// The service has no dependencies yet, so the graph is two objects. It grows here, never in a
// handler.
{%- endif %}
func NewRouter(cfg config.Config, deps Deps) (*gin.Engine, error) {
{%- if cookiecutter.use_postgres == "yes" %}
	if err := cfg.RequireSigningKey(); err != nil {
		return nil, err
	}

	store := repository.New(deps.DB)
	tokens := security.NewTokens(security.TokenConfig{
		Secret:     cfg.JWTSecret.Reveal(),
		Issuer:     cfg.JWTIssuer,
		Audience:   cfg.JWTAudience,
		AccessTTL:  time.Duration(cfg.AccessTokenTTLSeconds) * time.Second,
		RefreshTTL: time.Duration(cfg.RefreshTokenTTLDays) * 24 * time.Hour,
	})
	hasher := deps.Hasher
	if hasher == nil {
		hasher = security.NewHasher(security.DefaultArgon2Params, 0)
	}
	cookieTransport := cfg.RefreshTransport == config.TransportCookie
	document := docs.OpenAPI
	if cookieTransport {
		document = docs.OpenAPICookie
	}

	handlers := httpapi.Handlers{
		Health: handler.Health{Service: service.NewHealthService(store, cfg.GitSHA)},
		Auth: &handler.Auth{
			Service: service.NewAuthService(store, hasher, tokens, deps.Logger),
			Cookie: handler.CookieSettings{
				Path:     httpapi.APIPrefix + "/auth",
				MaxAge:   cfg.RefreshTokenTTLDays * 24 * 60 * 60,
				Secure:   cfg.RefreshCookieSecure,
				SameSite: sameSite(cfg.RefreshCookieSameSite),
			},
		},
	}
{%- else %}
	document := docs.OpenAPI
	handlers := httpapi.Handlers{
		Health: handler.Health{Service: service.NewHealthService(cfg.GitSHA)},
	}
{%- endif %}

	opts := httpapi.Options{
		Logger:      deps.Logger,
		CORSOrigins: cfg.CORSOrigins(),
		DocsEnabled: cfg.DocsEnabled(),
		OpenAPI:     document,
	}
{%- if cookiecutter.use_postgres == "yes" %}
	opts.Tokens = tokens
	opts.CookieTransport = cookieTransport
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
	opts.Report = observability.Report
	opts.Outer = []gin.HandlerFunc{observability.Middleware()}
{%- endif %}
	return httpapi.New(opts, handlers)
}
{%- if cookiecutter.use_postgres == "yes" %}

func sameSite(value string) http.SameSite {
	switch value {
	case "lax":
		return http.SameSiteLaxMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteStrictMode
	}
}
{%- endif %}
