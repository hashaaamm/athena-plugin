# Where the dev environment's state lives: `just init dev` passes this file as -backend-config.
# No credential belongs here. `bucket` is kept in step with `state_bucket` in dev.tfvars by
# infra/bootstrap/state-bucket.sh --bucket; the prefix is this component's own, so one bucket can
# hold every component's state side by side.
bucket = "{{ cookiecutter.state_bucket }}"
prefix = "{{ cookiecutter.component }}/dev"
