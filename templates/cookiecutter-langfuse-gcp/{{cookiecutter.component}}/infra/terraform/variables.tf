# The configuration's interface. Every value an environment sets is in environments/<env>.tfvars;
# nothing here holds a secret, and nothing secret may be added as a variable — a variable's value
# lands in state and in every saved plan.

variable "project_id" {
  description = "GCP project everything is created in. One project per environment."
  type        = string
  nullable    = false
}

variable "region" {
  description = "GCP region for the cluster (new mode), Cloud SQL, Memorystore and the bucket."
  type        = string
  nullable    = false
}

variable "environment" {
  description = "Environment name, carried into the names this configuration adds (service accounts, the Workload Identity pool)."
  type        = string
  default     = "dev"

  validation {
    condition     = can(regex("^[a-z][a-z0-9]{1,6}$", var.environment))
    error_message = "environment must be 2 to 7 lowercase letters or digits, starting with a letter: `<name>-<environment>-apply` has to fit a 30-character service-account id."
  }
}

variable "name" {
  description = "The component's name: the module's `name`, and the prefix of every name this configuration adds."
  type        = string
  nullable    = false

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{4,14}[a-z0-9]$", var.name))
    error_message = "name must be 6 to 16 characters: lowercase letters, digits and hyphens, starting with a letter."
  }
}

variable "hostname" {
  description = "Hostname Langfuse is served on, e.g. langfuse.example.com. The managed certificate is issued for it once DNS resolves to the load balancer."
  type        = string
  nullable    = false

  validation {
    condition     = can(regex("^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\\.)+[a-z]{2,}$", var.hostname))
    error_message = "hostname must be a lowercase DNS name such as langfuse.example.com."
  }
}

variable "profile" {
  description = "Size: `small` (one ClickHouse replica, zonal Cloud SQL Enterprise, Basic Redis) or `ha` (the module's defaults). See locals.tf."
  type        = string
  default     = "small"

  validation {
    condition     = contains(["small", "ha"], var.profile)
    error_message = "profile must be \"small\" or \"ha\"."
  }
}

variable "cluster_mode" {
  description = "`new`: the official module creates a GKE Autopilot cluster and its VPC. `existing`: install into the GKE cluster named by existing_cluster_name and existing_cluster_location."
  type        = string
  default     = "new"

  validation {
    condition     = contains(["new", "existing"], var.cluster_mode)
    error_message = "cluster_mode must be \"new\" or \"existing\"."
  }
}

variable "existing_cluster_name" {
  description = "Name of the GKE cluster to install into. Required when cluster_mode is `existing`, empty otherwise."
  type        = string
  default     = ""

  validation {
    condition     = var.cluster_mode == "existing" ? var.existing_cluster_name != "" : var.existing_cluster_name == ""
    error_message = "existing_cluster_name is required when cluster_mode is \"existing\" and must be empty when it is \"new\"."
  }
}

variable "existing_cluster_location" {
  description = "Region (regional or Autopilot cluster) or zone (zonal cluster) of the existing cluster. Required when cluster_mode is `existing`, empty otherwise."
  type        = string
  default     = ""

  validation {
    condition = (
      var.cluster_mode == "existing"
      ? can(regex("^[a-z]+-[a-z]+[0-9](-[a-z])?$", var.existing_cluster_location))
      : var.existing_cluster_location == ""
    )
    error_message = "existing_cluster_location must be a region or zone such as europe-west1 or europe-west1-b when cluster_mode is \"existing\", and empty when it is \"new\"."
  }
}

variable "install_cert_manager" {
  description = "Install cert-manager, which the ClickHouse operator's admission webhooks need. Set false in `existing` mode when the cluster already runs cert-manager: two installations fight over the same CRDs. The module always installs it in `new` mode."
  type        = bool
  default     = true
}

variable "create_dns_zone" {
  description = "Create a Cloud DNS zone for the hostname, as the official guide does, and delegate it from your registrar to the name servers in the `dns_name_servers` output. False: point an A record at the `ingress_ip` output yourself."
  type        = bool
  default     = true
}

variable "github_repository" {
  description = "owner/name of the repository whose workflows may plan and apply. Workload Identity trusts nothing else."
  type        = string
  nullable    = false

  validation {
    condition     = can(regex("^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$", var.github_repository))
    error_message = "github_repository must be owner/name."
  }
}

variable "state_bucket" {
  description = "The GCS bucket holding this configuration's state, as named in environments/<env>.backend.hcl. The plan identity is granted read on it and the apply identity write."
  type        = string
  nullable    = false
}

variable "deletion_protection" {
  description = "Deletion protection on the cluster (new mode), Cloud SQL and the bucket. Turned off through a reviewed apply before a teardown, never otherwise."
  type        = bool
  default     = true
}

variable "allow_signup" {
  description = "Whether anyone who reaches the hostname can create a Langfuse account. True for the first deploy, so the first person can sign up; set false once they have (AUTH_DISABLE_SIGNUP)."
  type        = bool
  default     = true
}

variable "google_sso" {
  description = "Sign in with Google. The OAuth client is created in the Google Cloud console and its id and secret are put in the Kubernetes secret `langfuse-sso` by hand (README, \"Single sign-on\"); Terraform only references that secret, so the client secret never enters state."
  type = object({
    enabled = bool
    # Comma-separated email domains allowed to sign in with Google, e.g. "example.com". Empty
    # admits any Google account, so leave it empty only when signup is closed.
    allowed_domains = optional(string, "")
    # Turn off email-and-password sign-in once Google sign-in works (AUTH_DISABLE_USERNAME_PASSWORD).
    disable_password_login = optional(bool, false)
  })
  default = { enabled = false }
}
