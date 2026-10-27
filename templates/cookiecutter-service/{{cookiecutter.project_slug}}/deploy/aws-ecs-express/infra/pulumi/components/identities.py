"""IAM roles: what ECS runs as, what the application runs as, and what CI deploys as.

The split is the point. The **execution** role pulls the image, writes logs and reads this stack's
secrets — and that is ECS acting, not the application. The **runtime** role is the application's own
AWS identity and starts with no permissions at all. The **deploy** role can push an image and point
the service at it, but cannot read a secret's value. The **preview** role can read everything and
change nothing but a lock file. None of them has an access key: CI exchanges GitHub's own OIDC token
for a short-lived session, and a long-lived key in a CI secret is the most-stolen credential class
there is. Retrieve the Pulumi standards and the GitHub Actions standards for the graded form.

The GitHub OIDC identity provider itself is **not** created here. There is exactly one per account
for `token.actions.githubusercontent.com`, shared by every service in it, and a stack that created
it would delete it from under its neighbours on `destroy` — the ownership problem ADR-0013 describes
for the state bucket. `infra/bootstrap/state-bucket.sh` creates it; this stack looks it up.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from typing import Any

import pulumi
import pulumi_aws as aws

from config import StackConfig

#: What the Express service's infrastructure role is allowed to do on our behalf: create and manage
#: the load balancer, target groups, security groups and scaling policies Express Mode provisions.
INFRASTRUCTURE_POLICY = (
    "arn:aws:iam::aws:policy/service-role/AmazonECSInfrastructureRoleforExpressGatewayServices"
)

#: Pull from ECR and write to CloudWatch Logs. Secret access is NOT in it; that is granted per
#: secret below, so a secret added to the account later is not readable by this role.
TASK_EXECUTION_POLICY = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"

#: Read-only across the account, for the pull-request preview. A preview runs with `--refresh`, so
#: it reads every resource the stack declares; a hand-maintained list of read permissions breaks
#: every time the stack gains a resource type. What this widens, stated plainly: the preview can
#: read resource metadata across the account. What it does not widen: it writes nothing, and it
#: cannot read a secret's value — that is granted below, for the stack's generated secrets only.
READ_ONLY_POLICY = "arn:aws:iam::aws:policy/ReadOnlyAccess"

#: GitHub's OIDC audience for AWS. `aws-actions/configure-aws-credentials` asks for exactly this.
GITHUB_AUDIENCE = "sts.amazonaws.com"
GITHUB_TOKEN_HOST = "token.actions.githubusercontent.com"

#: The GitHub environment cd.yml deploys from. The deploy role trusts this environment and nothing
#: else in the repository: a workflow on a feature branch cannot assume it.
DEPLOY_ENVIRONMENT = "production"

#: ECR actions a `docker push` and a `docker pull` make, and nothing that deletes or reconfigures.
ECR_PUSH_PULL = (
    "ecr:BatchCheckLayerAvailability",
    "ecr:BatchGetImage",
    "ecr:CompleteLayerUpload",
    "ecr:GetDownloadUrlForLayer",
    "ecr:InitiateLayerUpload",
    "ecr:PutImage",
    "ecr:UploadLayerPart",
)


def _document(*statements: Mapping[str, Any]) -> pulumi.Output[str]:
    return pulumi.Output.json_dumps({"Version": "2012-10-17", "Statement": list(statements)})


def _service_trust(config: StackConfig, service: str) -> pulumi.Output[str]:
    """Trust for an AWS service principal, confined to this account.

    `aws:SourceAccount` is the confused-deputy guard: without it, ECS acting for *another* account
    could be induced to assume this role.
    """
    statement: dict[str, Any] = {
        "Effect": "Allow",
        "Principal": {"Service": service},
        "Action": "sts:AssumeRole",
    }
    if service == "ecs-tasks.amazonaws.com":
        statement["Condition"] = {"StringEquals": {"aws:SourceAccount": config.account_id}}
    return _document(statement)


def _github_trust(provider_arn: pulumi.Input[str], subject: str) -> pulumi.Output[str]:
    """Trust for one GitHub OIDC subject, exactly.

    `StringEquals` on `sub`, never `StringLike` with a wildcard. Without the `sub` condition *any*
    repository on GitHub can mint a token this role accepts; with a wildcard, any branch or any
    environment of this one can. It is the single most important condition in this file.
    """
    return _document(
        {
            "Effect": "Allow",
            "Principal": {"Federated": provider_arn},
            "Action": "sts:AssumeRoleWithWebIdentity",
            "Condition": {
                "StringEquals": {
                    f"{GITHUB_TOKEN_HOST}:aud": GITHUB_AUDIENCE,
                    f"{GITHUB_TOKEN_HOST}:sub": subject,
                }
            },
        }
    )


@dataclass(frozen=True, slots=True)
class DeployTargets:
    """Everything the deploy role may act on, by ARN or name. Nothing else."""

    cluster_name: pulumi.Input[str]
    cluster_arn: pulumi.Input[str]
    repository_arns: Sequence[pulumi.Input[str]]
    service_arns: Sequence[pulumi.Input[str]]
    #: The migration task's family and log group, when there is a database to migrate.
    migrate_family: pulumi.Input[str] | None = None
    migrate_log_group: pulumi.Input[str] | None = None
    #: Roles a deploy hands to ECS: what it runs tasks as.
    passed_role_arns: Sequence[pulumi.Input[str]] = field(default_factory=tuple)


class Identities(pulumi.ComponentResource):
    def __init__(
        self,
        config: StackConfig,
        *,
        github_provider_arn: pulumi.Input[str],
        state_key_arn: pulumi.Input[str],
        secret_arns: Sequence[pulumi.Input[str]] = (),
        generated_secret_arns: Sequence[pulumi.Input[str]] = (),
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        super().__init__("service:infra:Identities", config.name("identities"), None, opts)
        self._config = config

        #: What the Express service has to wait for. AWS documents a race on delete: destroy the
        #: infrastructure role's policy before the service and the service sticks in DRAINING,
        #: because the role it is draining with can no longer touch its own load balancer. Waiting
        #: for the execution role's grants on create is the mirror image: a task started before its
        #: role can read its secrets fails to start and reports nothing useful.
        self.service_dependencies: list[pulumi.Resource] = []

        self.infrastructure = self._role(
            "infrastructure",
            _service_trust(config, "ecs.amazonaws.com"),
            "lets ECS manage the Express service's load balancer and scaling",
        )
        self.service_dependencies.append(
            self._attach("infrastructure", self.infrastructure, INFRASTRUCTURE_POLICY)
        )

        self.execution = self._role(
            "execution",
            _service_trust(config, "ecs-tasks.amazonaws.com"),
            "lets ECS pull the image, write logs and read this stack's secrets",
        )
        self.service_dependencies.append(
            self._attach("execution", self.execution, TASK_EXECUTION_POLICY)
        )
        if secret_arns:
            # Exactly this stack's secrets, by ARN. `secretsmanager:GetSecretValue` on `*` would let
            # every service in the account read every other service's database password.
            self.service_dependencies.append(
                aws.iam.RolePolicy(
                    "execution-reads-secrets",
                    role=self.execution.name,
                    policy=_document(
                        {
                            "Effect": "Allow",
                            "Action": "secretsmanager:GetSecretValue",
                            "Resource": list(secret_arns),
                        }
                    ),
                    opts=pulumi.ResourceOptions(parent=self),
                )
            )

        # The application's own AWS identity. No policies: the service as generated calls no AWS
        # API. When it needs one — an S3 bucket, a queue — the grant goes on this role, scoped to
        # that resource, and the execution role above stays exactly as it is.
        self.runtime = self._role(
            "runtime",
            _service_trust(config, "ecs-tasks.amazonaws.com"),
            "the application's own AWS identity; add its grants here",
        )

        repository = config.github_repository
        self.deploy = self._role(
            "deploy",
            _github_trust(
                github_provider_arn, f"{config.subject_prefix}:environment:{DEPLOY_ENVIRONMENT}"
            ),
            f"GitHub Actions in {repository}, environment {DEPLOY_ENVIRONMENT}: deploys",
        )

        self.preview = self._role(
            "preview",
            _github_trust(github_provider_arn, f"{config.subject_prefix}:pull_request"),
            f"GitHub Actions in {repository}, pull requests: pulumi preview",
        )
        self._attach("preview", self.preview, READ_ONLY_POLICY)

        bucket = f"arn:aws:s3:::{config.state_bucket}"
        project = config.pulumi_project
        preview_statements: list[dict[str, Any]] = [
            # The state of this Pulumi project and nothing else in the shared bucket. Pulumi's DIY
            # backend reads `.pulumi/meta.yaml` to learn the layout and the stack file under
            # `.pulumi/stacks/<project>/`; a preview with `--refresh` writes neither.
            {
                "Effect": "Allow",
                "Action": "s3:ListBucket",
                "Resource": bucket,
                "Condition": {"StringLike": {"s3:prefix": [".pulumi/*"]}},
            },
            {
                "Effect": "Allow",
                "Action": "s3:GetObject",
                "Resource": [
                    f"{bucket}/.pulumi/meta.yaml",
                    f"{bucket}/.pulumi/stacks/{project}/*",
                ],
            },
            # The one write. The DIY backend takes a lock for every operation, a preview included,
            # as an object under `.pulumi/locks/organization/<project>/<stack>/`, and deletes it
            # when it finishes. A grant that stops at reading fails on the lock, which reads as a
            # Pulumi bug rather than a missing permission.
            {
                "Effect": "Allow",
                "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"],
                "Resource": f"{bucket}/.pulumi/locks/organization/{project}/*",
            },
            # Decrypt reads the stack's secrets and every state object, which the bucket encrypts
            # with this key. GenerateDataKey is what S3 asks KMS for when the lock object above is
            # written into an SSE-KMS bucket; without it the one write fails.
            {
                "Effect": "Allow",
                "Action": ["kms:Decrypt", "kms:GenerateDataKey"],
                "Resource": state_key_arn,
            },
        ]
        if generated_secret_arns:
            # Refreshing a secret *version* reads its value. This is not a new exposure: the
            # preview can already decrypt the stack's state, and state holds these same values,
            # generated by the stack. And a pull request from a fork gets no OIDC token at all, so
            # nothing outside the repository can reach this. Manual secrets have no version in the
            # stack, so the preview never reads them and is not granted them.
            preview_statements.append(
                {
                    "Effect": "Allow",
                    "Action": "secretsmanager:GetSecretValue",
                    "Resource": list(generated_secret_arns),
                }
            )
        self.preview_state_policy = aws.iam.RolePolicy(
            "preview-reads-state",
            role=self.preview.name,
            policy=_document(*preview_statements),
            opts=pulumi.ResourceOptions(parent=self),
        )

        self.register_outputs(
            {
                "runtime_role_arn": self.runtime.arn,
                "deploy_role_arn": self.deploy.arn,
                "preview_role_arn": self.preview.arn,
            }
        )

    def grant_deploy(self, targets: DeployTargets) -> aws.iam.RolePolicy:
        """What the deploy role may do, granted after the things it acts on exist.

        A method rather than a constructor argument because the order is circular otherwise: the
        services need the roles above before they can be created, and this policy names the
        services. Least privilege, stated per statement: every action is scoped to this stack's
        resources except the three that IAM offers no resource-level control for.
        """
        config = self._config
        region, account = config.region, config.account_id
        ecs = f"arn:aws:ecs:{region}:{account}"
        statements: list[dict[str, Any]] = [
            # The registry login. It is account-wide by AWS's design — the token it returns is
            # useless without the per-repository grant below.
            {"Effect": "Allow", "Action": "ecr:GetAuthorizationToken", "Resource": "*"},
            {
                "Effect": "Allow",
                "Action": list(ECR_PUSH_PULL),
                "Resource": list(targets.repository_arns),
            },
            # Point a service at a new image. Describe to read the current container definition,
            # update to send it back with only the image changed — see cd.yml.
            {
                "Effect": "Allow",
                "Action": [
                    "ecs:DescribeExpressGatewayService",
                    "ecs:UpdateExpressGatewayService",
                ],
                "Resource": list(targets.service_arns),
            },
            # CD follows the rollout by polling DescribeServices.
            {
                "Effect": "Allow",
                "Action": "ecs:DescribeServices",
                "Resource": pulumi.Output.concat(ecs, ":service/", targets.cluster_name, "/*"),
            },
            # Hand ECS the roles a deployment runs as, and only to ECS.
            {
                "Effect": "Allow",
                "Action": "iam:PassRole",
                "Resource": list(targets.passed_role_arns),
                "Condition": {
                    "StringEquals": {
                        "iam:PassedToService": ["ecs-tasks.amazonaws.com", "ecs.amazonaws.com"]
                    }
                },
            },
        ]
        if targets.migrate_family is not None:
            statements += [
                # Neither action supports resource-level permissions: IAM accepts only `*`.
                {
                    "Effect": "Allow",
                    "Action": ["ecs:DescribeTaskDefinition", "ecs:RegisterTaskDefinition"],
                    "Resource": "*",
                },
                # Run a revision of the migration family, in this cluster, and nothing else.
                {
                    "Effect": "Allow",
                    "Action": "ecs:RunTask",
                    "Resource": pulumi.Output.concat(
                        ecs, ":task-definition/", targets.migrate_family, ":*"
                    ),
                    "Condition": {"ArnEquals": {"ecs:cluster": targets.cluster_arn}},
                },
                {
                    "Effect": "Allow",
                    "Action": "ecs:DescribeTasks",
                    "Resource": pulumi.Output.concat(ecs, ":task/", targets.cluster_name, "/*"),
                },
                # A failed migration prints its own log in the CD run, rather than a link to a
                # console the reader may not have access to.
                {
                    "Effect": "Allow",
                    "Action": "logs:GetLogEvents",
                    "Resource": pulumi.Output.concat(
                        f"arn:aws:logs:{region}:{account}:log-group:",
                        targets.migrate_log_group or "",
                        ":log-stream:*",
                    ),
                },
            ]
        return aws.iam.RolePolicy(
            "deploy-permissions",
            role=self.deploy.name,
            policy=_document(*statements),
            opts=pulumi.ResourceOptions(parent=self),
        )

    def _role(self, label: str, trust: pulumi.Input[str], description: str) -> aws.iam.Role:
        return aws.iam.Role(
            f"{label}-role",
            name=self._config.role_name(label),
            description=f"{self._config.slug}: {description}",
            assume_role_policy=trust,
            opts=pulumi.ResourceOptions(parent=self),
        )

    def _attach(
        self, label: str, role: aws.iam.Role, policy_arn: str
    ) -> aws.iam.RolePolicyAttachment:
        return aws.iam.RolePolicyAttachment(
            f"{label}-{policy_arn.rsplit('/', 1)[-1]}",
            role=role.name,
            policy_arn=policy_arn,
            opts=pulumi.ResourceOptions(parent=self),
        )
