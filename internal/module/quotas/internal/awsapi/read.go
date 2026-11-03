package awsapi

import (
	"context"
	reportdata "github.com/herta-network/aws-panel/internal/module/quotas/internal/report"
	"math"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	et "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	qt "github.com/aws/aws-sdk-go-v2/service/servicequotas/types"
	"github.com/herta-network/aws-panel/internal/infra/clock"
	"github.com/herta-network/aws-panel/internal/module/quotas/contract"
	"github.com/herta-network/aws-panel/internal/module/regions"
)

type Cloud struct{}

func pages(ctx context.Context, fetch func(*string) (string, error)) error {
	var token *string
	seen := map[string]bool{}
	for range 256 {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, err := fetch(token)
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

var classes = []string{"standard", "dl", "f", "gvt", "hpc", "high_memory", "inf", "p", "trn", "x"}
var classLabels = map[string]string{"standard": "标准实例", "dl": "DL 实例", "f": "F 实例", "gvt": "G / VT 实例", "hpc": "HPC 实例", "high_memory": "高内存实例", "inf": "Inf 实例", "p": "P 实例", "trn": "Trn 实例", "x": "X 实例"}

func InstanceClass(value string) string {
	family, _, _ := strings.Cut(strings.ToLower(value), ".")
	for _, prefix := range []string{"hpc", "inf", "trn", "dl"} {
		if strings.HasPrefix(family, prefix) {
			return prefix
		}
	}
	if strings.HasPrefix(family, "vt") {
		return "gvt"
	}
	if strings.HasPrefix(family, "u-") || (len(family) > 1 && family[0] == 'u' && family[1] >= '0' && family[1] <= '9') {
		return "high_memory"
	}
	if family == "" || strings.Contains(family, "mac") {
		return ""
	}
	switch family[0] {
	case 'f':
		return "f"
	case 'g':
		return "gvt"
	case 'p':
		return "p"
	case 'x':
		return "x"
	}
	if strings.ContainsRune("acdhimrtz", rune(family[0])) {
		return "standard"
	}
	return ""
}
func definition(service string) []contract.Entry {
	entries := []contract.Entry{}
	switch service {
	case "lightsail":
		entries = []contract.Entry{{Key: "lightsail.vcpu", Name: "Lightsail 实例 vCPU", Unit: "vCPU"}, {Key: "lightsail.static_ips", Name: "Lightsail 静态 IP", Unit: "个"}}
	case "ec2":
		for _, class := range classes {
			entries = append(entries, contract.Entry{Key: "ec2.ondemand." + class, Name: "按需 " + classLabels[class], Unit: "vCPU"})
		}
		entries = append(entries, contract.Entry{Key: "ec2.elastic_ips", Name: "EC2 弹性 IPv4 地址", Unit: "个"}, contract.Entry{Key: "ec2.carrier_ips", Name: "Wavelength Carrier IP", Unit: "个", Note: "按父区域汇总已分配地址；适用上限需在 AWS 核对，不使用普通弹性 IP 配额。"})
	case "vpc":
		entries = []contract.Entry{{Key: "vpc.vpcs", Name: "VPC", Unit: "个"}, {Key: "vpc.internet_gateways", Name: "互联网网关", Unit: "个"}}
	}
	return entries
}
func quotaKey(service string, q qt.ServiceQuota) string {
	name := strings.ToLower(aws.ToString(q.QuotaName))
	description := strings.ToLower(aws.ToString(q.Description) + " " + aws.ToString(q.Unit))
	if q.GlobalQuota {
		return ""
	}
	switch service {
	case "lightsail":
		if name == "instances" && (strings.Contains(description, "vcpu") || strings.Contains(description, "virtual cpu")) {
			return "lightsail.vcpu"
		}
		if strings.Contains(name, "static ip") {
			return "lightsail.static_ips"
		}
	case "ec2":
		if aws.ToString(q.QuotaCode) == "L-0263D0A3" || name == "ec2-vpc elastic ips" || strings.Contains(name, "elastic ip addresses per region") {
			return "ec2.elastic_ips"
		}
		if !strings.HasPrefix(name, "running on-demand ") {
			return ""
		}
		body := strings.TrimPrefix(name, "running on-demand ")
		switch {
		case strings.HasPrefix(body, "standard"):
			return "ec2.ondemand.standard"
		case strings.HasPrefix(body, "g and vt"):
			return "ec2.ondemand.gvt"
		case strings.HasPrefix(body, "high memory"):
			return "ec2.ondemand.high_memory"
		default:
			for _, class := range []string{"dl", "f", "hpc", "inf", "p", "trn", "x"} {
				if strings.HasPrefix(body, class+" ") {
					return "ec2.ondemand." + class
				}
			}
		}
	case "vpc":
		if name == "vpcs per region" {
			return "vpc.vpcs"
		}
		if name == "internet gateways per region" {
			return "vpc.internet_gateways"
		}
	}
	return ""
}
func (c Cloud) Read(ctx context.Context, cfg aws.Config, account, service string) (contract.Report, error) {
	limits, err := c.Limits(ctx, cfg, service)
	if err != nil || service == "bedrock" {
		return limits, err
	}
	usage, err := c.Usage(ctx, cfg, account, service)
	if err != nil {
		return limits, err
	}
	return reportdata.Merge(limits, usage), nil
}
func (Cloud) Limits(ctx context.Context, cfg aws.Config, service string) (contract.Report, error) {
	if service == "bedrock" {
		return bedrock(ctx, cfg)
	}
	entries := definition(service)
	if len(entries) == 0 {
		return contract.Report{}, contract.ErrInvalid
	}
	report := contract.Report{Region: cfg.Region, Service: service, Entries: entries, Warnings: []string{}}
	index := map[string]int{}
	for i, value := range report.Entries {
		index[value.Key] = i
		report.Entries[i].Note = "尚未取得该账号的有效配额"
	}
	client := servicequotas.NewFromConfig(cfg)
	err := pages(ctx, func(token *string) (string, error) {
		out, err := client.ListServiceQuotas(ctx, &servicequotas.ListServiceQuotasInput{ServiceCode: aws.String(service), MaxResults: aws.Int32(100), NextToken: token})
		if err != nil {
			return "", err
		}
		for _, quota := range out.Quotas {
			key := quotaKey(service, quota)
			i, ok := index[key]
			if !ok || quota.Value == nil || quota.ErrorReason != nil {
				continue
			}
			value := *quota.Value
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1e12 {
				return "", contract.ErrResponse
			}
			report.Entries[i].Limit = value
			report.Entries[i].LimitKnown = true
			report.Entries[i].QuotaCode = aws.ToString(quota.QuotaCode)
			report.Entries[i].Adjustable = quota.Adjustable
			report.Entries[i].Note = ""
		}
		return aws.ToString(out.NextToken), nil
	})
	report.LimitsRead = err == nil
	if err != nil {
		for i := range report.Entries {
			report.Entries[i].LimitKnown = false
			report.Entries[i].Note = "配额查询失败，请检查 Service Quotas 权限或连接"
		}
		report.Warnings = append(report.Warnings, "无法完整读取当前应用配额，未使用默认配额替代")
	}

	report.CheckedAt = clock.Now()
	return report, nil
}
func (Cloud) Usage(ctx context.Context, cfg aws.Config, account, service string) (contract.Report, error) {
	entries := definition(service)
	if len(entries) == 0 {
		return contract.Report{}, contract.ErrInvalid
	}
	report := contract.Report{Region: cfg.Region, Service: service, Entries: entries, Warnings: []string{}}
	index := map[string]int{}
	for i, v := range entries {
		index[v.Key] = i
	}
	var err error
	use := func(key string, value float64, err error) {
		i, ok := index[key]
		if !ok {
			return
		}
		if err != nil {
			report.Entries[i].Note = "用量无法完整核对，请检查资源读取权限或连接"
			return
		}
		report.Entries[i].Used = value
		report.Entries[i].UsageKnown = true
	}
	switch service {
	case "lightsail":
		ls := lightsail.NewFromConfig(cfg)
		var cpus float64
		err = pages(ctx, func(token *string) (string, error) {
			out, err := ls.GetInstances(ctx, &lightsail.GetInstancesInput{PageToken: token})
			if err != nil {
				return "", err
			}
			for _, instance := range out.Instances {
				if instance.Hardware == nil || instance.Hardware.CpuCount == nil || *instance.Hardware.CpuCount < 1 {
					return "", contract.ErrResponse
				}
				cpus += float64(*instance.Hardware.CpuCount)
			}
			return aws.ToString(out.NextPageToken), nil
		})
		use("lightsail.vcpu", cpus, err)
		var ips float64
		err = pages(ctx, func(token *string) (string, error) {
			out, err := ls.GetStaticIps(ctx, &lightsail.GetStaticIpsInput{PageToken: token})
			if err != nil {
				return "", err
			}
			ips += float64(len(out.StaticIps))
			return aws.ToString(out.NextPageToken), nil
		})
		use("lightsail.static_ips", ips, err)
	case "ec2":
		client := ec2.NewFromConfig(cfg)
		out, err := client.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{})
		if err == nil {
			count := float64(0)
			carrierCount := float64(0)
			var usageErr error
			for _, address := range out.Addresses {
				if aws.ToString(address.CarrierIp) != "" {
					if aws.ToString(address.AllocationId) == "" {
						usageErr = contract.ErrResponse
						break
					}
					carrierCount++
					continue
				}
				if aws.ToString(address.CustomerOwnedIp) != "" {
					continue
				}
				pool := aws.ToString(address.PublicIpv4Pool)
				if pool != "" && pool != "amazon" {
					continue
				}
				if aws.ToString(address.AllocationId) == "" {
					usageErr = contract.ErrResponse
					break
				}
				count++
			}
			use("ec2.elastic_ips", count, usageErr)
			use("ec2.carrier_ips", carrierCount, usageErr)
		} else {
			use("ec2.elastic_ips", 0, err)
			use("ec2.carrier_ips", 0, err)
		}
		counts, err := ec2Usage(ctx, client, account)
		for _, class := range classes {
			use("ec2.ondemand."+class, counts[class], err)
		}
		if err == nil {
			report.Warnings = append(report.Warnings, "按需 vCPU 用量包含运行中实例与本账号容量预留，排除 Spot、专用主机和未运行实例；查询并不预留配额")
		}
	case "vpc":
		client := ec2.NewFromConfig(cfg)
		var vpcs, gateways float64
		err = pages(ctx, func(token *string) (string, error) {
			out, err := client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{NextToken: token, MaxResults: aws.Int32(1000)})
			if err != nil {
				return "", err
			}
			vpcs += float64(len(out.Vpcs))
			return aws.ToString(out.NextToken), nil
		})
		use("vpc.vpcs", vpcs, err)
		err = pages(ctx, func(token *string) (string, error) {
			out, err := client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{NextToken: token, MaxResults: aws.Int32(1000)})
			if err != nil {
				return "", err
			}
			gateways += float64(len(out.InternetGateways))
			return aws.ToString(out.NextToken), nil
		})
		use("vpc.internet_gateways", gateways, err)
	}
	for i := range report.Entries {
		row := &report.Entries[i]
		if row.LimitKnown && row.UsageKnown {
			row.Available = math.Max(0, row.Limit-row.Used)
		}
	}
	report.CheckedAt = clock.Now()
	return report, nil
}
func ec2Usage(ctx context.Context, client *ec2.Client, account string) (map[string]float64, error) {
	counts := map[string]int64{}
	reservations := map[string]bool{}
	err := pages(ctx, func(token *string) (string, error) {
		out, err := client.DescribeCapacityReservations(ctx, &ec2.DescribeCapacityReservationsInput{NextToken: token, MaxResults: aws.Int32(1000), Filters: []et.Filter{{Name: aws.String("owner-id"), Values: []string{account}}, {Name: aws.String("state"), Values: []string{"active"}}}})
		if err != nil {
			return "", err
		}
		for _, row := range out.CapacityReservations {
			if aws.ToString(row.CapacityReservationId) == "" || aws.ToString(row.OwnerId) != account || row.TotalInstanceCount == nil || *row.TotalInstanceCount < 0 {
				return "", contract.ErrResponse
			}
			if string(row.ReservationType) != "" && string(row.ReservationType) != "default" {
				return "", contract.ErrResponse
			}
			reservations[aws.ToString(row.CapacityReservationId)] = true
			counts[aws.ToString(row.InstanceType)] += int64(*row.TotalInstanceCount)
		}
		return aws.ToString(out.NextToken), nil
	})
	if err != nil {
		return nil, err
	}
	seenInstances := map[string]bool{}
	err = pages(ctx, func(token *string) (string, error) {
		out, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{NextToken: token, MaxResults: aws.Int32(1000), Filters: []et.Filter{{Name: aws.String("instance-state-name"), Values: []string{"running"}}}})
		if err != nil {
			return "", err
		}
		for _, reservation := range out.Reservations {
			for _, row := range reservation.Instances {
				if row.State == nil || aws.ToString(row.InstanceId) == "" || seenInstances[aws.ToString(row.InstanceId)] {
					return "", contract.ErrResponse
				}
				seenInstances[aws.ToString(row.InstanceId)] = true
				if row.State.Name != et.InstanceStateNameRunning || row.InstanceLifecycle != "" {
					continue
				}
				if row.Placement != nil && row.Placement.Tenancy == et.TenancyHost {
					continue
				}
				if reservations[aws.ToString(row.CapacityReservationId)] {
					continue
				}
				counts[string(row.InstanceType)]++
			}
		}
		return aws.ToString(out.NextToken), nil
	})
	if err != nil {
		return nil, err
	}
	kinds := []et.InstanceType{}
	for name, count := range counts {
		if count > 0 {
			kinds = append(kinds, et.InstanceType(name))
		}
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	cpus := map[string]int64{}
	for start := 0; start < len(kinds); start += 100 {
		end := min(start+100, len(kinds))
		out, err := client.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{InstanceTypes: kinds[start:end]})
		if err != nil {
			return nil, err
		}
		for _, row := range out.InstanceTypes {
			if row.VCpuInfo == nil || row.VCpuInfo.DefaultVCpus == nil || *row.VCpuInfo.DefaultVCpus < 1 {
				return nil, contract.ErrResponse
			}
			cpus[string(row.InstanceType)] = int64(*row.VCpuInfo.DefaultVCpus)
		}
	}
	result := map[string]float64{}
	for name, count := range counts {
		if count == 0 {
			continue
		}
		cpu, ok := cpus[name]
		class := InstanceClass(name)
		if !ok || class == "" {
			return nil, contract.ErrResponse
		}
		result[class] += float64(cpu * count)
	}
	return result, nil
}

func (Cloud) Regions(ctx context.Context, cfg aws.Config) ([]contract.Region, error) {
	cfg.Region = "us-east-1"
	rows, err := regions.NewCloud().List(ctx, cfg)
	if err != nil {
		return nil, err
	}
	result := []contract.Region{}
	for _, row := range rows {
		result = append(result, contract.Region{Code: row.Region, State: row.Status, Available: row.Status == "ENABLED" || row.Status == "ENABLED_BY_DEFAULT"})
	}
	return result, nil
}
