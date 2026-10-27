package components

import (
	"fmt"
	"slices"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/vpc"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// The address plan. A /16 for the VPC, a /24 per subnet: public subnets count up from 10.0.0.0,
// isolated ones from 10.0.100.0, so a third AZ — or a fourth subnet tier later — never has to
// renumber anything already allocated.
const (
	VPCCidr            = "10.0.0.0/16"
	publicCidrFormat   = "10.0.%d.0/24"
	isolatedCidrFormat = "10.0.%d.0/24"
	isolatedCidrOffset = 100

	// PostgresPort is the one port the database security group admits.
	PostgresPort = 5432
)

// Network is the VPC everything in this stack runs in, in the shape Express Mode expects.
//
// Express Mode puts its Application Load Balancer and the service's tasks in the SAME subnets, and
// picks the load balancer's scheme from them: public subnets give an internet-facing ALB, private
// subnets an internal one. A public API therefore runs its tasks in public subnets, with public
// IPs — which is also how they reach ECR, Secrets Manager and CloudWatch Logs without a NAT
// gateway. A NAT gateway is an hourly charge per AZ plus a charge per gigabyte, and would be the
// largest line on a small service's bill; VPC endpoints for the same three services cost more
// again. What keeps a task with a public IP unreachable is the security group, not the subnet:
// see AppSecurityGroup.
//
// The database is the one thing that must not be on the internet at all, so it gets subnets of its
// own — isolated, with a route table that has no route out. Not "private": a private subnet has a
// NAT route; these have nothing but the VPC's local route.
type Network struct {
	pulumi.ResourceState

	VPC *ec2.Vpc

	// PublicSubnetIDs is where the Express services and the migration task run.
	PublicSubnetIDs pulumi.StringArray
	// IsolatedSubnetIDs is where the database lives, and nothing else.
	IsolatedSubnetIDs pulumi.StringArray

	// AppSecurityGroup is the identity every task in this stack carries. It has no ingress rule
	// of its own: Express Mode attaches a second, service-specific group that admits traffic from
	// its load balancer only. This one is what the database trusts.
	AppSecurityGroup *ec2.SecurityGroup
	// DBSecurityGroup admits Postgres from AppSecurityGroup, and nothing else from anywhere.
	DBSecurityGroup *ec2.SecurityGroup
}

// NewNetwork creates the VPC, its subnets and routes, and the two security groups.
func NewNetwork(ctx *pulumi.Context, cfg stackconfig.StackConfig, opts ...pulumi.ResourceOption) (*Network, error) {
	c := &Network{}
	if err := ctx.RegisterComponentResource("service:infra:Network", cfg.Name("network"), c, opts...); err != nil {
		return nil, err
	}

	// The first N available zones, by name. Sorted, because the API does not promise an order and
	// a subnet whose zone moved between two runs is a subnet Pulumi replaces — along with the
	// database in it. "available" excludes zones that are impaired or that this account has not
	// opted into.
	zones := aws.GetAvailabilityZonesOutput(ctx, aws.GetAvailabilityZonesOutputArgs{
		State: pulumi.String("available"),
	}, invokeOpts(opts)...).Names().ApplyT(func(names []string) ([]string, error) {
		names = slices.Sorted(slices.Values(names))
		if len(names) < cfg.AvailabilityZoneCount {
			return nil, fmt.Errorf("%s has %d available zones; availabilityZoneCount is %d",
				cfg.Region, len(names), cfg.AvailabilityZoneCount)
		}
		return names[:cfg.AvailabilityZoneCount], nil
	}).(pulumi.StringArrayOutput)

	var err error
	c.VPC, err = ec2.NewVpc(ctx, "vpc", &ec2.VpcArgs{
		CidrBlock: pulumi.String(VPCCidr),
		// Both on, or RDS endpoints and the ECR/Secrets Manager hostnames do not resolve from
		// inside the VPC.
		EnableDnsSupport:   pulumi.Bool(true),
		EnableDnsHostnames: pulumi.Bool(true),
		Tags:               nameTag(cfg.Name(cfg.Slug)),
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	gateway, err := ec2.NewInternetGateway(ctx, "igw", &ec2.InternetGatewayArgs{
		VpcId: c.VPC.ID(),
		Tags:  nameTag(cfg.Name(cfg.Slug)),
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	public, err := ec2.NewRouteTable(ctx, "public", &ec2.RouteTableArgs{
		VpcId: c.VPC.ID(),
		Tags:  nameTag(cfg.Name(cfg.Slug, "public")),
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}
	// A standalone route rather than an inline one: inline routes and Route resources on one
	// table fight each other, and a standalone one is a line in the preview of its own.
	if _, err := ec2.NewRoute(ctx, "public-internet", &ec2.RouteArgs{
		RouteTableId:         public.ID(),
		DestinationCidrBlock: pulumi.String("0.0.0.0/0"),
		GatewayId:            gateway.ID(),
	}, child(c, opts)...); err != nil {
		return nil, err
	}

	// No routes at all. The VPC's local route is implicit, so the database can answer the tasks
	// and nothing can answer the database.
	isolated, err := ec2.NewRouteTable(ctx, "isolated", &ec2.RouteTableArgs{
		VpcId: c.VPC.ID(),
		Tags:  nameTag(cfg.Name(cfg.Slug, "isolated")),
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	for i := range cfg.AvailabilityZoneCount {
		zone := zones.Index(pulumi.Int(i))

		subnet, err := ec2.NewSubnet(ctx, fmt.Sprintf("public-%d", i), &ec2.SubnetArgs{
			VpcId:            c.VPC.ID(),
			CidrBlock:        pulumi.String(fmt.Sprintf(publicCidrFormat, i)),
			AvailabilityZone: zone,
			// Without a public IP a task in a subnet with no NAT cannot pull its image.
			MapPublicIpOnLaunch: pulumi.Bool(true),
			Tags:                nameTag(cfg.Name(cfg.Slug, fmt.Sprintf("public-%d", i))),
		}, child(c, opts)...)
		if err != nil {
			return nil, err
		}
		if _, err := ec2.NewRouteTableAssociation(ctx, fmt.Sprintf("public-%d", i), &ec2.RouteTableAssociationArgs{
			SubnetId:     subnet.ID(),
			RouteTableId: public.ID(),
		}, child(c, opts)...); err != nil {
			return nil, err
		}
		c.PublicSubnetIDs = append(c.PublicSubnetIDs, subnet.ID())

		subnet, err = ec2.NewSubnet(ctx, fmt.Sprintf("isolated-%d", i), &ec2.SubnetArgs{
			VpcId:               c.VPC.ID(),
			CidrBlock:           pulumi.String(fmt.Sprintf(isolatedCidrFormat, isolatedCidrOffset+i)),
			AvailabilityZone:    zone,
			MapPublicIpOnLaunch: pulumi.Bool(false),
			Tags:                nameTag(cfg.Name(cfg.Slug, fmt.Sprintf("isolated-%d", i))),
		}, child(c, opts)...)
		if err != nil {
			return nil, err
		}
		if _, err := ec2.NewRouteTableAssociation(ctx, fmt.Sprintf("isolated-%d", i), &ec2.RouteTableAssociationArgs{
			SubnetId:     subnet.ID(),
			RouteTableId: isolated.ID(),
		}, child(c, opts)...); err != nil {
			return nil, err
		}
		c.IsolatedSubnetIDs = append(c.IsolatedSubnetIDs, subnet.ID())
	}

	// The provider removes the allow-all egress rule AWS adds to every new group, so a group
	// declared here starts with no rules at all and has exactly the ones below.
	c.AppSecurityGroup, err = ec2.NewSecurityGroup(ctx, "app", &ec2.SecurityGroupArgs{
		VpcId:       c.VPC.ID(),
		Name:        pulumi.String(cfg.Name(cfg.Slug, "app")),
		Description: pulumi.String("Tasks of " + cfg.Slug + ". No ingress: Express Mode adds its own load-balancer-only group."),
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}
	// Out to anywhere: ECR, Secrets Manager, CloudWatch Logs and Sentry are all public endpoints,
	// and with no NAT the task's own public IP is the way there.
	if _, err := vpc.NewSecurityGroupEgressRule(ctx, "app-egress", &vpc.SecurityGroupEgressRuleArgs{
		SecurityGroupId: c.AppSecurityGroup.ID(),
		IpProtocol:      pulumi.String("-1"),
		CidrIpv4:        pulumi.String("0.0.0.0/0"),
		Description:     pulumi.String("Image pulls, secrets, logs and outbound calls, without a NAT gateway"),
	}, child(c, opts)...); err != nil {
		return nil, err
	}

	c.DBSecurityGroup, err = ec2.NewSecurityGroup(ctx, "db", &ec2.SecurityGroupArgs{
		VpcId:       c.VPC.ID(),
		Name:        pulumi.String(cfg.Name(cfg.Slug, "db")),
		Description: pulumi.String("Postgres for " + cfg.Slug + ", from its tasks only"),
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}
	// By group, not by CIDR. A CIDR rule admits whatever lands in those subnets next; a group
	// rule admits exactly the tasks this stack starts. No egress rule: a database that answers
	// connections needs none, and one that can open its own is one more way out.
	if _, err := vpc.NewSecurityGroupIngressRule(ctx, "db-from-app", &vpc.SecurityGroupIngressRuleArgs{
		SecurityGroupId:           c.DBSecurityGroup.ID(),
		ReferencedSecurityGroupId: c.AppSecurityGroup.ID(),
		IpProtocol:                pulumi.String("tcp"),
		FromPort:                  pulumi.Int(PostgresPort),
		ToPort:                    pulumi.Int(PostgresPort),
		Description:               pulumi.String("Postgres from the app tasks"),
	}, child(c, opts)...); err != nil {
		return nil, err
	}

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{
		"vpcId":             c.VPC.ID(),
		"publicSubnetIds":   c.PublicSubnetIDs,
		"isolatedSubnetIds": c.IsolatedSubnetIDs,
	})
}

// nameTag is the Name tag the console shows. Everything else — app, environment, managed-by,
// pulumi-stack — comes from the provider's default tags.
func nameTag(name string) pulumi.StringMap {
	return pulumi.StringMap{"Name": pulumi.String(name)}
}

// invokeOpts carries the explicit provider from a component's resource options onto an invoke.
// A data source looked up through the default provider would fail outright — Pulumi.yaml disables
// it — so every invoke goes through the same provider as every resource.
func invokeOpts(opts []pulumi.ResourceOption) []pulumi.InvokeOption {
	var out []pulumi.InvokeOption
	for _, opt := range opts {
		if invoke, ok := opt.(pulumi.InvokeOption); ok {
			out = append(out, invoke)
		}
	}
	return out
}
