# Outputs are named for what consumes them. infra/scripts/sync-github.sh copies the three CI ones
# into repository variables; the rest are for the person setting the environment up. None is
# sensitive: every one is a name, an address or an identifier.

# The module creates the zone in new mode but does not output its name servers (its README reads
# them with gcloud), so they are read back here once it exists.
data "google_dns_managed_zone" "module" {
  count      = var.cluster_mode == "new" && var.create_dns_zone ? 1 : 0
  name       = replace(var.hostname, ".", "-")
  depends_on = [module.langfuse]
}

output "langfuse_url" {
  description = "Where Langfuse is served, once DNS resolves and the managed certificate is ACTIVE."
  value       = "https://${var.hostname}"
}

output "otel_traces_endpoint" {
  description = "OTLP/HTTP traces endpoint (HTTP/protobuf or HTTP/JSON; no gRPC). Authenticate with `Authorization: Basic base64(<public key>:<secret key>)` from a Langfuse project's API keys."
  value       = "https://${var.hostname}/api/public/otel/v1/traces"
}

output "dns_name_servers" {
  description = "Delegate the hostname to these name servers at your registrar or parent zone (NS records). Empty when create_dns_zone is false."
  value = (
    !var.create_dns_zone ? [] :
    var.cluster_mode == "new" ? one(data.google_dns_managed_zone.module[*].name_servers) :
    one(google_dns_managed_zone.existing[*].name_servers)
  )
}

output "ingress_ip" {
  description = "The load balancer's reserved address, for an A record you manage yourself. Null when create_dns_zone is true: the zone's own A record points at it."
  value = (
    var.create_dns_zone ? null :
    var.cluster_mode == "new" ? one(module.langfuse[*].ingress_ip) :
    one(google_compute_global_address.existing_ingress[*].address)
  )
}

output "cluster_name" {
  description = "The GKE cluster Langfuse runs in: `gcloud container clusters get-credentials <name> --location <region>`."
  value       = var.cluster_mode == "new" ? one(module.langfuse[*].cluster_name) : var.existing_cluster_name
}

output "namespace" {
  description = "The Kubernetes namespace this component owns."
  value       = var.name
}

output "gcp_project" {
  description = "The project everything is in."
  value       = var.project_id
}

output "wif_provider" {
  description = "Workload Identity provider resource name — the repository variable <COMPONENT>_WIF_PROVIDER."
  value       = google_iam_workload_identity_pool_provider.github.name
}

output "plan_service_account" {
  description = "The read-only identity pull requests plan as — <COMPONENT>_PLAN_SA."
  value       = google_service_account.plan.email
}

output "apply_service_account" {
  description = "The identity merges to main apply as, from the `infrastructure` environment — <COMPONENT>_APPLY_SA."
  value       = google_service_account.apply.email
}
