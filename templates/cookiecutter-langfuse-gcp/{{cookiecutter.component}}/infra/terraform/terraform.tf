# Terraform and provider versions, and the state backend.
#
# Providers are bounded with `~>` and pinned exactly by the committed .terraform.lock.hcl, which
# carries hashes for linux_amd64 (CI), darwin_arm64 and darwin_amd64 (laptops). Regenerate it with
# `just lock` after changing a constraint, and commit the diff: `init -lockfile=readonly` in CI
# refuses a provider the lock file does not name.
#
# The bounds are the official module's, not a choice made here: langfuse-terraform-gcp 1.0.10
# declares google ~> 6.0, kubernetes ~> 2.0 and helm ~> 2.7 (its versions.tf), and the helm
# provider's 2.x `kubernetes { }` block is the shape its README configures. Moving any of them to a
# new major is an upstream change first.
terraform {
  required_version = "~> 1.16"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.50"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.38"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.17"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.7"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }

  # GCS locks natively. The bucket, and the prefix this environment's state lives under, come from
  # environments/<env>.backend.hcl (`just init` passes it as -backend-config), so one bucket can
  # hold every component's state, a prefix each. The bucket is created — or an existing one checked
  # — by infra/bootstrap/state-bucket.sh, never by this configuration: a configuration cannot create
  # the bucket that holds its own state.
  backend "gcs" {}
}
