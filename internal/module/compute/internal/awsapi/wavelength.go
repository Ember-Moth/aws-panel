package awsapi

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/compute/internal/source"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/middleware"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"sort"
)

func (c Cloud) WavelengthZones(ctx context.Context, cfg aws.Config) ([]contract.WavelengthZone, error) {
	out, err := c.client(cfg, false).DescribeAvailabilityZones(ctx, &ec2sdk.DescribeAvailabilityZonesInput{AllAvailabilityZones: aws.Bool(true), Filters: []types.Filter{filter("zone-type", "wavelength-zone")}})
	if err != nil {
		return nil, err
	}
	rows := []contract.WavelengthZone{}
	seen := map[string]bool{}
	for _, v := range out.AvailabilityZones {
		if aws.ToString(v.ZoneType) != "wavelength-zone" || aws.ToString(v.RegionName) != cfg.Region {
			return nil, contract.ErrResponse
		}
		z := contract.WavelengthZone{ID: aws.ToString(v.ZoneId), Name: aws.ToString(v.ZoneName), Group: aws.ToString(v.GroupName), Region: cfg.Region, BorderGroup: aws.ToString(v.NetworkBorderGroup), State: string(v.State), OptInStatus: string(v.OptInStatus)}
		if z.ID == "" || z.Name == "" || z.Group == "" || z.BorderGroup == "" || seen[z.ID] {
			return nil, contract.ErrResponse
		}
		seen[z.ID] = true
		rows = append(rows, z)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}
func (c Cloud) WavelengthCatalogue(ctx context.Context, cfg aws.Config, id string) (contract.Catalogue, error) {
	zs, err := c.WavelengthZones(ctx, cfg)
	if err != nil {
		return contract.Catalogue{}, err
	}
	for _, z := range zs {
		if z.ID == id && z.State == "available" && z.OptInStatus == "opted-in" {
			return c.catalogue(ctx, cfg, &z)
		}
	}
	return contract.Catalogue{}, contract.ErrNetwork
}
func (c Cloud) EnableWavelength(ctx context.Context, cfg aws.Config, group string) (source.Accepted, error) {
	out, err := c.client(cfg, true).ModifyAvailabilityZoneGroup(ctx, &ec2sdk.ModifyAvailabilityZoneGroupInput{GroupName: aws.String(group), OptInStatus: types.ModifyAvailabilityZoneOptInStatusOptedIn})
	if err != nil {
		return source.Accepted{}, err
	}
	if !aws.ToBool(out.Return) {
		return source.Accepted{}, contract.ErrResponse
	}
	id, _ := middleware.GetRequestIDMetadata(out.ResultMetadata)
	return source.Accepted{ResourceID: group, RequestID: id}, nil
}
