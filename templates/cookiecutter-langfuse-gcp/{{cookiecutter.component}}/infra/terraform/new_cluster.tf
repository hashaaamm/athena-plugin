# cluster_mode = "new": the official module, as Langfuse's GCP deployment guide uses it. It creates
# the VPC, Cloud NAT, a GKE Autopilot cluster, Cloud SQL (private IP), Memorystore Redis, the
# bucket, the Workload Identity binding for the pods, cert-manager, the ClickHouse operator and the
# Langfuse chart, and — with create_dns_zone — a Cloud DNS zone and the A record.
#
# Pinned to a tag of the module's repository. The lock file does not record module versions, so
# this line is the whole pin: 1.0.10 is commit 55a57e31090a49dfd5b4311a6cdb058aa5d0400a
# (2026-10-27). A bump is a pull request whose plan is read before it merges — README, "Upgrading".
#
# The first apply of this mode is two applies, and the module's README says so: the Kubernetes and
# Helm providers are configured from the cluster this module creates, so the cluster has to exist
# before anything inside it can be planned. infra/scripts/up.sh does the targeted first stage.
module "langfuse" {
  source = "github.com/langfuse/langfuse-terraform-gcp?ref=1.0.10"
  count  = var.cluster_mode == "new" ? 1 : 0

  name                 = var.name
  domain               = var.hostname
  kubernetes_namespace = var.name

  langfuse_chart_version = local.langfuse_chart_version
  app_version            = local.langfuse_app_version

  clickhouse_replicas            = local.size.clickhouse_replicas
  clickhouse_keeper_replicas     = local.size.clickhouse_keeper_replicas
  clickhouse_resources           = local.size.clickhouse_resources
  clickhouse_storage_size        = local.size.clickhouse_storage_size
  clickhouse_keeper_storage_size = local.size.clickhouse_keeper_storage_size
  clickhouse_storage_class       = local.size.clickhouse_storage_class

  database_instance_tier              = local.size.database_instance_tier
  database_instance_edition           = local.size.database_instance_edition
  database_instance_availability_type = local.size.database_availability_type

  cache_tier           = local.size.cache_tier
  cache_memory_size_gb = local.size.cache_memory_size_gb

  create_dns_zone = var.create_dns_zone
  # Without a zone here, DNS is an A record you add elsewhere, and it needs an address that exists
  # before the full apply and survives the Ingress being recreated.
  provision_static_ip = !var.create_dns_zone

  deletion_protection = var.deletion_protection

  additional_env         = local.additional_env
  additional_helm_values = local.extra_helm_values
}
