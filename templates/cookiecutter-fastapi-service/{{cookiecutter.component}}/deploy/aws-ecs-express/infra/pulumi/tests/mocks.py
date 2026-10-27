"""The mocked Pulumi engine every test module runs against. No credentials, no network, no state.

It records what the program asks for — every resource, with its inputs and the provider it names,
and every lookup — so a test can assert on the request AWS would receive rather than on the
program's source. Outputs are filled in the shape AWS returns them: ARNs, a repository URL, an
Express endpoint, the RDS-managed master secret.

Imported first by each test module, because `set_mocks` must run before any component module is
imported.
"""

from __future__ import annotations

import json
import uuid
from dataclasses import dataclass
from typing import Any

import pulumi

ACCOUNT = "123456789012"
REGION = "{{ cookiecutter.aws_region }}"
#: The component's full name, `<project>-<component>`: the base of every resource name.
SLUG = "{{ cookiecutter.__name }}"
#: The image `just infra-first-deploy` pushes and records as `serviceImage`: the registry this stack
#: creates, tagged with a commit.
IMAGE = (
    f"{ACCOUNT}.dkr.ecr.{REGION}.amazonaws.com/{SLUG}-dev:0123456789abcdef0123456789abcdef01234567"
)
ZONES = [f"{REGION}c", f"{REGION}a", f"{REGION}b"]
OIDC_ARN = f"arn:aws:iam::{ACCOUNT}:oidc-provider/token.actions.githubusercontent.com"
STATE_KEY_ARN = f"arn:aws:kms:{REGION}:{ACCOUNT}:key/0f0e0d0c-0b0a-0908-0706-050403020100"
#: The secret RDS creates for a managed master password: `rds!db-<uuid>`, plus the random suffix.
MASTER_SECRET_ARN = f"arn:aws:secretsmanager:{REGION}:{ACCOUNT}:secret:rds!db-0a1b2c3d-XyZ123"


@dataclass(frozen=True)
class Registered:
    typ: str
    name: str
    inputs: dict[str, Any]
    provider: str

    def json(self, key: str) -> Any:
        """An input AWS receives as a JSON document — a policy, container definitions."""
        value = self.inputs[key]
        return json.loads(value) if isinstance(value, str) else value


@dataclass(frozen=True)
class Called:
    token: str
    args: dict[str, Any]
    provider: str


class Mocks(pulumi.runtime.Mocks):
    def __init__(self) -> None:
        self.resources: list[Registered] = []
        self.calls: list[Called] = []

    def reset(self) -> None:
        self.resources.clear()
        self.calls.clear()

    def of_type(self, typ: str) -> list[Registered]:
        return [r for r in self.resources if r.typ == typ]

    def named(self, typ: str, name: str) -> Registered:
        matches = [r for r in self.of_type(typ) if r.name == name]
        assert len(matches) == 1, f"{typ} {name!r}: {len(matches)} registered"
        return matches[0]

    def new_resource(self, args: pulumi.runtime.MockResourceArgs) -> tuple[str, dict[str, Any]]:
        self.resources.append(
            Registered(args.typ, args.name, dict(args.inputs), args.provider or "")
        )
        outputs = dict(args.inputs)
        name = str(args.inputs.get("name") or args.name)
        typ = args.typ
        if typ == "aws:iam/role:Role":
            outputs["arn"] = f"arn:aws:iam::{ACCOUNT}:role/{name}"
        elif typ == "aws:ecr/repository:Repository":
            outputs["arn"] = f"arn:aws:ecr:{REGION}:{ACCOUNT}:repository/{name}"
            outputs["repositoryUrl"] = f"{ACCOUNT}.dkr.ecr.{REGION}.amazonaws.com/{name}"
        elif typ == "aws:secretsmanager/secret:Secret":
            outputs["arn"] = f"arn:aws:secretsmanager:{REGION}:{ACCOUNT}:secret:{name}-AbCdEf"
        elif typ == "aws:secretsmanager/secretVersion:SecretVersion":
            # A UUID, as Secrets Manager's are, and the same one on every run.
            outputs["versionId"] = str(uuid.uuid5(uuid.NAMESPACE_URL, args.name))
        elif typ == "aws:ecs/cluster:Cluster":
            outputs["arn"] = f"arn:aws:ecs:{REGION}:{ACCOUNT}:cluster/{name}"
        elif typ == "aws:ecs/expressGatewayService:ExpressGatewayService":
            service = str(args.inputs.get("serviceName"))
            cluster = str(args.inputs.get("cluster"))
            outputs["serviceArn"] = f"arn:aws:ecs:{REGION}:{ACCOUNT}:service/{cluster}/{service}"
            # Express Mode reports a bare host name, which is why the component adds the scheme.
            outputs["ingressPaths"] = [
                {"accessType": "PUBLIC", "endpoint": f"{service}.ecs.{REGION}.on.aws"}
            ]
        elif typ == "aws:ecs/taskDefinition:TaskDefinition":
            family = str(args.inputs.get("family"))
            outputs["arn"] = f"arn:aws:ecs:{REGION}:{ACCOUNT}:task-definition/{family}:1"
            outputs["revision"] = 1
        elif typ == "aws:cloudwatch/logGroup:LogGroup":
            outputs["arn"] = f"arn:aws:logs:{REGION}:{ACCOUNT}:log-group:{name}"
        elif typ == "aws:rds/instance:Instance":
            identifier = str(args.inputs.get("identifier"))
            outputs["address"] = f"{identifier}.abcdefghijkl.{REGION}.rds.amazonaws.com"
            outputs["port"] = 5432
            outputs["resourceId"] = "db-ABCDEFGHIJKLMNOPQRSTUVWXYZ"
            if args.inputs.get("manageMasterUserPassword"):
                # The secret RDS creates and owns: `rds!db-<uuid>`, in the account's default key.
                outputs["masterUserSecrets"] = [
                    {
                        "secretArn": MASTER_SECRET_ARN,
                        "kmsKeyId": f"arn:aws:kms:{REGION}:{ACCOUNT}:alias/aws/secretsmanager",
                        "secretStatus": "active",
                    }
                ]
        return f"{args.name}-id", outputs

    def call(
        self, args: pulumi.runtime.MockCallArgs
    ) -> tuple[dict[str, Any], list[tuple[str, str]] | None]:
        self.calls.append(Called(args.token, dict(args.args), args.provider or ""))
        if args.token == "aws:index/getAvailabilityZones:getAvailabilityZones":
            return {"names": ZONES, "state": "available"}, []
        if args.token == "aws:iam/getOpenIdConnectProvider:getOpenIdConnectProvider":
            return {"arn": OIDC_ARN, "url": args.args.get("url")}, []
        if args.token == "aws:kms/getKey:getKey":
            return {"arn": STATE_KEY_ARN, "id": STATE_KEY_ARN.rsplit("/", 1)[-1]}, []
        return {}, []


MOCKS = Mocks()
pulumi.runtime.set_mocks(MOCKS, project=SLUG, stack="dev", preview=False)
