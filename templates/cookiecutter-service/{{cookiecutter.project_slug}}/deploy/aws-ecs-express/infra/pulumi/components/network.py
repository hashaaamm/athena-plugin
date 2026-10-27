"""The VPC, in Express Mode's own shape.

Express Mode puts its Application Load Balancer and the service's tasks **in the same subnets**:
give it public subnets and the load balancer is internet-facing, give it private ones and it is
internal. So the subnets the tasks run in are public, the tasks get public IPs, and there is **no
NAT gateway**. That reads as a mistake and is not. A task needs outbound access for exactly three
things — pulling its image, reading its secrets from Secrets Manager and writing to CloudWatch Logs
— and a public IP gives it that without one, where a NAT gateway is a standing hourly charge plus a
per-gigabyte one, which on a small service is more than the tasks. Inbound, a public IP admits
nothing a security group does not: Express Mode attaches its own service security group, which
admits only its load balancer, and ours (`app`) has no ingress rules at all.

The database is the one thing that gets no route out: the isolated subnets have a route table with
no default route, and its security group admits Postgres from the `app` group and nothing else.
"""

from __future__ import annotations

from functools import partial

import pulumi
import pulumi_aws as aws

from config import StackConfig

#: One /16, carved into /24s: public tier at 10.0.0-2, isolated tier at 10.0.100-102.
VPC_CIDR = "10.0.0.0/16"
POSTGRES_PORT = 5432


def _zone(names: list[str], index: int, count: int) -> str:
    """The `index`-th of the first `count` available zones, sorted.

    Sorted so the choice is the same on every run: the API's order is not a promise, and a subnet
    whose zone changes is a subnet Pulumi replaces.
    """
    zones = sorted(names)
    if len(zones) < count:
        raise ValueError(
            f"availabilityZoneCount is {count} but the region has only {len(zones)} available"
        )
    return zones[index]


class Network(pulumi.ComponentResource):
    def __init__(
        self,
        config: StackConfig,
        *,
        zone_names: pulumi.Input[list[str]],
        database: bool = False,
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        super().__init__("service:infra:Network", config.name("network"), None, opts)
        child = pulumi.ResourceOptions(parent=self)
        zones = pulumi.Output.from_input(zone_names)
        count = config.availability_zone_count

        self.vpc = aws.ec2.Vpc(
            "vpc",
            cidr_block=VPC_CIDR,
            # Support is the VPC's own resolver: without it a task cannot resolve ECR, Secrets
            # Manager or the database endpoint. Hostnames is what RDS in a VPC is documented to
            # need. Both are free.
            enable_dns_support=True,
            enable_dns_hostnames=True,
            tags={"Name": config.name(config.slug)},
            opts=child,
        )

        gateway = aws.ec2.InternetGateway(
            "internet-gateway",
            vpc_id=self.vpc.id,
            tags={"Name": config.name(config.slug)},
            opts=child,
        )

        public_routes = aws.ec2.RouteTable(
            "public-routes",
            vpc_id=self.vpc.id,
            tags={"Name": config.name(config.slug, "public")},
            opts=child,
        )
        aws.ec2.Route(
            "public-default-route",
            route_table_id=public_routes.id,
            destination_cidr_block="0.0.0.0/0",
            gateway_id=gateway.id,
            opts=child,
        )

        self.public_subnets: list[aws.ec2.Subnet] = []
        for index in range(count):
            subnet = aws.ec2.Subnet(
                f"public-{index}",
                vpc_id=self.vpc.id,
                cidr_block=f"10.0.{index}.0/24",
                availability_zone=zones.apply(partial(_zone, index=index, count=count)),
                # The tasks need a public IP to reach ECR, Secrets Manager and CloudWatch without a
                # NAT gateway. See this module's docstring.
                map_public_ip_on_launch=True,
                tags={"Name": config.name(config.slug, "public", str(index))},
                opts=child,
            )
            aws.ec2.RouteTableAssociation(
                f"public-{index}-routes",
                subnet_id=subnet.id,
                route_table_id=public_routes.id,
                opts=child,
            )
            self.public_subnets.append(subnet)

        #: Our identity for the service's tasks, and the one thing the database trusts. No ingress
        #: rules: Express Mode attaches a security group of its own that admits its load balancer,
        #: and nothing else should reach a task directly.
        self.app_security_group = aws.ec2.SecurityGroup(
            "app",
            vpc_id=self.vpc.id,
            name=config.name(config.slug, "app"),
            description=f"{config.slug} tasks: no ingress, egress anywhere",
            opts=child,
        )
        # Egress is a rule of its own rather than inline, because AWS's default allow-all egress
        # rule is removed when the provider creates a group: anything this group may send has to be
        # said here. Anywhere, because ECR, Secrets Manager and CloudWatch are public endpoints.
        aws.vpc.SecurityGroupEgressRule(
            "app-egress",
            security_group_id=self.app_security_group.id,
            ip_protocol="-1",
            cidr_ipv4="0.0.0.0/0",
            description="ECR, Secrets Manager, CloudWatch Logs and the database",
            opts=child,
        )

        self.isolated_subnets: list[aws.ec2.Subnet] = []
        self.db_security_group: aws.ec2.SecurityGroup | None = None
        if database:
            # A route table with no route out of the VPC. A database in these subnets has nowhere
            # to send a packet except back into the VPC, whatever its security group says.
            isolated_routes = aws.ec2.RouteTable(
                "isolated-routes",
                vpc_id=self.vpc.id,
                tags={"Name": config.name(config.slug, "isolated")},
                opts=child,
            )
            for index in range(count):
                subnet = aws.ec2.Subnet(
                    f"isolated-{index}",
                    vpc_id=self.vpc.id,
                    cidr_block=f"10.0.{100 + index}.0/24",
                    availability_zone=zones.apply(partial(_zone, index=index, count=count)),
                    map_public_ip_on_launch=False,
                    tags={"Name": config.name(config.slug, "isolated", str(index))},
                    opts=child,
                )
                aws.ec2.RouteTableAssociation(
                    f"isolated-{index}-routes",
                    subnet_id=subnet.id,
                    route_table_id=isolated_routes.id,
                    opts=child,
                )
                self.isolated_subnets.append(subnet)

            # Postgres from the app group, and nothing else in or out. No egress rule at all: a
            # database answers on the connection it was given and never opens one of its own.
            self.db_security_group = aws.ec2.SecurityGroup(
                "db",
                vpc_id=self.vpc.id,
                name=config.name(config.slug, "db"),
                description=f"{config.slug} database: Postgres from the app group only",
                opts=child,
            )
            aws.vpc.SecurityGroupIngressRule(
                "db-from-app",
                security_group_id=self.db_security_group.id,
                referenced_security_group_id=self.app_security_group.id,
                ip_protocol="tcp",
                from_port=POSTGRES_PORT,
                to_port=POSTGRES_PORT,
                description="Postgres from the service and its migration task",
                opts=child,
            )

        self.public_subnet_ids = pulumi.Output.all(*[s.id for s in self.public_subnets])
        self.isolated_subnet_ids = pulumi.Output.all(*[s.id for s in self.isolated_subnets])
        self.register_outputs(
            {
                "vpc_id": self.vpc.id,
                "public_subnet_ids": self.public_subnet_ids,
                "app_security_group_id": self.app_security_group.id,
            }
        )
