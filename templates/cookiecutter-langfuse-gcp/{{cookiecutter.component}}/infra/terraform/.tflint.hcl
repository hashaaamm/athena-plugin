# tflint configuration, read by `just lint` and the infra workflow. `tflint --init` downloads the
# Google ruleset at the version pinned here.
config {
  # The remote module is not this repository's to lint; its own CI does that.
  call_module_type = "local"
}

plugin "terraform" {
  enabled = true
  preset  = "recommended"
}

plugin "google" {
  enabled = true
  version = "0.41.0"
  source  = "github.com/terraform-linters/tflint-ruleset-google"
}
