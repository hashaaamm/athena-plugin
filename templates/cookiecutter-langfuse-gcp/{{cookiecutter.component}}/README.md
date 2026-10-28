# {{ cookiecutter.project_name }} — self-hosted Langfuse on GCP

[Langfuse](https://langfuse.com) v4, the open-source LLM observability platform, deployed into GCP
project `{{ cookiecutter.gcp_project_id }}` with **Terraform and Langfuse's own module**,
[`langfuse/langfuse-terraform-gcp`](https://github.com/langfuse/langfuse-terraform-gcp), the way
[Langfuse's GCP deployment guide](https://langfuse.com/self-hosting/deployment/gcp) does it:
GKE Autopilot, Cloud SQL for PostgreSQL, Memorystore for Redis, a Cloud Storage bucket, ClickHouse
in the cluster under the official ClickHouse operator, and the official Helm chart. Served on
`https://{{ cookiecutter.hostname }}`.

{% if cookiecutter.cluster_mode == "new" -%}
This component was generated with `cluster_mode = "new"`: the module creates the cluster and its
network.
{%- else -%}
This component was generated with `cluster_mode = "existing"`: Langfuse is installed into the
cluster `{{ cookiecutter.existing_cluster_name }}` ({{ cookiecutter.existing_cluster_location }}),
which this component does not own. Read [An existing cluster](#an-existing-cluster) before the
first apply.
{%- endif %}

**When something does not work, read upstream first, in this order:**

1. the module's own README and `variables.tf` **at the pinned version**, 1.0.10 —
   <https://github.com/langfuse/langfuse-terraform-gcp/tree/1.0.10>;
2. Langfuse's GCP deployment guide — <https://langfuse.com/self-hosting/deployment/gcp> — and the
   self-hosting docs it links;
3. the Helm chart's docs at the pinned chart version, 2.1.7 —
   <https://github.com/langfuse/langfuse-k8s>, including its `TROUBLESHOOTING.md`.

The rest of this file covers what this component adds around the module: the state bucket, the CI
identities, the existing-cluster path, and the order things have to happen in.

## What it costs

Estimates for `us-central1` list prices on 2026-10-28, from the Cloud Billing catalog, 730 hours a
month, before any committed-use discount and before traffic. Other regions differ by up to ~30%.
Langfuse's own pods (web, worker) run with the chart's empty resource requests, so Autopilot gives
each its default of 0.5 vCPU and 2 GiB.

| | `small` (default) | `ha` |
| --- | --- | --- |
| ClickHouse | 1 replica, 1 vCPU / 4 GiB, 50 GiB pd-balanced; 1 Keeper | 3 replicas, 2 vCPU / 8 GiB, 100 GiB pd-ssd each; 3 Keepers |
| Cloud SQL | Enterprise, `db-custom-1-3840`, zonal, PITR 7 days | Enterprise Plus, `db-perf-optimized-N-2`, regional, PITR 14 days |
| Memorystore | Basic, 1 GB | Standard HA, 1 GB |
| Langfuse web / worker | 1 + 1 | 2 + 2 |
| GKE Autopilot pods | ~$130 | ~$420 |
| Cloud SQL | ~$52 | ~$373 |
| Memorystore | ~$36 | ~$47 |
| Persistent disks | ~$6 | ~$56 |
| Cluster fee | $73 (offset by the $74.40/month GKE free tier on a billing account's first cluster) | $73 |
| Load balancer, Cloud NAT, DNS | ~$30 | ~$35 |
| **Roughly** | **$250–330 / month** | **~$1,000 / month** |

In `cluster_mode = "existing"` there is no cluster fee and no NAT of this component's own; the pods
bill to the existing cluster. The module's VPC flow logs (50% sampling) also bill as Cloud Logging
ingestion beyond the free allowance.

## Layout

```
{{ cookiecutter.component }}/
├── justfile                       every command below
├── infra/
│   ├── bootstrap/state-bucket.sh  APIs, and the state bucket and its KMS key — created or checked
│   ├── scripts/                   tf.sh (Terraform natively or in the pinned image), up.sh (first
│   │                              apply), sync-github.sh, teardown.sh
│   └── terraform/                 the root module
│       ├── new_cluster.tf         cluster_mode "new": the official module
│       ├── existing_cluster.tf    cluster_mode "existing": what the module creates, minus the cluster
│       ├── identities.tf          the plan and apply identities, Workload Identity Federation
│       ├── locals.tf              the two profiles; the Langfuse and chart versions
│       ├── environments/          dev.tfvars, dev.backend.hcl — one pair per environment
│       ├── tests/                 plan tests against mocked providers: no credentials
│       └── .terraform.lock.hcl    provider versions and hashes; committed
.github/workflows/{{ cookiecutter.component }}-infra.yml   static checks, plan on PR, apply on merge
.github/workflows/{{ cookiecutter.component }}-ci.yml      secret scan over the full history
```

## First deploy

Everything up to step 6 runs once per environment, from an operator's machine, as someone who can
administer the project (`gcloud auth login` and `gcloud auth application-default login`). It costs
money from step 4. Every apply after step 6 runs from CI.

```bash
cd {{ cookiecutter.component }}
just lint                 # 0. no credentials: fmt, validate, the plan tests, tflint, shellcheck, actionlint
just bootstrap            # 1. APIs, the state bucket and its KMS key (see "The state bucket")
just init                 # 2. points Terraform at the bucket, prefix {{ cookiecutter.component }}/dev
just plan                 # 3. read it
just up                   # 4. the first apply; in "new" mode, two applies — see below
                          # 5. DNS — see "DNS"
just sync-github          # 6. repository variables: CI plans and applies from here on
```

Then, in the repository's **Settings → Environments → `infrastructure`**: add required reviewers and
limit deployment branches to `main`. Only a job in that environment can become the apply identity,
so those two settings are the review gate on every infrastructure change.

**Why `just up` is two applies in `new` mode.** The Kubernetes and Helm providers are configured from
the cluster the module creates, so nothing inside the cluster can be planned before the cluster
exists — the module's README calls the targeted first apply "the supported installation flow, not a
workaround". `up.sh` applies the cluster and the DNS zone (or the reserved IP) first, then everything.

**Why setup runs from a laptop at all.** The identity CI applies as is created by the first apply,
so the first apply cannot run in CI. Once `just sync-github` has set `{{ cookiecutter.__component_var }}_APPLY_SA`,
`just up` refuses to run; `--break-glass` overrides it, and a break-glass apply is recorded and
followed by a CI plan that shows no changes.

## The state bucket

Terraform state holds **every secret Langfuse runs with**, in cleartext: the module generates the
database and ClickHouse passwords, `SALT`, `NEXTAUTH_SECRET` and `ENCRYPTION_KEY` with
`random_password` and `random_bytes`, writes them into a Kubernetes secret, and reads back
Memorystore's `auth_string`. The module offers no way to supply them out of band at 1.0.10, so the
state bucket is where they are protected:

- **CMEK**: a default Cloud KMS key, `terraform-state/state`, in the bucket's location;
- **versioning**, so a corrupted state has a way back;
- **uniform bucket-level access** and **public access prevention enforced**;
- **read only by** the operators, the plan identity (`roles/storage.objectViewer` on the bucket) and
  the apply identity (`roles/storage.objectAdmin`). The bootstrap prints the bucket's policy;
  anyone with a project-level storage role can also read it, so keep that list short too.

Saved plan files hold the same values. CI never uploads one: the pull request gets the rendered
plan, where provider-marked values print as `(sensitive)`, and the apply job plans and applies in
one job.

**A new bucket** is the default: `{{ cookiecutter.state_bucket }}`, created by `just bootstrap`.

**An existing bucket**, shared with other components: each keeps its state under its own prefix
(`<component>/<env>`), so one bucket serves them all.

```bash
just bootstrap dev --bucket my-shared-tf-state           # check it; write its name into
                                                         # dev.backend.hcl and dev.tfvars
just bootstrap dev --bucket my-shared-tf-state --harden  # also fix what the check found
```

Without `--harden` an existing bucket is only checked, never changed: missing versioning or public
access prevention fails the bootstrap; missing uniform access or CMEK is a warning. `--harden` turns
on what is missing, including a CMEK default key, which applies to objects written from then on.
Commit the two files `--bucket` rewrote.

## An existing cluster

`cluster_mode = "existing"` installs Langfuse into a cluster this component does not own. The
official module cannot do that at 1.0.10 — it creates the cluster unconditionally — so
`existing_cluster.tf` creates what the module creates inside and beside a cluster, and nothing else.
Each resource there names the module file and line it mirrors; a module bump updates both.

The cluster must have, and the plan checks the first four before anything is created:

| Requirement | Why |
| --- | --- |
| **Workload Identity Federation for GKE**, pool `<project>.svc.id.goog` — so the cluster is in this project | the pods reach the bucket as a Google service account |
| **VPC-native** networking (alias IPs) | Cloud SQL and Memorystore are reached on private IPs |
| **HTTP load balancing** add-on (always on in Autopilot) | Langfuse is exposed through a GKE Ingress, class `gce` |
| **GKE 1.30 or later**, Autopilot or Standard | the versions this was written against; Langfuse's chart declares no `kubeVersion` |
| **private services access** on the cluster's network: a reserved range and a `servicenetworking` peering | Cloud SQL (private IP only) and Memorystore (`PRIVATE_SERVICE_ACCESS`) attach through it; the module creates it with its own VPC, this path does not |
| **outbound internet** from the nodes (Cloud NAT, or public nodes) | images from Docker Hub and ghcr.io, Helm charts |
| **a public control-plane endpoint**, or a runner inside the network | GitHub-hosted runners must reach the API server to plan and apply; the plan warns otherwise |
| **cert-manager**: none, or set `install_cert_manager = false` | the ClickHouse operator's webhooks need it; two installations fight over its CRDs |

The component owns the namespace `{{ cookiecutter.component }}` — nothing else should be deployed in it — and, if
it installed them, the `cert-manager` and `clickhouse-operator-system` releases. TLS is a
Google-managed certificate on the Ingress; DNS is as below.

## DNS

The managed certificate is issued only once `{{ cookiecutter.hostname }}` resolves to the load balancer,
15–60 minutes after DNS is in place (`gcloud compute ssl-certificates list` shows `PROVISIONING`
until then, and browsers show `ERR_SSL_VERSION_OR_CIPHER_MISMATCH`).

- **`create_dns_zone = true`** (the default, and the official guide's path): Terraform creates a
  Cloud DNS zone for the hostname and its A record. You delegate: at the registrar or in the parent
  zone, add **NS records** for `{{ cookiecutter.hostname }}` with the values of `just output` →
  `dns_name_servers`.
- **`create_dns_zone = false`**: no zone. A global IP is reserved instead; add an **A record** for
  the hostname pointing at `ingress_ip` wherever your DNS lives.

`hostname` is a placeholder until you set it. Changing it later replaces the certificate, the zone
and the Langfuse bucket, whose name is derived from it.

## First sign-in

Anyone who reaches the hostname can create an account while `allow_signup = true`, which it is for
the first deploy so that the first person can. Sign up, then set `allow_signup = false` in
`environments/dev.tfvars` in a pull request — the first change CI applies. Invite everyone else from
Langfuse's organisation settings, or use single sign-on.

## Single sign-on

Langfuse supports Google, Okta, Microsoft Entra ID, Auth0, GitHub, GitLab, Keycloak and generic
OIDC, among others, configured through environment variables
(<https://langfuse.com/self-hosting/security/authentication-and-sso>). This component wires
**Google**; another provider is the same shape, added to `locals.tf`.

1. **Console step — Terraform cannot do this.** In the Google Cloud console, *APIs & Services →
   Credentials → Create credentials → OAuth client ID*, type *Web application*, authorised redirect
   URI `https://{{ cookiecutter.hostname }}/api/auth/callback/google`. Configure the consent screen first if
   asked (*Internal* for a Workspace organisation).
2. Put the client id and secret in the cluster, never in a file:

   ```bash
   gcloud container clusters get-credentials "$(just output | sed -n 's/^cluster_name = "\(.*\)"/\1/p')" \
     --location {% if cookiecutter.cluster_mode == "new" %}{{ cookiecutter.gcp_region }}{% else %}{{ cookiecutter.existing_cluster_location }}{% endif %} --project {{ cookiecutter.gcp_project_id }}
   kubectl -n {{ cookiecutter.component }} create secret generic langfuse-sso \
     --from-literal=google-client-id='<client id>' \
     --from-literal=google-client-secret='<client secret>'
   ```

3. In `environments/dev.tfvars`, in a pull request:

   ```hcl
   google_sso = {
     enabled         = true
     allowed_domains = "example.com"   # who may sign in with Google
     # disable_password_login = true   # once Google sign-in works
   }
   ```

Terraform references the secret by name, so the client secret never enters state.

## Sending traces to it

Langfuse accepts OpenTelemetry over **OTLP/HTTP** (HTTP/protobuf or HTTP/JSON; not gRPC):

- endpoint: `https://{{ cookiecutter.hostname }}/api/public/otel` — an exporter that takes a signal-specific
  URL wants `https://{{ cookiecutter.hostname }}/api/public/otel/v1/traces` (`just output` → `otel_traces_endpoint`);
- auth: `Authorization: Basic <base64 of "<public key>:<secret key>">`, from a Langfuse project's
  API keys; Langfuse's docs also send `x-langfuse-ingestion-version: 4`.

```bash
OTEL_EXPORTER_OTLP_ENDPOINT="https://{{ cookiecutter.hostname }}/api/public/otel"
OTEL_EXPORTER_OTLP_HEADERS="Authorization=Basic $(printf '%s' "$LANGFUSE_PUBLIC_KEY:$LANGFUSE_SECRET_KEY" | base64)"
```

The Langfuse SDKs need only `LANGFUSE_HOST=https://{{ cookiecutter.hostname }}` and the two keys. Keep the keys
in the calling service's secret store. Source: <https://langfuse.com/integrations/native/opentelemetry>.

## Upgrading

Three versions move, each in its own pull request whose plan is read before it merges:

| What | Where | Pinned |
| --- | --- | --- |
| the module | `source = "…?ref=<tag>"` in `new_cluster.tf` | 1.0.10 |
| the Helm chart | `langfuse_chart_version` in `locals.tf` | 2.1.7 |
| Langfuse | `langfuse_app_version` in `locals.tf` | 4.70.1 |

- **Langfuse within v4**: bump `langfuse_app_version`. Migrations run on start ("Updates within a
  major version are designed to be non-disruptive", <https://langfuse.com/self-hosting/upgrade>).
  Upgrade the deployment before the SDKs: Python SDK v5 and JS SDK v6 need Langfuse 4.59.0 or later.
- **The module**: read its release notes and diff `variables.tf` and the files `existing_cluster.tf`
  cites between the two tags; carry the changes into `existing_cluster.tf` in the same pull request.
  Take the chart and app versions the new module defaults to unless you have a reason not to.
- **The chart**: follow the chart's `UPGRADE.md` for a major. ClickHouse's server version comes from
  the chart; upgrade the chart, not ClickHouse on its own.
- **A Langfuse major** (v4 → v5): follow its upgrade guide, before any of the above.
- **Providers**: `just lock` after changing a constraint in `terraform.tf`; commit the lock file. The
  module bounds google to 6.x and helm and kubernetes to 2.x.
- **GKE**: Autopilot upgrades itself within its release channel. A Standard cluster in `existing`
  mode is its owner's to upgrade.

## Never change these

- **`ENCRYPTION_KEY`** encrypts what Langfuse stores encrypted, such as LLM API keys entered in the
  UI. A new one leaves every stored value unreadable.
- **`SALT`** salts the hashes of Langfuse API keys. A new one makes every issued API key invalid.

Both are `random_bytes` in state. Anything that replaces them — `terraform apply -replace`, deleting
them from state, renaming the resources without a `moved` block, changing the module `name` —
breaks a running installation. A plan that shows either being replaced is a plan to stop and read.

## Backups

- **Cloud SQL**: automated daily backups and point-in-time recovery, on in both profiles.
- **ClickHouse** holds the traces and has **no backup configured** — the module and chart set none.
  `small` has one replica, so losing its volume loses the traces. Schedule persistent-disk
  snapshots of the ClickHouse volumes (a snapshot schedule policy, or VolumeSnapshots), or run
  ClickHouse's `BACKUP` to a bucket, before relying on it.
- **The bucket** of raw events and media is versioned.
- **State** is versioned in the state bucket.

## Teardown

```bash
# 1. In a pull request: deletion_protection = false in environments/dev.tfvars. Merge; CI applies it.
# 2. From your machine:
just teardown dev
```

`teardown.sh` refuses until step 1 has been done, asks for the project id typed out, and runs
`terraform destroy`. It leaves the state bucket: other components may share it. It prints how to
see what else is in the bucket and how to remove only this component's prefix. In `existing` mode
the cluster stays; the namespace and the releases this component installed go.

## Pinned versions

| What | Version |
| --- | --- |
| `langfuse/langfuse-terraform-gcp` | 1.0.10 (commit `55a57e3`, 2026-10-27) |
| Langfuse Helm chart / Langfuse | 2.1.7 / 4.70.1 |
| cert-manager / ClickHouse operator charts | v1.20.2 / 0.0.5 (the module's defaults) |
| Terraform | `~> 1.16`; CI and the image use 1.16.5 |
| providers | google 6.50.0, kubernetes 2.38.0, helm 2.17.0, random 3.9.1, tls 4.4.2 (lock file) |
| tflint / ruleset-google | 0.65.0 / 0.41.0 |
| actionlint / shellcheck / gitleaks | 1.7.12 / 0.11.0 / 8.30.1 |
