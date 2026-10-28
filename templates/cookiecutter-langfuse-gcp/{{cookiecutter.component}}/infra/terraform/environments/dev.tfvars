# The dev environment. Every plan and apply reads this file (`-var-file`), in CI and on a laptop.
# Nothing secret belongs here: this file is committed, and every value lands in state and plans.
# A second environment is a copy of this file and of dev.backend.hcl, in its own GCP project.

project_id        = "{{ cookiecutter.gcp_project_id }}"
region            = "{{ cookiecutter.gcp_region }}"
environment       = "dev"
name              = "{{ cookiecutter.component }}"
github_repository = "{{ cookiecutter.github_repository }}"

# Must match `bucket` in dev.backend.hcl; infra/bootstrap/state-bucket.sh keeps the two in step.
state_bucket = "{{ cookiecutter.state_bucket }}"

# The hostname is yours to choose and to delegate: after the first apply, add NS records for it at
# your registrar pointing at the `dns_name_servers` output (README, "DNS"). Changing it later
# replaces the certificate, the zone and the bucket's name.
hostname = "{{ cookiecutter.hostname }}"

profile = "{{ cookiecutter.profile }}"

cluster_mode = "{{ cookiecutter.cluster_mode }}"
{%- if cookiecutter.cluster_mode == "existing" %}

# The cluster to install into, which must meet the README's "An existing cluster" requirements.
existing_cluster_name     = "{{ cookiecutter.existing_cluster_name }}"
existing_cluster_location = "{{ cookiecutter.existing_cluster_location }}"
# false when the cluster already runs cert-manager, or the ClickHouse operator (another Langfuse
# in the same cluster runs both).
install_cert_manager        = true
install_clickhouse_operator = true
{%- endif %}

# true: a Cloud DNS zone for the hostname, delegated from your registrar (the official guide's
# path). false: no zone; a reserved IP you point an A record at yourself.
create_dns_zone = true

# Turn off before a teardown, in a reviewed pull request, and never otherwise.
deletion_protection = true

# true until the first person has signed up, then false (README, "First sign-in").
allow_signup = true

# Sign in with Google (README, "Single sign-on"). Create the OAuth client and the `langfuse-sso`
# Kubernetes secret first; enabling this before the secret exists leaves the pods unable to start.
google_sso = {
  enabled = false
  # allowed_domains        = "example.com"
  # disable_password_login = true
}
