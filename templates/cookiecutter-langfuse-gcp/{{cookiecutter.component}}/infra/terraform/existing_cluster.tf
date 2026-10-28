# cluster_mode = "existing": Langfuse installed into a GKE cluster this configuration does not own.
#
# langfuse-terraform-gcp 1.0.10 cannot do this: gke.tf creates the cluster unconditionally, and
# vpc.tf the network it sits in, with no variable to skip either. So this file creates what the
# module creates *inside and beside* a cluster, and nothing else — no VPC, no NAT, no cluster. Every
# resource names the module file and line it mirrors; when the module is bumped, diff those files
# and carry their changes here in the same pull request (README, "Upgrading").
#
# What the cluster must already have is checked before anything is planned
# (terraform_data.existing_cluster_requirements below), and listed in the README, "An existing
# cluster".

locals {
  existing = var.cluster_mode == "existing"

  # The cluster's network as `projects/<project>/global/networks/<name>`. A Shared VPC cluster
  # names its host project there, which is the project Cloud SQL and Memorystore peer with.
  existing_network_parts = local.existing ? regex(
    "projects/([^/]+)/global/networks/([^/]+)$",
    one(data.google_container_cluster.existing[*].network),
  ) : []
}

data "google_container_cluster" "existing" {
  count    = local.existing ? 1 : 0
  name     = var.existing_cluster_name
  location = var.existing_cluster_location
}

data "google_compute_network" "existing" {
  count   = local.existing ? 1 : 0
  project = local.existing_network_parts[0]
  name    = local.existing_network_parts[1]
}

# The requirements, as preconditions, so a cluster that cannot run this fails at plan with a named
# reason rather than half way through an apply.
resource "terraform_data" "existing_cluster_requirements" {
  count = local.existing ? 1 : 0

  lifecycle {
    precondition {
      condition     = try(data.google_container_cluster.existing[0].workload_identity_config[0].workload_pool, "") == "${var.project_id}.svc.id.goog"
      error_message = "The existing cluster must have Workload Identity Federation for GKE enabled with the pool ${var.project_id}.svc.id.goog: the Langfuse pods reach the bucket as a Google service account through it (module langfuse.tf:167)."
    }
    precondition {
      condition     = length(try(data.google_container_cluster.existing[0].ip_allocation_policy, [])) > 0
      error_message = "The existing cluster must be VPC-native (alias IPs): Cloud SQL and Memorystore are reached on private IPs over private services access."
    }
    precondition {
      condition     = !try(data.google_container_cluster.existing[0].addons_config[0].http_load_balancing[0].disabled, false)
      error_message = "The existing cluster must have the HTTP load balancing add-on enabled: Langfuse is exposed through a GKE Ingress (class gce)."
    }
    precondition {
      condition = (
        tonumber(split(".", data.google_container_cluster.existing[0].master_version)[0]) > 1 ||
        tonumber(split(".", data.google_container_cluster.existing[0].master_version)[1]) >= 30
      )
      error_message = "The existing cluster must run GKE 1.30 or later."
    }
  }
}

# A cluster with only a private endpoint plans and applies from a machine inside its network, and
# from nowhere else — not from a GitHub-hosted runner. Warn rather than fail: a self-hosted runner
# in that network is a legitimate answer.
check "existing_cluster_endpoint_is_reachable_from_ci" {
  assert {
    condition     = !local.existing || !try(data.google_container_cluster.existing[0].private_cluster_config[0].enable_private_endpoint, false)
    error_message = "The existing cluster has a private endpoint only. GitHub-hosted runners cannot reach it, so the infra workflow's plan and apply will fail; run them on a runner inside the cluster's network."
  }
}

# --- Cloud SQL: module postgres.tf:1-68 ------------------------------------------------------------

resource "random_password" "existing_postgres" {
  # postgres.tf:62
  count       = local.existing ? 1 : 0
  length      = 64
  special     = false
  min_lower   = 1
  min_upper   = 1
  min_numeric = 1
}

resource "google_sql_database_instance" "existing" {
  # postgres.tf:1. Private IP only, on the cluster's network: that network needs private services
  # access already configured (the module creates it in vpc.tf:61-74; here it is a requirement).
  count            = local.existing ? 1 : 0
  name             = var.name
  region           = var.region
  database_version = "POSTGRES_16"

  settings {
    tier                        = local.size.database_instance_tier
    edition                     = local.size.database_instance_edition
    availability_type           = local.size.database_availability_type
    deletion_protection_enabled = var.deletion_protection

    backup_configuration {
      enabled                        = true
      point_in_time_recovery_enabled = true
      # postgres.tf:19: 14 days on ENTERPRISE_PLUS, 7 (the maximum) on ENTERPRISE.
      transaction_log_retention_days = local.size.database_instance_edition == "ENTERPRISE_PLUS" ? 14 : 7
    }

    ip_configuration {
      ipv4_enabled                                  = false
      private_network                               = data.google_compute_network.existing[0].id
      enable_private_path_for_google_cloud_services = true
      ssl_mode                                      = "ENCRYPTED_ONLY"
    }
  }

  deletion_protection = var.deletion_protection

  depends_on = [terraform_data.existing_cluster_requirements]
}

