package scope

import (
	"regexp"
	"sort"
	"strings"

	"github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	"github.com/google/uuid"
)

const Operation = "inventory.refresh"

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)

func RegionOK(value string) bool {
	return len(value) <= 40 && regionPattern.MatchString(value) && !strings.HasPrefix(value, "cn-") && !strings.HasPrefix(value, "us-gov-")
}
func KindOK(value string) bool {
	return value == contract.AccountRegion || value == contract.LightsailRegion || value == contract.LightsailInstance || value == contract.LightsailStaticIP || value == contract.EC2Instance || value == contract.EC2Address
}
func Regional(kind string) bool {
	return kind != contract.AccountRegion && kind != contract.LightsailRegion
}
func Region(kind, region string) string {
	if !Regional(kind) {
		return "global"
	}
	return region
}
func InputKey(target contract.RefreshTarget) string {
	return target.ConnectionID + "/" + target.Kind + "/" + target.Region
}
func Key(account, kind, region string) string {
	return account + "/" + kind + "/" + Region(kind, region)
}
func Label(kind string) string {
	switch kind {
	case contract.AccountRegion:
		return "区域启用状态"
	case contract.LightsailRegion:
		return "Lightsail 可用区域"
	case contract.LightsailInstance:
		return "Lightsail 实例"
	case contract.LightsailStaticIP:
		return "Lightsail 静态 IP"
	case contract.EC2Instance:
		return "EC2 实例"
	case contract.EC2Address:
		return "EC2 弹性 / Carrier IP"
	default:
		return kind
	}
}
func Normalize(input []contract.RefreshTarget) ([]contract.RefreshTarget, error) {
	if len(input) == 0 || len(input) > 50 {
		return nil, contract.ErrInvalid
	}
	result := append([]contract.RefreshTarget{}, input...)
	seen := map[string]bool{}
	for i := range result {
		target := &result[i]
		id, err := uuid.Parse(target.ConnectionID)
		if err != nil || !KindOK(target.Kind) {
			return nil, contract.ErrInvalid
		}
		target.ConnectionID = id.String()
		if !Regional(target.Kind) {
			if target.Region != "" && target.Region != "global" && target.Region != "us-east-1" {
				return nil, contract.ErrInvalid
			}
			target.Region = "us-east-1"
		} else if !RegionOK(target.Region) {
			return nil, contract.ErrInvalid
		}
		key := InputKey(*target)
		if seen[key] {
			return nil, contract.ErrInvalid
		}
		seen[key] = true
	}
	sort.Slice(result, func(i, j int) bool { return InputKey(result[i]) < InputKey(result[j]) })
	return result, nil
}
