# The two size profiles, and the Langfuse settings both cluster modes pass to the chart.
#
# Every key in a profile is a variable of langfuse-terraform-gcp 1.0.10 (its variables.tf), and the
# existing-cluster path reads the same keys, so a profile means the same thing in either mode.
locals {
  profiles = {
    # The fewest replicas the chart allows, and the cheapest managed tiers the module accepts.
    #
    # ClickHouse: one replica and one Keeper. The chart says "1 is a valid single-pod cluster", and
    # Keeper "must stay enabled while clickhouse.deploy is true — the ClickHouseCluster CRD requires
    # a keeperClusterRef even for a single replica" (langfuse-k8s 2.1.7 values.yaml); the module
    # accepts Keeper counts of 1, 3 or 5. One replica has no redundancy: losing its volume loses the
    # traces, so this profile leans on the volume's durability and on the backups in the README.
    # 1 vCPU and 4 GiB is above the chart's own request (1 vCPU, 2 GiB, limited at 4 GiB); the
    # module sets requests equal to limits, which is what Autopilot bills.
    #
    # Cloud SQL: ENTERPRISE with a custom 1 vCPU / 3.75 GB machine, zonal. The module's precondition
    # allows any non-`db-perf-optimized-*` tier on ENTERPRISE, and point-in-time recovery keeps 7
    # days there, the edition's maximum, which the module sets for it.
    #
    # Memorystore: BASIC, one node, no replica. A failure or maintenance restart loses the jobs
    # queued at that moment; the raw events stay in the bucket, but nothing replays them on its own.
    small = {
      clickhouse_replicas            = 1
      clickhouse_keeper_replicas     = 1
      clickhouse_resources           = { cpu = "1", memory = "4Gi" }
      clickhouse_storage_size        = "50Gi"
      clickhouse_keeper_storage_size = "10Gi"
      clickhouse_storage_class       = "standard-rwo" # pd-balanced
      database_instance_tier         = "db-custom-1-3840"
      database_instance_edition      = "ENTERPRISE"
      database_availability_type     = "ZONAL"
      cache_tier                     = "BASIC"
      cache_memory_size_gb           = 1
      web_worker_replicas            = 1
    }

    # The module's own defaults, which its README describes as highly available: three ClickHouse
    # replicas and three Keepers on SSD, regional Cloud SQL Enterprise Plus, Standard HA Redis.
    # Two web and two worker pods on top, so a node drain does not take the UI down.
    ha = {
      clickhouse_replicas            = 3
      clickhouse_keeper_replicas     = 3
      clickhouse_resources           = { cpu = "2", memory = "8Gi" }
      clickhouse_storage_size        = "100Gi"
      clickhouse_keeper_storage_size = "10Gi"
      clickhouse_storage_class       = "premium-rwo" # pd-ssd
      database_instance_tier         = "db-perf-optimized-N-2"
      database_instance_edition      = "ENTERPRISE_PLUS"
      database_availability_type     = "REGIONAL"
      cache_tier                     = "STANDARD_HA"
      cache_memory_size_gb           = 1
      web_worker_replicas            = 2
    }
  }

  size = local.profiles[var.profile]

  # The Langfuse application version and chart version. The module pins both as defaults; they are
  # restated here so an upgrade is a reviewed diff in this repository, not a side effect of bumping
  # the module. Upgrade them together with the module, in that order: README, "Upgrading".
  langfuse_chart_version = "2.1.7"
  langfuse_app_version   = "4.70.1"

  # Chart values both modes add on top of the module's own. Web and worker replicas are chart keys
  # (`langfuse.web.replicas`, `langfuse.worker.replicas`); the module has no variable for them.
  extra_helm_values = [
    yamlencode({
      langfuse = {
        web    = { replicas = local.size.web_worker_replicas }
        worker = { replicas = local.size.web_worker_replicas }
      }
    })
  ]

  # Container environment both modes add. Values only — nothing secret: the Google client secret is
  # a reference to a Kubernetes secret a human creates (README, "Single sign-on"), so Terraform never
  # holds it.
  sso_env = var.google_sso.enabled ? [
    {
      name  = "AUTH_GOOGLE_CLIENT_ID"
      value = null
      valueFrom = {
        secretKeyRef = { name = "langfuse-sso", key = "google-client-id" }
      }
    },
    {
      name  = "AUTH_GOOGLE_CLIENT_SECRET"
      value = null
      valueFrom = {
        secretKeyRef = { name = "langfuse-sso", key = "google-client-secret" }
      }
    },
  ] : []

  sso_domains_env = var.google_sso.enabled && var.google_sso.allowed_domains != "" ? [
    { name = "AUTH_GOOGLE_ALLOWED_DOMAINS", value = var.google_sso.allowed_domains, valueFrom = null },
  ] : []

  auth_env = [
    { name = "AUTH_DISABLE_SIGNUP", value = tostring(!var.allow_signup), valueFrom = null },
    {
      name      = "AUTH_DISABLE_USERNAME_PASSWORD"
      value     = tostring(var.google_sso.enabled && var.google_sso.disable_password_login)
      valueFrom = null
    },
  ]

  additional_env = concat(local.sso_env, local.sso_domains_env, local.auth_env)
}
