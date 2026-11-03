package awsapi

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	awsnet "github.com/herta-network/aws-panel/internal/infra/aws"
	"github.com/herta-network/aws-panel/internal/module/dashboard/contract"
	"github.com/herta-network/aws-panel/internal/module/dashboard/internal/source"
	"github.com/herta-network/aws-panel/internal/module/quotas"
	"github.com/herta-network/aws-panel/internal/module/regions"
	"net/netip"
	"sort"
	"strings"
	"sync"
)

type Cloud struct{}

func pages(ctx context.Context, fn func(*string) (string, error)) error {
	seen := map[string]bool{}
	var token *string
	for range 256 {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, err := fn(token)
		if err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		if seen[next] {
			return contract.ErrResponse
		}
		seen[next] = true
		token = aws.String(next)
	}
	return contract.ErrResponse
}
func (c Cloud) Catalogue(ctx context.Context, cfg aws.Config) (source.Catalogue, error) {
	cfg.Region = "us-east-1"
	states, err := regions.NewCloud().List(ctx, cfg)
	if err != nil {
		return source.Catalogue{}, err
	}
	out := source.Catalogue{Regions: []string{}, LightsailRegions: []string{}}
	seen := map[string]bool{}
	for _, v := range states {
		if v.Status != "ENABLED" && v.Status != "ENABLED_BY_DEFAULT" {
			continue
		}
		if seen[v.Region] || v.Region == "" {
			return out, contract.ErrResponse
		}
		seen[v.Region] = true
		out.Regions = append(out.Regions, v.Region)
	}
	if len(out.Regions) > 80 {
		return out, contract.ErrResponse
	}
	sort.Strings(out.Regions)
	ls, err := lightsail.NewFromConfig(cfg).GetRegions(ctx, &lightsail.GetRegionsInput{})
	if err == nil {
		out.LightsailKnown = true
		for _, v := range ls.Regions {
			if seen[string(v.Name)] {
				out.LightsailRegions = append(out.LightsailRegions, string(v.Name))
			}
		}
		sort.Strings(out.LightsailRegions)
	}
	return out, nil
}
func validARN(value, region, account, kind string) bool {
	a, err := arn.Parse(value)
	return err == nil && a.Partition == "aws" && a.Service == "lightsail" && a.Region == region && a.AccountID == account && strings.HasPrefix(a.Resource, kind+"/")
}
func addIP(set map[string]bool, value string) error {
	if value == "" {
		return nil
	}
	ip, err := netip.ParseAddr(value)
	if err != nil || !ip.Is4() {
		return contract.ErrResponse
	}
	set[ip.String()] = true
	return nil
}
func ipList(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func (c Cloud) resources(ctx context.Context, cfg aws.Config, account, kind string) (contract.Data, error) {
	data := contract.Data{}
	ips := map[string]bool{}
	ids := map[string]bool{}
	var err error
	switch kind {
	case contract.EC2Instances:
		client := ec2.NewFromConfig(cfg)
		err = pages(ctx, func(token *string) (string, error) {
			out, e := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{NextToken: token, MaxResults: aws.Int32(1000)})
			if e != nil {
				return "", e
			}
			for _, res := range out.Reservations {
				if aws.ToString(res.OwnerId) != account {
					return "", contract.ErrResponse
				}
				for _, v := range res.Instances {
					id := aws.ToString(v.InstanceId)
					if id == "" || ids[id] || v.State == nil {
						return "", contract.ErrResponse
					}
					ids[id] = true
					if string(v.State.Name) == "terminated" {
						continue
					}
					data.Count++
					if string(v.State.Name) == "running" {
						data.Running++
					}
					if e := addIP(ips, aws.ToString(v.PublicIpAddress)); e != nil {
						return "", e
					}
				}
			}
			return aws.ToString(out.NextToken), nil
		})
	case contract.EC2IPs:
		out, e := ec2.NewFromConfig(cfg).DescribeAddresses(ctx, &ec2.DescribeAddressesInput{})
		err = e
		if e == nil {
			for _, v := range out.Addresses {
				id := aws.ToString(v.AllocationId)
				if id == "" || ids[id] {
					return data, contract.ErrResponse
				}
				ids[id] = true
				data.Count++
				if e := addIP(ips, aws.ToString(v.PublicIp)); e != nil {
					return data, e
				}
			}
		}
	case contract.LightsailInstances:
		client := lightsail.NewFromConfig(cfg)
		err = pages(ctx, func(token *string) (string, error) {
			out, e := client.GetInstances(ctx, &lightsail.GetInstancesInput{PageToken: token})
			if e != nil {
				return "", e
			}
			for _, v := range out.Instances {
				id := aws.ToString(v.Arn)
				if !validARN(id, cfg.Region, account, "Instance") || ids[id] || v.State == nil {
					return "", contract.ErrResponse
				}
				ids[id] = true
				data.Count++
				if aws.ToString(v.State.Name) == "running" {
					data.Running++
				}
				if e := addIP(ips, aws.ToString(v.PublicIpAddress)); e != nil {
					return "", e
				}
			}
			return aws.ToString(out.NextPageToken), nil
		})
	case contract.LightsailIPs:
		client := lightsail.NewFromConfig(cfg)
		err = pages(ctx, func(token *string) (string, error) {
			out, e := client.GetStaticIps(ctx, &lightsail.GetStaticIpsInput{PageToken: token})
			if e != nil {
				return "", e
			}
			for _, v := range out.StaticIps {
				id := aws.ToString(v.Arn)
				if !validARN(id, cfg.Region, account, "StaticIp") || ids[id] {
					return "", contract.ErrResponse
				}
				ids[id] = true
				data.Count++
				if e := addIP(ips, aws.ToString(v.IpAddress)); e != nil {
					return "", e
				}
			}
			return aws.ToString(out.NextPageToken), nil
		})
	default:
		return data, contract.ErrInvalid
	}
	data.IPv4 = ipList(ips)
	return data, err
}
func (c Cloud) ReadRegion(ctx context.Context, cfg aws.Config, account string, ls bool) ([]source.Observation, error) {
	kinds := []string{contract.EC2Instances, contract.EC2IPs, contract.EC2Quotas, contract.BedrockQuotas}
	if ls {
		kinds = append(kinds, contract.LightsailInstances, contract.LightsailIPs, contract.LightsailQuotas)
	}
	out := make([]source.Observation, len(kinds))
	var group sync.WaitGroup
	gate := make(chan struct{}, 2)
	for index, kind := range kinds {
		group.Go(func() {
			select {
			case gate <- struct{}{}:
			case <-ctx.Done():
				out[index] = source.Observation{Kind: kind, Code: "COLLECTION_TIMEOUT"}
				return
			}
			defer func() { <-gate }()
			result := source.Observation{Kind: kind}
			var err error
			switch kind {
			case contract.EC2Quotas, contract.LightsailQuotas, contract.BedrockQuotas:
				service := map[string]string{contract.EC2Quotas: "ec2", contract.LightsailQuotas: "lightsail", contract.BedrockQuotas: "bedrock"}[kind]
				report, e := quotas.NewCloud().Read(ctx, cfg, account, service)
				err = e
				if e == nil && report.LimitsRead {
					result.OK = true
					for _, q := range report.Entries {
						result.Data.Quotas = append(result.Data.Quotas, contract.Quota{Key: q.Key, Name: q.Name, Code: q.QuotaCode, Unit: q.Unit, Period: q.Period, Global: q.Global, Limit: q.Limit, Used: q.Used, LimitKnown: q.LimitKnown, UsageKnown: q.UsageKnown})
					}
				} else {
					result.Code = "QUOTA_READ_INCOMPLETE"
				}
			default:
				result.Data, err = c.resources(ctx, cfg, account, kind)
				result.OK = err == nil
			}
			if err != nil {
				result.Code = awsnet.ClassifyError(err).Code
			}
			out[index] = result
		})
	}
	group.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, nil
}
