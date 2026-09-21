#!/usr/bin/env bash
# The production container's start command. `docker/Dockerfile.prod` ends in
# `CMD ["/app/docker/start.sh"]` and that is the only caller: CD deploys by updating the image
# (`gcloud run services update --image`), the Pulumi service component sets no `commands`, and
# Compose only overrides the command on the *dev* image. Nothing overrides this one, so what is
# written here is what production runs.
#
# It is a file rather than nine backslashed lines inside a JSON array because a start command is
# read far more often than it is written: this one shows up whole in a diff, and a human can run it
# without retyping it.
#
#   docker run --rm -e PORT=9090 -p 9090:9090 <image>   # exactly as the container runs it
#   cd backend && uv run docker/start.sh                 # against the local virtualenv
#
# Three properties here are load-bearing, and each one fails silently in production rather than in
# a test if it is dropped:
#
#   1. `exec`. It replaces this shell with gunicorn, so gunicorn is PID 1 and receives Cloud Run's
#      SIGTERM directly. Without it the shell is PID 1, the signal never reaches the server,
#      in-flight requests are cut off, and Cloud Run kills the container at the end of its grace
#      period instead of draining it. Do not "simplify" this into a bare `gunicorn ...` call.
#   2. `${PORT}` expands at run time. Cloud Run assigns the port and the container MUST listen on
#      it. Dockerfile.prod defaults it to 8080 so `docker run` works with no arguments; this reads
#      whatever the platform actually set. Never hard-code a port here.
#   3. `--graceful-timeout`. In-flight requests get 30 seconds to finish during a revision swap.
#      Removing it turns every deploy into a source of 502s that no test will show you.
#
# Worker count is deliberately absent. Gunicorn reads WEB_CONCURRENCY from the environment
# natively, so the platform sizes the container rather than this file: Dockerfile.prod sets the
# default and infra/pulumi/__main__.py sets the deployed value.
set -euo pipefail

exec gunicorn app.main:app \
    --worker-class uvicorn.workers.UvicornWorker \
    --bind "0.0.0.0:${PORT}" \
    --timeout 60 \
    --graceful-timeout 30 \
    --access-logfile - \
    --error-logfile -
