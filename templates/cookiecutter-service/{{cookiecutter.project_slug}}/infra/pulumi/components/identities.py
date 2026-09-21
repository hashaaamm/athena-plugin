"""Service accounts and Workload Identity Federation.

Two accounts, and the split is the point: the CI account can build and deploy but cannot read the
database or a secret's value; the runtime account can read its own secrets but cannot deploy.
Neither has a downloadable key. A downloaded service-account JSON key is the most-stolen credential
class there is, and the house rule is that CI authenticates with Workload Identity Federation
instead — retrieve the Pulumi standards and the GitHub Actions standards for the graded form.
"""

from __future__ import annotations

import pulumi
import pulumi_gcp as gcp

from config import StackConfig

#: Granted to the Cloud Run service and its jobs.
#:
#: Secret *access* is deliberately absent: it is granted per secret in `components/secrets.py`, so
#: a secret added later is not automatically readable by everything that can already read these.
{%- if cookiecutter.use_postgres == "yes" %}
RUNTIME_ROLES = (
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
        gcp.kms.CryptoKeyIAMMember(
            "ci-decrypts-stack-config",
            crypto_key_id=(
                f"projects/{config.project}/locations/{config.region}"
                f"/keyRings/{KEYRING}/cryptoKeys/{KEY}"
            ),
            role="roles/cloudkms.cryptoKeyDecrypter",
            member=self.ci.email.apply(lambda email: f"serviceAccount:{email}"),
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

        #: The value GitHub Actions needs as the `WIF_PROVIDER` repository variable.
        self.provider_name = self.provider.name

        self.register_outputs({"runtime_email": self.runtime.email, "ci_email": self.ci.email})

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
