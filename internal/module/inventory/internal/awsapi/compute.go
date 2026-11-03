package awsapi

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/addresses"
	ad "github.com/Ember-Moth/aws-panel/internal/module/addresses/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/internal/source"
	"github.com/aws/aws-sdk-go-v2/aws"
)

func computeResources(ctx context.Context, cfg aws.Config, req source.Request) (source.Snapshot, error) {
	out := source.Snapshot{Resources: []source.Resource{}, Pages: 1}
	observed := clock.Now()
	if req.Kind == contract.EC2Instance {
		rows, err := compute.NewCloud().Instances(ctx, cfg, req.AWSAccountID)
		if err != nil {
			return out, err
		}
		for _, v := range rows {
			out.Resources = append(out.Resources, source.Resource{ID: v.ARN, Name: v.Name, Region: req.Region, State: v.State, ObservedAt: observed, Details: contract.Details{EC2: &contract.EC2Details{CarrierIP: v.CarrierIP, ID: v.ID, ImageID: v.ImageID, InstanceType: v.Type, AvailabilityZone: v.Zone, VPCID: v.VPCID, SubnetID: v.SubnetID, SecurityGroupIDs: v.SecurityGroupIDs, PrimaryENI: v.PrimaryENI, PublicIP: v.PublicIP, PrivateIP: v.PrivateIP, IPv6: v.IPv6, RootVolumeID: v.RootVolumeID, DeleteRootOnTermination: v.DeleteRootOnTermination, CreatedAt: v.LaunchTime}}})
		}
		return out, nil
	}
	kind := "ec2"
	if req.Kind == contract.LightsailStaticIP {
		kind = "lightsail"
	}
	var rows ad.Catalogue
	var err error
	if kind == "ec2" {
		rows.Addresses, err = addresses.AllocatedEC2Addresses(ctx, cfg, req.AWSAccountID)
	} else {
		rows, err = addresses.NewCloud().Catalogue(ctx, cfg, kind, req.AWSAccountID)
	}
	if err != nil {
		return out, err
	}
	for _, v := range rows.Addresses {
		state := "unattached"
		if v.Attached {
			state = "attached"
		}
		out.Resources = append(out.Resources, source.Resource{ID: v.ARN, Name: v.Name, Region: req.Region, State: state, ObservedAt: observed, Details: contract.Details{Address: &contract.AddressDetails{Kind: v.Kind, ID: v.ID, IP: v.IP, InstanceID: v.InstanceID, NetworkInterfaceID: v.NetworkInterfaceID, AssociationID: v.AssociationID, BorderGroup: v.BorderGroup, Attached: v.Attached}}})
	}
	return out, nil
}
