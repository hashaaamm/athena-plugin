// Command api is the service's one binary. The production image runs `api serve`; the migrate job
// runs the same image with `api migrate up`; the image's HEALTHCHECK runs `api healthcheck`.
//
//	api [serve]                    run the HTTP server until SIGTERM
{%- if cookiecutter.use_postgres == "yes" %}
//	api migrate up|down|status     apply, roll back one, or list migrations
{%- endif %}
//	api healthcheck                exit 0 if this container's server answers /health/live
//
// One binary rather than three, so what is migrated, served and probed is the same build — a
// migrate job running last week's migrations against this week's code cannot happen.
//
// The annotations below are swag's general API info, read by `just gen`.
//
// @title						{{ cookiecutter.project_name }}
// @version					0.1.0
// @description				{{ cookiecutter.description }}
// @BasePath					/
{%- if cookiecutter.use_postgres == "yes" %}
// @securityDefinitions.apikey	BearerAuth
// @in							header
// @name						Authorization
// @description				An access token from `POST /api/v1/auth/login`, sent as `Authorization: Bearer <token>`.
{%- endif %}
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"{{ cookiecutter.go_module }}/internal/app"
	"{{ cookiecutter.go_module }}/internal/config"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// SIGTERM is how Cloud Run (and `docker stop`) asks a container to stop; SIGINT is Ctrl-C. Both
	// cancel ctx, which starts the drain in app.Serve rather than killing requests mid-flight.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration:", err)
		return 1
	}
	logger := app.NewLogger(cfg, os.Stdout)

	command := "serve"
	if len(args) > 0 {
{%- if cookiecutter.use_postgres == "yes" %}
		command, args = args[0], args[1:]
{%- else %}
		command = args[0]
{%- endif %}
	}
	switch command {
	case "serve":
		err = app.Serve(ctx, cfg, logger)
{%- if cookiecutter.use_postgres == "yes" %}
	case "migrate":
		err = app.Migrate(ctx, cfg, logger, args, os.Stdout)
{%- endif %}
	case "healthcheck":
		err = app.Healthcheck(ctx, cfg)
	default:
		err = errors.New("usage: api [serve|{% if cookiecutter.use_postgres == "yes" %}migrate up|down|status|{% endif %}healthcheck]")
	}
	if err != nil {
		logger.Error("exit", "command", command, "error", err.Error())
		return 1
	}
	return 0
}