# postgres.tf:50 and :55, plus deletion_policy = "ABANDON", which the module does not set. A destroy
# otherwise drops the database while the Langfuse pods still hold connections ("database langfuse
# is being accessed by other users") and the role while it still owns the schema ("role langfuse
# cannot be dropped because some objects depend on it"), and stops half way. Neither is ever
# removed without the instance, whose deletion takes both with it; the provider documents ABANDON
# for exactly this Postgres case.
resource "google_sql_database" "existing" {
  count           = local.existing ? 1 : 0
  name            = "langfuse"
  instance        = google_sql_database_instance.existing[0].name
  deletion_policy = "ABANDON"
}

resource "google_sql_user" "existing" {
  count           = local.existing ? 1 : 0
  name            = "langfuse"
  instance        = google_sql_database_instance.existing[0].name
  password        = random_password.existing_postgres[0].result
  deletion_policy = "ABANDON"
}

# --- Memorystore: module redis.tf:1-18 -------------------------------------------------------------

resource "google_redis_instance" "existing" {
  count                   = local.existing ? 1 : 0
  name                    = var.name
  tier                    = local.size.cache_tier
  memory_size_gb          = local.size.cache_memory_size_gb
  region                  = var.region
  authorized_network      = data.google_compute_network.existing[0].id
  connect_mode            = "PRIVATE_SERVICE_ACCESS"
  transit_encryption_mode = "SERVER_AUTHENTICATION"
  display_name            = "Langfuse ${var.name} Redis Instance"
  auth_enabled            = true

  redis_configs = {
    "maxmemory-policy" = "noeviction"
  }

  depends_on = [terraform_data.existing_cluster_requirements]
}

# --- Bucket: module storage.tf:1-30 ----------------------------------------------------------------

resource "google_storage_bucket" "existing" {
  count                       = local.existing ? 1 : 0
  name                        = "${replace(var.hostname, ".", "-")}-${var.name}"
  location                    = var.region
  force_destroy               = !var.deletion_protection
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  versioning {
    enabled = true
  }
}

resource "google_storage_bucket_iam_member" "existing_langfuse" {
  # storage.tf:19 grants both roles with an authoritative binding; a member per role here, so a
  # grant someone else adds on this bucket is not removed by the next apply.
  for_each = local.existing ? toset(["storage.admin", "storage.objectAdmin"]) : toset([])
  bucket   = google_storage_bucket.existing[0].name
  role     = "roles/${each.value}"
  member   = "serviceAccount:${google_service_account.existing_langfuse[0].email}"
}

# --- Identity the pods run as: module langfuse.tf:162-186 ------------------------------------------

resource "google_service_account" "existing_langfuse" {
  count        = local.existing ? 1 : 0
  account_id   = var.name
  display_name = "Langfuse ${var.name}"
}

resource "google_service_account_iam_member" "existing_workload_identity" {
  # langfuse.tf:167. The chart's service account is named after the release, `langfuse`.
  count              = local.existing ? 1 : 0
  service_account_id = google_service_account.existing_langfuse[0].name
  role               = "roles/iam.workloadIdentityUser"
  member             = "serviceAccount:${var.project_id}.svc.id.goog[${kubernetes_namespace.existing[0].metadata[0].name}/langfuse]"
}

resource "google_service_account_iam_member" "existing_token_creator" {
  # langfuse.tf:176: GCS signed URLs for media uploads are signed through the IAM API as itself.
  count              = local.existing ? 1 : 0
  service_account_id = google_service_account.existing_langfuse[0].name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${google_service_account.existing_langfuse[0].email}"
}

# The namespace this component owns. Nothing else should be deployed into it: a teardown deletes it.
resource "kubernetes_namespace" "existing" {
  # langfuse.tf:182
  count = local.existing ? 1 : 0
  metadata {
    name = var.name
  }

  depends_on = [terraform_data.existing_cluster_requirements]
}

# --- Generated application secrets: module langfuse.tf:188-219, clickhouse.tf:2 --------------------
#
# These are in Terraform state, exactly as they are in the module's. That is why the state bucket is
# CMEK-encrypted, versioned and readable only by the operators and the two CI identities.

resource "random_bytes" "existing_salt" {
  count  = local.existing ? 1 : 0
  length = 32
}

