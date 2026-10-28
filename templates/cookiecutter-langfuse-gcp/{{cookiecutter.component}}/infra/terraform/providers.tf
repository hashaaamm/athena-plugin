# Provider configuration. No credential appears here: locally the Google provider reads Application
# Default Credentials (`gcloud auth application-default login`), and in CI the federated credentials
# google-github-actions/auth writes. The Kubernetes and Helm providers authenticate with the same
# identity's access token, so whoever runs Terraform is who the cluster sees.

# The official module reads the project and region from this provider's configuration
# (`data.google_client_config.current` in its versions.tf and every regional resource), so these
# two values place everything it creates.
provider "google" {
  project = var.project_id
  region  = var.region
}

data "google_client_config" "current" {}

# The cluster the chart is installed into: the module's in `new` mode, the one named in the
# variables in `existing` mode. The module's README configures both providers from its outputs;
# the existing-cluster path reads the same values from a data source instead.
#
# `module.langfuse[0].x`, not `one(module.langfuse[*].x)`: a splat refers to the whole module, which
# includes the Helm releases these providers deploy, and Terraform reports a cycle. An index refers
# to the one output. In `existing` mode the index is out of range, which is harmless: only the
# branch the condition selects is evaluated.
locals {
  cluster_host = (
    var.cluster_mode == "new"
    ? module.langfuse[0].cluster_host
    : "https://${one(data.google_container_cluster.existing[*].endpoint)}"
  )
  cluster_ca_certificate = (
    var.cluster_mode == "new"
    ? module.langfuse[0].cluster_ca_certificate
    : base64decode(one(data.google_container_cluster.existing[*].master_auth[0].cluster_ca_certificate))
  )
}

provider "kubernetes" {
  host                   = local.cluster_host
  cluster_ca_certificate = local.cluster_ca_certificate
  token                  = data.google_client_config.current.access_token
}

provider "helm" {
  kubernetes {
    host                   = local.cluster_host
    cluster_ca_certificate = local.cluster_ca_certificate
    token                  = data.google_client_config.current.access_token
  }
}
