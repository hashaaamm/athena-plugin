# Plan-only tests with mocked providers: no cloud credentials, no cloud resources. `just lint` and
# the infra workflow's static job run them with `terraform test`.
#
# They prove what `terraform validate` cannot: that each cluster mode plans the resources it should
# and none it should not, that the profiles reach the module, and that a bad combination of
# variables fails at plan with its own message.

# `override_during = plan`: mocked values are generated while planning, so a value the plan
# computes from them (the cluster's CA certificate, the network's id) is known in the assertions.
mock_provider "google" {
  override_during = plan
}
mock_provider "kubernetes" {
  override_during = plan
}
mock_provider "helm" {
  override_during = plan
}
mock_provider "random" {
  override_during = plan
}
mock_provider "tls" {
  override_during = plan
}

# The official module stands in as its outputs. A mocked provider cannot fill the cluster's
# `master_auth` block, which the module's outputs index, so its resources are not planned here:
# `terraform validate` checks the arguments passed to it against its variables, and its own
# repository tests its resources.
override_module {
  target = module.langfuse
  outputs = {
    cluster_name           = "langfuse"
    cluster_host           = "https://203.0.113.20"
    cluster_ca_certificate = "ca"
    cluster_token          = "token"
    ingress_ip             = null
  }
}

variables {
  project_id        = "acme-obs-dev"
  region            = "europe-west1"
  environment       = "dev"
  name              = "langfuse"
  hostname          = "langfuse.example.com"
  github_repository = "acme/platform"
  state_bucket      = "acme-obs-dev-tf-state"
}

# An existing cluster that meets every requirement.
override_data {
  target          = data.google_container_cluster.existing
  override_during = plan
  values = {
    endpoint                 = "203.0.113.10"
    network                  = "projects/acme-obs-dev/global/networks/shared"
    master_version           = "1.33.5-gke.1080000"
    master_auth              = [{ cluster_ca_certificate = "Y2E=" }]
    workload_identity_config = [{ workload_pool = "acme-obs-dev.svc.id.goog" }]
    ip_allocation_policy     = [{ cluster_secondary_range_name = "pods" }]
    addons_config            = [{ http_load_balancing = [{ disabled = false }] }]
    private_cluster_config   = [{ enable_private_endpoint = false }]
  }
}

override_data {
  target          = data.google_compute_network.existing
  override_during = plan
  values = {
    id = "projects/acme-obs-dev/global/networks/shared"
  }
}

run "new_cluster_small" {
  command = plan

  variables {
    cluster_mode = "new"
    profile      = "small"
  }

  assert {
    condition     = length(module.langfuse) == 1
    error_message = "new mode must call the official module"
  }
  assert {
    condition     = length(google_sql_database_instance.existing) == 0 && length(helm_release.existing_langfuse) == 0
    error_message = "new mode must not plan the existing-cluster resources"
  }
  assert {
    condition     = local.size.clickhouse_replicas == 1 && local.size.clickhouse_keeper_replicas == 1
    error_message = "the small profile runs one ClickHouse replica and one Keeper"
  }
  assert {
    condition     = local.size.database_instance_edition == "ENTERPRISE" && local.size.cache_tier == "BASIC"
    error_message = "the small profile uses Cloud SQL Enterprise and Basic Redis"
  }
  assert {
    condition     = google_iam_workload_identity_pool_provider.github.attribute_condition == "assertion.repository == \"acme/platform\""
    error_message = "Workload Identity must trust exactly the configured repository"
  }
  assert {
    condition     = endswith(google_service_account_iam_member.github_impersonates_apply.member, "/attribute.environment/infrastructure")
    error_message = "only the infrastructure environment may impersonate the apply identity"
  }
  assert {
    condition     = google_storage_bucket_iam_member.plan_reads_state.role == "roles/storage.objectViewer"
    error_message = "the plan identity reads state and never writes it"
  }
}

run "new_cluster_ha" {
  command = plan

  variables {
    cluster_mode = "new"
    profile      = "ha"
  }

  assert {
    condition     = local.size.clickhouse_replicas == 3 && local.size.database_availability_type == "REGIONAL" && local.size.cache_tier == "STANDARD_HA"
    error_message = "the ha profile is the module's defaults"
  }
}

run "existing_cluster" {
  command = plan

  variables {
    cluster_mode              = "existing"
    existing_cluster_name     = "shared"
    existing_cluster_location = "europe-west1"
  }

  assert {
    condition     = length(module.langfuse) == 0
    error_message = "existing mode must not call the module, which would create a cluster"
  }
  assert {
    condition     = google_sql_database_instance.existing[0].settings[0].ip_configuration[0].private_network == "projects/acme-obs-dev/global/networks/shared"
    error_message = "Cloud SQL must be on the existing cluster's network"
  }
  assert {
    condition     = google_redis_instance.existing[0].authorized_network == "projects/acme-obs-dev/global/networks/shared"
    error_message = "Memorystore must be on the existing cluster's network"
  }
  assert {
    condition     = helm_release.existing_langfuse[0].version == "2.1.7" && kubernetes_namespace.existing[0].metadata[0].name == "langfuse"
    error_message = "existing mode installs the pinned chart into the component's namespace"
  }
  assert {
    condition     = length(helm_release.existing_cert_manager) == 1
    error_message = "cert-manager is installed unless install_cert_manager is false"
  }
}

run "existing_cluster_without_cert_manager" {
  command = plan

  variables {
    cluster_mode              = "existing"
    existing_cluster_name     = "shared"
    existing_cluster_location = "europe-west1"
    install_cert_manager      = false
  }

  assert {
    condition     = length(helm_release.existing_cert_manager) == 0
    error_message = "install_cert_manager = false must leave the cluster's own cert-manager alone"
  }
}

run "existing_cluster_without_workload_identity_fails" {
  command = plan

  variables {
    cluster_mode              = "existing"
    existing_cluster_name     = "shared"
    existing_cluster_location = "europe-west1"
  }

  override_data {
    target          = data.google_container_cluster.existing
    override_during = plan
    values = {
      endpoint                 = "203.0.113.10"
      network                  = "projects/acme-obs-dev/global/networks/shared"
      master_version           = "1.33.5-gke.1080000"
      master_auth              = [{ cluster_ca_certificate = "Y2E=" }]
      workload_identity_config = []
      ip_allocation_policy     = [{ cluster_secondary_range_name = "pods" }]
      addons_config            = [{ http_load_balancing = [{ disabled = false }] }]
      private_cluster_config   = [{ enable_private_endpoint = false }]
    }
  }

  expect_failures = [terraform_data.existing_cluster_requirements]
}

run "existing_mode_without_a_cluster_name_fails" {
  command = plan

  variables {
    cluster_mode = "existing"
  }

  expect_failures = [var.existing_cluster_name, var.existing_cluster_location]
}

run "new_mode_with_a_cluster_name_fails" {
  command = plan

  variables {
    cluster_mode          = "new"
    existing_cluster_name = "shared"
  }

  expect_failures = [var.existing_cluster_name]
}

run "unknown_profile_fails" {
  command = plan

  variables {
    profile = "medium"
  }

  expect_failures = [var.profile]
}