resource "random_bytes" "existing_nextauth_secret" {
  count  = local.existing ? 1 : 0
  length = 32
}

resource "random_bytes" "existing_encryption_key" {
  count  = local.existing ? 1 : 0
  length = 32
}

resource "random_password" "existing_clickhouse" {
  count       = local.existing ? 1 : 0
  length      = 64
  special     = false
  min_lower   = 1
  min_upper   = 1
  min_numeric = 1
}

resource "kubernetes_secret" "existing" {
  count = local.existing ? 1 : 0
  metadata {
    name      = "langfuse"
    namespace = kubernetes_namespace.existing[0].metadata[0].name
  }

  data = {
    "redis-password"      = google_redis_instance.existing[0].auth_string
    "redis-certificate"   = google_redis_instance.existing[0].server_ca_certs[0].cert
    "postgres-password"   = random_password.existing_postgres[0].result
    "salt"                = random_bytes.existing_salt[0].base64
    "nextauth-secret"     = random_bytes.existing_nextauth_secret[0].base64
    "clickhouse-password" = random_password.existing_clickhouse[0].result
    "encryption_key"      = random_bytes.existing_encryption_key[0].hex
  }
}

# --- cert-manager and the ClickHouse operator: module clickhouse.tf:17-77 ---------------------------

resource "helm_release" "existing_cert_manager" {
  count            = local.existing && var.install_cert_manager ? 1 : 0
  name             = "cert-manager"
  repository       = "https://charts.jetstack.io"
  chart            = "cert-manager"
  version          = "v1.20.2" # the module's cert_manager_chart_version default
  namespace        = "cert-manager"
  create_namespace = true
  timeout          = 600

  # clickhouse.tf:31-51: CRDs on, leader election out of kube-system (Autopilot denies leases
  # there), and small requests so Autopilot does not apply its 500m / 2 GiB defaults.
  values = [yamlencode({
    crds       = { enabled = true }
    global     = { leaderElection = { namespace = "cert-manager" } }
    resources  = { requests = { cpu = "50m", memory = "128Mi" } }
    webhook    = { resources = { requests = { cpu = "50m", memory = "128Mi" } } }
    cainjector = { resources = { requests = { cpu = "50m", memory = "128Mi" } } }
  })]

  depends_on = [terraform_data.existing_cluster_requirements]
}

resource "helm_release" "existing_clickhouse_operator" {
  count            = local.existing && var.install_clickhouse_operator ? 1 : 0
  name             = "clickhouse-operator"
  repository       = "oci://ghcr.io/clickhouse"
  chart            = "clickhouse-operator-helm"
  version          = "0.0.5" # the module's clickhouse_operator_chart_version default
  namespace        = "clickhouse-operator-system"
  create_namespace = true
  timeout          = 600

  depends_on = [helm_release.existing_cert_manager, terraform_data.existing_cluster_requirements]
}

# --- TLS and the HTTPS redirect: module tls.tf:2-39 -------------------------------------------------

resource "google_compute_managed_ssl_certificate" "existing" {
  count = local.existing ? 1 : 0
  name  = var.name

  managed {
    domains = [var.hostname]
  }
}

resource "kubernetes_manifest" "existing_https_redirect" {
  count = local.existing ? 1 : 0
  manifest = {
    apiVersion = "networking.gke.io/v1beta1"
    kind       = "FrontendConfig"
    metadata = {
      name      = "https-redirect"
      namespace = kubernetes_namespace.existing[0].metadata[0].name
    }
    spec = {
      redirectToHttps = {
        enabled          = "true"
        responseCodeName = "PERMANENT_REDIRECT"
      }
    }
  }
}

# vpc.tf:76: a reserved address for the Ingress when DNS is an A record added outside Cloud DNS.
resource "google_compute_global_address" "existing_ingress" {
  count = local.existing && !var.create_dns_zone ? 1 : 0
  name  = "${var.name}-ingress-ip"
}

# --- The Langfuse chart: module langfuse.tf:1-158 and :221-246 --------------------------------------
#
# The same values the module renders, written as data rather than a heredoc: langfuse_values
# (langfuse.tf:2-81), clickhouse_internal_values (:86-109), ingress_values (:132-151) and
# encryption_values (:152-158).

