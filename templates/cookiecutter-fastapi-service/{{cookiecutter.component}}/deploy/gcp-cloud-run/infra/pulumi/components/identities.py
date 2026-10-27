"""Service accounts and Workload Identity Federation.

The split is the point: the CI account can build and deploy but cannot read the database or a
secret's value; the runtime account can read its own secrets and log in to the database as itself,
but cannot deploy{% if cookiecutter.use_postgres == "yes" %}; the migrator account runs the migration job and nothing else, and is the only
one that can read the database's admin password{% endif %}. The apply account runs `pulumi up` when a
change merges to main, and only a job in the repository's `infrastructure` GitHub environment may
impersonate it. None has a downloadable key. A downloaded
service-account JSON key is the most-stolen credential class there is, and the house rule is that CI
authenticates with Workload Identity Federation instead — retrieve the Pulumi standards and the
GitHub Actions standards for the graded form.
"""

from __future__ import annotations

import pulumi
import pulumi_gcp as gcp

from config import StackConfig

#: Granted to the Cloud Run service.
#:
#: Secret *access* is deliberately absent: it is granted per secret in `components/secrets.py`, so
#: a secret added later is not automatically readable by everything that can already read these.
{%- if cookiecutter.use_postgres == "yes" %}
#: `cloudsql.client` lets the connector reach the instance; `cloudsql.instanceUser` lets the
#: account log in to it as its own IAM database user. Neither reads data on its own: what the user
#: may do inside the database is the grants the migration gives it.
RUNTIME_ROLES = (
    "roles/logging.logWriter",
    "roles/cloudsql.client",
    "roles/cloudsql.instanceUser",
)

#: Granted to the migration job's own account. It connects as `postgres` through the Cloud SQL
#: connection Cloud Run mounts, with the password only it can read, so it needs the client role and
#: no IAM login.
MIGRATOR_ROLES = (
    "roles/logging.logWriter",
    "roles/cloudsql.client",
)
{%- else %}
RUNTIME_ROLES = ("roles/logging.logWriter",)
{%- endif %}

#: Granted to the GitHub Actions identity.
#:
#: `viewer` and `iam.securityReviewer` are there for one job: the `pulumi preview` a pull request
#: runs. A preview runs with `--refresh`, so it reads every resource in the stack, and refreshing
#: an IAM binding means reading an IAM policy — which `viewer` deliberately excludes. Granting the
#: individual read roles instead was tried in the reference stack and is the wrong shape: a
#: hand-maintained list breaks every time the stack gains a resource type.
#:
#: What this widens, stated plainly: CI can read resource metadata across the project rather than
#: only what it deploys. What it does **not** widen is what matters — both roles are read-only, and
#: reading a secret's value needs `secretmanager.versions.access`, which neither carries.
CI_ROLES = (
    "roles/run.developer",
    "roles/artifactregistry.writer",
    "roles/viewer",
    "roles/iam.securityReviewer",
)

#: Granted to the apply account, which infra.yml runs `pulumi up` as on a merge to main: one
#: predefined admin role per service this stack enables and manages, and read on compute, which the
#: provider resolves the project's default region and zone through.
#:
#: Predefined roles, not `roles/owner`: the GCP infrastructure standards rule out basic roles in
#: production. That keeps every grant readable and every service this account can touch named here;
#: it does not make the account weak. `resourcemanager.projectIamAdmin` can grant any role on the
#: project, this account's own included, and it has to — the stack binds project roles — so in
#: effect the account can do anything in the project. One project per environment is what keeps
#: that to the environment it applies. A resource type the stack gains is a role added here, in the
#: same pull request, or the apply on main fails with a permission error.
#:
#: The state bucket and its key are granted on the resources themselves, not here.
APPLY_ROLES = (
    "roles/serviceusage.serviceUsageAdmin",
    "roles/resourcemanager.projectIamAdmin",
    "roles/iam.serviceAccountAdmin",
    "roles/iam.workloadIdentityPoolAdmin",
    "roles/artifactregistry.admin",
    "roles/secretmanager.admin",
    "roles/run.admin",
{%- if cookiecutter.use_postgres == "yes" %}
    "roles/cloudsql.admin",
{%- endif %}
    "roles/compute.viewer",
)

#: The GitHub environment infra.yml's apply job runs in. Only a token from it may impersonate the
#: apply account, so the environment's protection rules — required reviewers, and main as the only
#: branch that may use it — gate every apply.
APPLY_ENVIRONMENT = "infrastructure"

#: The bucket the bootstrap script creates, and the KMS key it creates alongside. Named here rather
#: than passed in, because the bootstrap script names them the same way and the two must agree.
STATE_BUCKET_SUFFIX = "-pulumi-state"
KEYRING = "pulumi"
KEY = "stack"


