# The two identities CI runs as, and the Workload Identity Federation that lets GitHub Actions
# become them without a key.
#
#   plan   read-only. Runs `terraform plan` on a pull request. Impersonable by any workflow of the
#          repository named in var.github_repository — and nothing else, by the provider's
#          attribute condition. It can read state, which holds every generated Langfuse secret,
#          so it is exactly as sensitive as the state bucket; it cannot change anything.
#   apply  the only CI identity that changes infrastructure. Impersonable only by a job running in
#          the repository's `infrastructure` GitHub environment, so that environment's protection
#          rules — required reviewers, main only — gate every apply.
#
# The first apply runs from an operator's machine, because it is the apply that creates these two
# accounts. Every apply after it runs from CI as `apply`.
#
# No account here has a downloadable key, and none is ever created.

locals {
  ci_prefix = "${var.name}-${var.environment}"

  # Read access for a plan. `viewer` reads resource metadata across the project and the GKE objects
  # in the cluster except Secrets; `iam.securityReviewer` reads IAM policies, which `viewer`
  # deliberately excludes and a refresh of every IAM binding below needs. Neither can read a
  # secret's value or change anything.
  plan_roles = toset([
    "roles/viewer",
    "roles/iam.securityReviewer",
  ])

  # What the apply creates, one predefined role per service, never `roles/owner`.
  # `resourcemanager.projectIamAdmin` can grant any role on the project, this account's own
  # included, and it has to — the configuration binds project roles — so in effect this account
  # can do anything in the project. One project per environment is what keeps that to the
  # environment it applies, and the protected GitHub environment is what keeps it to reviewed
  # changes. A resource type the configuration gains is a role added here in the same pull
  # request, or the next apply fails with a permission error.
  apply_roles = toset([
    "roles/container.admin",                   # the Autopilot cluster and everything inside it
    "roles/cloudsql.admin",                    # Cloud SQL
    "roles/redis.admin",                       # Memorystore
    "roles/storage.admin",                     # the Langfuse bucket
    "roles/compute.networkAdmin",              # VPC, subnet, router, NAT, reserved addresses
    "roles/compute.securityAdmin",             # firewall rules, SSL certificates
    "roles/servicenetworking.networksAdmin",   # private services access for Cloud SQL and Redis
    "roles/dns.admin",                         # the Cloud DNS zone and record
    "roles/iam.serviceAccountAdmin",           # the pods' service account, and these two
    "roles/iam.serviceAccountUser",            # Autopilot nodes run as the compute default account
    "roles/iam.workloadIdentityPoolAdmin",     # the pool and provider below
    "roles/resourcemanager.projectIamAdmin",   # the project bindings in this file
    "roles/serviceusage.serviceUsageConsumer", # calling the enabled APIs
  ])

  # The namespaces whose Secrets a plan must read: Helm keeps each release's state in a Secret in
  # the release's namespace, and the module's `kubernetes_secret` is refreshed on every plan.
  plan_secret_namespaces = toset(concat(
    [var.name],
    var.cluster_mode == "new" || var.install_clickhouse_operator ? ["clickhouse-operator-system"] : [],
    var.cluster_mode == "new" || var.install_cert_manager ? ["cert-manager"] : [],
  ))
}

resource "google_service_account" "plan" {
  account_id   = "${local.ci_prefix}-plan"
  display_name = "${var.name} Terraform plan (${var.environment})"
}

resource "google_service_account" "apply" {
  account_id   = "${local.ci_prefix}-apply"
  display_name = "${var.name} Terraform apply (${var.environment})"
}

resource "google_project_iam_member" "plan" {
  for_each = local.plan_roles
  project  = var.project_id
  role     = each.value
  member   = google_service_account.plan.member
}

resource "google_project_iam_member" "apply" {
  for_each = local.apply_roles
  project  = var.project_id
  role     = each.value
  member   = google_service_account.apply.member
}

# State. Granted on the bucket, not the project: a project storage role is every bucket in it.
# The plan reads and never writes — infra.yml plans with `-lock=false` for exactly that reason, so a
# compromised pull request cannot rewrite state. The apply writes state and takes the lock.
resource "google_storage_bucket_iam_member" "plan_reads_state" {
  bucket = var.state_bucket
  role   = "roles/storage.objectViewer"
  member = google_service_account.plan.member
}

resource "google_storage_bucket_iam_member" "apply_writes_state" {
  bucket = var.state_bucket
  role   = "roles/storage.objectAdmin"
  member = google_service_account.apply.member
}

# --- Workload Identity Federation ------------------------------------------------------------------

data "google_project" "this" {}

resource "google_iam_workload_identity_pool" "github" {
  # Unique in the project, not the configuration: two components each asking for `github-dev`
  # collide on the second. A deleted pool's id is reserved for 30 days.
  workload_identity_pool_id = "${local.ci_prefix}-github"
  display_name              = "${var.name} GitHub Actions"
}

resource "google_iam_workload_identity_pool_provider" "github" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "github"
  display_name                       = "GitHub OIDC"

  attribute_mapping = {
    "google.subject"        = "assertion.sub"
    "attribute.repository"  = "assertion.repository"
    "attribute.ref"         = "assertion.ref"
    "attribute.environment" = "assertion.environment"
  }

  # Without this condition any GitHub repository in the world could mint a token for this pool.
  # It is the single most important line in this file.
  attribute_condition = "assertion.repository == \"${var.github_repository}\""

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }
}

locals {
  pool_principal = "principalSet://iam.googleapis.com/projects/${data.google_project.this.number}/locations/global/workloadIdentityPools/${google_iam_workload_identity_pool.github.workload_identity_pool_id}"
}

# Any workflow of the repository may plan.
resource "google_service_account_iam_member" "github_impersonates_plan" {
  service_account_id = google_service_account.plan.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "${local.pool_principal}/attribute.repository/${var.github_repository}"
}

# Only a job in the `infrastructure` environment may apply. The provider's condition already
# limits every token to this repository.
resource "google_service_account_iam_member" "github_impersonates_apply" {
  service_account_id = google_service_account.apply.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "${local.pool_principal}/attribute.environment/infrastructure"
}

# --- Inside the cluster ----------------------------------------------------------------------------
#
# A plan refreshes the Helm releases and the module's Secret, and both are Kubernetes Secrets,
# which `viewer` cannot read. Read on Secrets in this component's namespaces only: in an existing
# cluster the other namespaces' Secrets belong to other teams.
resource "kubernetes_role" "plan_reads_secrets" {
  for_each = local.plan_secret_namespaces

  metadata {
    name      = "${var.name}-plan-reads-secrets"
    namespace = each.value
  }

  rule {
    api_groups = [""]
    resources  = ["secrets"]
    verbs      = ["get", "list", "watch"]
  }

  depends_on = [module.langfuse, helm_release.existing_langfuse, helm_release.existing_cert_manager]
}

resource "kubernetes_role_binding" "plan_reads_secrets" {
  for_each = local.plan_secret_namespaces

  metadata {
    name      = "${var.name}-plan-reads-secrets"
    namespace = each.value
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Role"
    name      = kubernetes_role.plan_reads_secrets[each.key].metadata[0].name
  }

  # GKE maps a Google service account to an RBAC user named by its email.
  subject {
    kind      = "User"
    name      = google_service_account.plan.email
    api_group = "rbac.authorization.k8s.io"
  }
}
