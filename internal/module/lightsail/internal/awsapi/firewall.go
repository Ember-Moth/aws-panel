package awsapi

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/module/lightsail/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/lightsail/internal/source"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/middleware"
	sdk "github.com/aws/aws-sdk-go-v2/service/lightsail"
	lt "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
	"slices"
)

func (Cloud) FirewallOpen(ctx context.Context, cfg aws.Config, name, ipType string) (bool, error) {
	out, err := sdk.NewFromConfig(cfg).GetInstancePortStates(ctx, &sdk.GetInstancePortStatesInput{InstanceName: aws.String(name)})
	if err != nil {
		return false, err
	}
	v4, v6 := false, false
	for _, p := range out.PortStates {
		if p.State != lt.PortStateOpen || p.Protocol != lt.NetworkProtocolAll {
			continue
		}
		from, to := p.FromPort, p.ToPort
		if !((from == 0 && to == 65535) || (from == -1 && to == -1)) {
			continue
		}
		v4 = v4 || slices.Contains(p.Cidrs, "0.0.0.0/0")
		v6 = v6 || slices.Contains(p.Ipv6Cidrs, "::/0")
	}
	return (ipType == "ipv6" || v4) && (ipType == "ipv4" || v6), nil
}
func (Cloud) OpenFirewall(ctx context.Context, cfg aws.Config, name, ipType string) (source.Created, error) {
	if ipType != "ipv4" && ipType != "ipv6" && ipType != "dualstack" {
		return source.Created{}, contract.ErrInvalid
	}
	cfg.Retryer = func() aws.Retryer { return aws.NopRetryer{} }
	rule := lt.PortInfo{Protocol: lt.NetworkProtocolAll, FromPort: 0, ToPort: 65535, Cidrs: []string{}, Ipv6Cidrs: []string{}}
	if ipType != "ipv6" {
		rule.Cidrs = []string{"0.0.0.0/0"}
	}
	if ipType != "ipv4" {
		rule.Ipv6Cidrs = []string{"::/0"}
	}
	out, err := sdk.NewFromConfig(cfg).PutInstancePublicPorts(ctx, &sdk.PutInstancePublicPortsInput{InstanceName: aws.String(name), PortInfos: []lt.PortInfo{rule}})
	if err != nil {
		return source.Created{}, err
	}
	if out.Operation == nil {
		return source.Created{}, contract.ErrResponse
	}
	result := source.Created{Operations: []source.Operation{operation(*out.Operation)}}
	result.RequestID, _ = middleware.GetRequestIDMetadata(out.ResultMetadata)
	return result, nil
}