class Identities(pulumi.ComponentResource):
    def __init__(
        self,
        config: StackConfig,
        project_number: pulumi.Input[str],
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        super().__init__("service:infra:Identities", config.name("identities"), None, opts)

        self.runtime = gcp.serviceaccount.Account(
            "runtime-sa",
            project=config.project,
            account_id=config.account_id("run"),
            display_name=f"{config.slug} runtime",
            opts=pulumi.ResourceOptions(parent=self),
        )
        self._bind(config, "runtime", self.runtime, RUNTIME_ROLES)
{%- if cookiecutter.use_postgres == "yes" %}

        #: The migration job's account. Separate from the runtime account so that the admin
        #: password is readable by the one process that uses it, and a compromised service holds
        #: neither that password nor the schema.
        self.migrator = gcp.serviceaccount.Account(
            "migrator-sa",
            project=config.project,
            account_id=config.account_id("migrate"),
            display_name=f"{config.slug} migrations",
            opts=pulumi.ResourceOptions(parent=self),
        )
        self._bind(config, "migrator", self.migrator, MIGRATOR_ROLES)
{%- endif %}

        self.ci = gcp.serviceaccount.Account(
            "ci-sa",
            project=config.project,
            account_id=config.account_id("ci"),
            display_name=f"{config.slug} CI",
            opts=pulumi.ResourceOptions(parent=self),
        )
        self._bind(config, "ci", self.ci, CI_ROLES)

        # CI deploys revisions that run *as* the runtime account, which needs explicit permission
        # to act as it. Without this the deploy fails with a permission error that does not mention
        # impersonation anywhere.
        gcp.serviceaccount.IAMMember(
            "ci-acts-as-runtime",
            service_account_id=self.runtime.name,
            role="roles/iam.serviceAccountUser",
            member=self.ci.email.apply(lambda email: f"serviceAccount:{email}"),
            opts=pulumi.ResourceOptions(parent=self),
        )
{%- if cookiecutter.use_postgres == "yes" %}
        # And the migration job runs as the migrator account: `gcloud run jobs update --image`
        # needs the same permission on it, or the migrate step fails before anything has run.
        gcp.serviceaccount.IAMMember(
            "ci-acts-as-migrator",
            service_account_id=self.migrator.name,
            role="roles/iam.serviceAccountUser",
            member=self.ci.email.apply(lambda email: f"serviceAccount:{email}"),
            opts=pulumi.ResourceOptions(parent=self),
        )
{%- endif %}

        # Read and write the Pulumi state bucket, so the preview job on a pull request can select
        # the stack. Bucket-scoped rather than a project storage role, because a project-level
        # storage role is access to every bucket in the project — including the registry's.
        #
        # `objectUser` rather than `objectViewer`: the self-managed backend writes a lock object,
        # so a read-only grant fails on the lock rather than on the listing, which is the worse
        # failure because it looks like a Pulumi bug rather than a missing permission.
        #
        # The bucket is created by the bootstrap script, not here — Pulumi cannot create the bucket
        # it stores its own state in. The binding lives here because the account it grants to does
        # not exist until Pulumi makes it.
        gcp.storage.BucketIAMMember(
            "ci-reads-pulumi-state",
            bucket=f"{config.project}{STATE_BUCKET_SUFFIX}",
            role="roles/storage.objectUser",
            member=self.ci.email.apply(lambda email: f"serviceAccount:{email}"),
            opts=pulumi.ResourceOptions(parent=self),
        )

        # Reading the state is not enough to read the *stack*. Stack configuration is encrypted
        # with the KMS key named by `secretsprovider`, so a preview that can list the bucket then
        # fails on `cloudkms.cryptoKeyVersions.useToDecrypt` — one permission further in, with an
        # error that names KMS rather than the missing grant.
        #
        # Decrypter and not encrypter: a preview reads configuration and never writes any.
        state_key = (
            f"projects/{config.project}/locations/{config.region}"
            f"/keyRings/{KEYRING}/cryptoKeys/{KEY}"
        )
        gcp.kms.CryptoKeyIAMMember(
            "ci-decrypts-stack-config",
            crypto_key_id=state_key,
            role="roles/cloudkms.cryptoKeyDecrypter",
            member=self.ci.email.apply(lambda email: f"serviceAccount:{email}"),
            opts=pulumi.ResourceOptions(parent=self),
        )

        # --- The apply account ------------------------------------------------------------------
        #
        # Every infrastructure change after the first is applied by infra.yml on a merge to main,
        # as this account (Pulumi standards MUST-9). The first apply runs from an operator's
        # machine, because it is the apply that creates this account.
        self.apply = gcp.serviceaccount.Account(
            "apply-sa",
            project=config.project,
            account_id=config.account_id("apply"),
            display_name=f"{config.slug} apply",
            opts=pulumi.ResourceOptions(parent=self),
        )
        self._bind(config, "apply", self.apply, APPLY_ROLES)
        apply_member = self.apply.email.apply(lambda email: f"serviceAccount:{email}")

        # The apply creates and updates Cloud Run resources that run as these accounts, which
        # needs the right to act as each — granted per account, as for CI.
        acts_as = {"apply-acts-as-runtime": self.runtime}
{%- if cookiecutter.use_postgres == "yes" %}
        acts_as["apply-acts-as-migrator"] = self.migrator
{%- endif %}
        for name, account in acts_as.items():
            gcp.serviceaccount.IAMMember(
                name,
                service_account_id=account.name,
                role="roles/iam.serviceAccountUser",
                member=apply_member,
                opts=pulumi.ResourceOptions(parent=self),
            )

        # The apply writes the state, and the CI account's grants on the state bucket and its key
        # are resources of this stack, so it administers both — on those two resources, not the
        # project. Encrypter as well as decrypter: an apply writes secret outputs back into state.
        gcp.storage.BucketIAMMember(
            "apply-administers-pulumi-state",
            bucket=f"{config.project}{STATE_BUCKET_SUFFIX}",
            role="roles/storage.admin",
            member=apply_member,
            opts=pulumi.ResourceOptions(parent=self),
        )
        for name, role in (
            ("apply-administers-state-key", "roles/cloudkms.admin"),
            ("apply-uses-state-key", "roles/cloudkms.cryptoKeyEncrypterDecrypter"),
        ):
            gcp.kms.CryptoKeyIAMMember(
                name,
                crypto_key_id=state_key,
                role=role,
                member=apply_member,
                opts=pulumi.ResourceOptions(parent=self),
            )

        self.pool = gcp.iam.WorkloadIdentityPool(
            "github-pool",
            project=config.project,
            workload_identity_pool_id=config.pool_id("github"),
            display_name="GitHub Actions",
            opts=pulumi.ResourceOptions(parent=self),
        )

        self.provider = gcp.iam.WorkloadIdentityPoolProvider(
            "github-provider",
            project=config.project,
            workload_identity_pool_id=self.pool.workload_identity_pool_id,
            workload_identity_pool_provider_id="github",
            display_name="GitHub OIDC",
            attribute_mapping={
                "google.subject": "assertion.sub",
                "attribute.repository": "assertion.repository",
                "attribute.ref": "assertion.ref",
                # The GitHub environment a job runs in; the claim is present only when the job
                # names one. The apply account is impersonable from `infrastructure` alone.
                "attribute.environment": "assertion.environment",
            },
            # Without this condition, *any* GitHub repository in the world could mint a token for
            # this pool. It is the single most important line in this file.
            attribute_condition=f'assertion.repository == "{config.github_repository}"',
            oidc=gcp.iam.WorkloadIdentityPoolProviderOidcArgs(
                issuer_uri="https://token.actions.githubusercontent.com",
            ),
            opts=pulumi.ResourceOptions(parent=self),
        )

        # Only workflows in that repository may impersonate the CI account.
        gcp.serviceaccount.IAMMember(
            "github-impersonates-ci",
            service_account_id=self.ci.name,
            role="roles/iam.workloadIdentityUser",
            member=pulumi.Output.all(project_number, self.pool.workload_identity_pool_id).apply(
                lambda parts: (
                    f"principalSet://iam.googleapis.com/projects/{parts[0]}"
                    f"/locations/global/workloadIdentityPools/{parts[1]}"
                    f"/attribute.repository/{config.github_repository}"
                )
            ),
            opts=pulumi.ResourceOptions(parent=self),
        )

        # Only infra.yml's apply job may impersonate the apply account: a token from this repository
        # — the provider's attribute condition — whose job runs in the `infrastructure` environment.
        # A pull request's preview, the deploy job and every other workflow get a token from
        # another environment or none, and are refused.
        gcp.serviceaccount.IAMMember(
            "github-impersonates-apply",
            service_account_id=self.apply.name,
            role="roles/iam.workloadIdentityUser",
            member=pulumi.Output.all(project_number, self.pool.workload_identity_pool_id).apply(
                lambda parts: (
                    f"principalSet://iam.googleapis.com/projects/{parts[0]}"
                    f"/locations/global/workloadIdentityPools/{parts[1]}"
                    f"/attribute.environment/{APPLY_ENVIRONMENT}"
                )
            ),
            opts=pulumi.ResourceOptions(parent=self),
        )

        #: The value GitHub Actions needs as a repository variable:
        #: `{{ cookiecutter.__component_var }}_WIF_PROVIDER`.
        self.provider_name = self.provider.name

        self.register_outputs(
            {
                "runtime_email": self.runtime.email,
                "ci_email": self.ci.email,
                "apply_email": self.apply.email,
            }
        )

    def _bind(
        self,
        config: StackConfig,
        label: str,
        account: gcp.serviceaccount.Account,
        roles: tuple[str, ...],
    ) -> None:
        member = account.email.apply(lambda email: f"serviceAccount:{email}")
        for role in roles:
            gcp.projects.IAMMember(
                f"{label}-{role.split('/')[-1]}",
                project=config.project,
                role=role,
                member=member,
                opts=pulumi.ResourceOptions(parent=self),
            )
