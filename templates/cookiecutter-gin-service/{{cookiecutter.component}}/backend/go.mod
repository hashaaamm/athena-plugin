// Direct dependencies only, each at an explicit version. go.sum is deliberately not templated — a
// committed one would pin whatever existed when the template was last touched — so the first
// command in a new project is `just lock` (`go mod tidy` in a container), which resolves the
// indirect graph and writes go.sum. Commit both. Every image build then runs with -mod=readonly and
// fails on a go.mod that disagrees with go.sum rather than quietly resolving something new.
module {{ cookiecutter.go_module }}

go {{ cookiecutter.go_version }}

require (
{%- if cookiecutter.use_postgres == "yes" %}
	// DATABASE_AUTH=cloud-sql-iam: the Cloud SQL Go connector, which carries the service account's
	// identity to the instance, so the deployed service holds no database password.
	cloud.google.com/go/cloudsqlconn v1.25.4
	// DATABASE_AUTH=rds-iam: the credential chain and the token signer, and nothing else of the SDK.
	github.com/aws/aws-sdk-go-v2 v1.47.4
	github.com/aws/aws-sdk-go-v2/config v1.33.11
	github.com/aws/aws-sdk-go-v2/feature/rds/auth v1.7.7
{%- endif %}
	github.com/caarlos0/env/v11 v11.4.1
	github.com/getkin/kin-openapi v0.152.0
{%- if cookiecutter.use_sentry == "yes" %}
	github.com/getsentry/sentry-go v0.50.0
	github.com/getsentry/sentry-go/gin v0.50.0
{%- endif %}
	github.com/gin-contrib/cors v1.7.9
	github.com/gin-gonic/gin v1.12.0
	github.com/go-playground/validator/v10 v10.30.5
{%- if cookiecutter.use_postgres == "yes" %}
	github.com/golang-jwt/jwt/v5 v5.3.1
{%- endif %}
	github.com/google/uuid v1.6.0
{%- if cookiecutter.use_postgres == "yes" %}
	github.com/jackc/pgx/v5 v5.11.0
	github.com/pressly/goose/v3 v3.29.0
{%- endif %}
	github.com/swaggo/files v1.0.1
	github.com/swaggo/gin-swagger v1.6.1
{%- if cookiecutter.use_postgres == "yes" %}
	golang.org/x/crypto v0.58.0
	golang.org/x/sync v0.24.0
{%- endif %}
)
