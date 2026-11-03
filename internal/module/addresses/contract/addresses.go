package contract

import (
	"github.com/Ember-Moth/aws-panel/internal/catalogue"
	"github.com/Ember-Moth/aws-panel/internal/instanceip"
	"time"
)

type Address = instanceip.Address
type Instance struct {
	Zone               string `json:"zone,omitempty"`
	WavelengthZoneID   string `json:"wavelength_zone_id,omitempty"`
	ID                 string `json:"id"`
	Name               string `json:"name"`
	ARN                string `json:"arn"`
	State              string `json:"state"`
	IPType             string `json:"ip_type,omitempty"`
	PublicIP           string `json:"public_ip,omitempty"`
	PrivateIP          string `json:"private_ip,omitempty"`
	NetworkInterfaceID string `json:"network_interface_id,omitempty"`
	AddressID          string `json:"address_id,omitempty"`
	BorderGroup        string `json:"border_group,omitempty"`
}
type WavelengthZone struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	BorderGroup string `json:"border_group"`
}
type Catalogue struct {
	Freshness       map[string]catalogue.Freshness `json:"freshness,omitempty"`
	WavelengthZones []WavelengthZone               `json:"wavelength_zones,omitempty"`
	ConnectionID    string                         `json:"connection_id"`
	AWSAccountID    string                         `json:"aws_account_id"`
	Region          string                         `json:"region"`
	Service         string                         `json:"service"`
	Addresses       []Address                      `json:"addresses"`
	Instances       []Instance                     `json:"instances"`
	CheckedAt       time.Time                      `json:"checked_at"`
	WritesEnabled   bool                           `json:"writes_enabled"`
}
type Target struct {
	WavelengthZoneID string `json:"wavelength_zone_id,omitempty"`
	ConnectionID     string `json:"connection_id"`
	Region           string `json:"region"`
	Service          string `json:"service"`
	Action           string `json:"action"`
	InstanceID       string `json:"instance_id,omitempty"`
	AddressID        string `json:"address_id,omitempty"`
	NamePrefix       string `json:"name_prefix,omitempty"`
	ReleasePrevious  bool   `json:"release_previous"`
}
type Request struct {
	Targets []Target `json:"targets"`
}
type Quota = instanceip.Quota
type Automatic = instanceip.Automatic
type Spec struct {
	Managed     bool      `json:"managed,omitempty"`
	BorderGroup string    `json:"border_group,omitempty"`
	Target      Target    `json:"target"`
	NewName     string    `json:"new_name,omitempty"`
	Instance    *Instance `json:"instance,omitempty"`
	Address     *Address  `json:"address,omitempty"`
	Previous    *Address  `json:"previous,omitempty"`
	Quota       Quota     `json:"quota"`
	CheckedAt   time.Time `json:"checked_at"`
}
type PreviewTarget struct {
	AWSAccountID string `json:"aws_account_id"`
	Spec         Spec   `json:"spec"`
}
type Preview struct {
	ID        string          `json:"id"`
	Targets   []PreviewTarget `json:"targets"`
	ExpiresAt time.Time       `json:"expires_at"`
	CanSubmit bool            `json:"can_submit"`
}
type Connection struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}
type Account struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	AWSAccountID string       `json:"aws_account_id"`
	Connections  []Connection `json:"connections"`
}
type Options struct {
	Accounts      []Account `json:"accounts"`
	WritesEnabled bool      `json:"writes_enabled"`
}
type Error struct{ Code, Message string }

func (e *Error) Error() string       { return e.Message }
func (e *Error) Is(other error) bool { v, ok := other.(*Error); return ok && v.Code == e.Code }

var (
	ErrInvalid     = &Error{"INVALID_INPUT", "地址操作参数不正确"}
	ErrForbidden   = &Error{"FORBIDDEN", "没有管理这些账号地址的权限"}
	ErrUnavailable = &Error{"DEPENDENCY_UNAVAILABLE", "地址服务暂不可用"}
	ErrNotFound    = &Error{"NOT_FOUND", "地址或实例不存在"}
	ErrConflict    = &Error{"RESOURCE_CHANGED", "地址或实例状态已变化，请重新预览"}
	ErrAttached    = &Error{"ADDRESS_ATTACHED", "该地址仍被资源使用，请先检查并解绑"}
	ErrUnsupported = &Error{"UNSUPPORTED_TARGET", "此实例或地址不支持该操作"}
	ErrQuota       = &Error{"QUOTA_INSUFFICIENT", "静态 IP 配额不足，不能先分配新的替换地址"}
	ErrResponse    = &Error{"INVALID_AWS_RESPONSE", "AWS 地址数据无法完整核对"}
)

type ManagedIP = instanceip.ManagedIP