locals {
  existing_secret = "langfuse"

  existing_helm_values = local.existing ? [yamlencode({
    langfuse = {
      image = { tag = local.langfuse_app_version }
      salt  = { secretKeyRef = { name = local.existing_secret, key = "salt" } }
      nextauth = {
        url    = "https://${var.hostname}"
        secret = { secretKeyRef = { name = local.existing_secret, key = "nextauth-secret" } }
      }
      encryptionKey = { secretKeyRef = { name = local.existing_secret, key = "encryption_key" } }
      serviceAccount = {
        annotations = { "iam.gke.io/gcp-service-account" = google_service_account.existing_langfuse[0].email }
      }
      additionalEnv = concat(
        [{ name = "LANGFUSE_USE_GOOGLE_CLOUD_STORAGE", value = "true" }],
        [for env in local.additional_env : { for k, v in env : k => v if v != null }],
      )
      extraVolumeMounts = [{ name = "redis-certificate", mountPath = "/var/run/secrets/", readOnly = true }]
      extraVolumes = [{
        name   = "redis-certificate"
        secret = { secretName = local.existing_secret, items = [{ key = "redis-certificate", path = "redis-ca.crt" }] }
      }]
      ingress = {
        enabled   = true
        className = "gce"
        annotations = merge(
          {
            "kubernetes.io/ingress.class"               = "gce"
            "ingress.gcp.kubernetes.io/pre-shared-cert" = google_compute_managed_ssl_certificate.existing[0].name
            "networking.gke.io/v1beta1.FrontendConfig"  = "https-redirect"
          },
          var.create_dns_zone ? {} : {
            "kubernetes.io/ingress.global-static-ip-name" = google_compute_global_address.existing_ingress[0].name
          },
        )
        hosts = [{ host = var.hostname, paths = [{ path = "/", pathType = "Prefix" }] }]
      }
      securityContext = { allowPrivilegeEscalation = false }
    }
    postgresql = {
      deploy = false
      host   = google_sql_database_instance.existing[0].private_ip_address
      auth = {
        username       = "langfuse"
        database       = "langfuse"
        existingSecret = local.existing_secret
        secretKeys     = { userPasswordKey = "postgres-password" }
      }
    }
    redis = {
      deploy = false
      host   = google_redis_instance.existing[0].host
      port   = google_redis_instance.existing[0].port
      tls    = { enabled = true, caPath = "/var/run/secrets/redis-ca.crt" }
      auth   = { existingSecret = local.existing_secret, existingSecretPasswordKey = "redis-password" }
    }
    s3 = {
      deploy          = false
      storageProvider = "gcs"
      bucket          = google_storage_bucket.existing[0].name
      eventUpload     = { prefix = "events/" }
      batchExport     = { prefix = "exports/" }
      mediaUpload     = { prefix = "media/" }
    }
    clickhouse = {
      deploy = true
      auth   = { existingSecret = local.existing_secret, existingSecretKey = "clickhouse-password" }
      cluster = {
        replicas = local.size.clickhouse_replicas
        storage  = { size = local.size.clickhouse_storage_size, className = local.size.clickhouse_storage_class }
        resources = {
          requests = local.size.clickhouse_resources
          limits   = local.size.clickhouse_resources
        }
      }
      keeper = {
        replicas = local.size.clickhouse_keeper_replicas
        storage  = { size = local.size.clickhouse_keeper_storage_size, className = local.size.clickhouse_storage_class }
      }
    }
  })] : []
}

resource "helm_release" "existing_langfuse" {
  count      = local.existing ? 1 : 0
  name       = "langfuse"
  repository = "https://langfuse.github.io/langfuse-k8s"
  version    = local.langfuse_chart_version
  chart      = "langfuse"
  namespace  = kubernetes_namespace.existing[0].metadata[0].name
  timeout    = 1800

  values = concat(local.existing_helm_values, local.extra_helm_values)

  depends_on = [
    kubernetes_secret.existing,
    kubernetes_manifest.existing_https_redirect,
    helm_release.existing_clickhouse_operator,
    google_service_account_iam_member.existing_workload_identity,
  ]
}

# --- DNS: module dns.tf:1-30 ------------------------------------------------------------------------

resource "google_dns_managed_zone" "existing" {
  count       = local.existing && var.create_dns_zone ? 1 : 0
  name        = replace(var.hostname, ".", "-")
  dns_name    = "${var.hostname}."
  description = "DNS zone for Langfuse domain"
}

data "kubernetes_ingress_v1" "existing" {
  count = local.existing ? 1 : 0
  metadata {
    name      = "langfuse"
    namespace = kubernetes_namespace.existing[0].metadata[0].name
  }

  depends_on = [helm_release.existing_langfuse]
}

resource "google_dns_record_set" "existing" {
  count        = local.existing && var.create_dns_zone ? 1 : 0
  name         = google_dns_managed_zone.existing[0].dns_name
  managed_zone = google_dns_managed_zone.existing[0].name
  type         = "A"
  ttl          = 300
  rrdatas      = [data.kubernetes_ingress_v1.existing[0].status[0].load_balancer[0].ingress[0].ip]
}
