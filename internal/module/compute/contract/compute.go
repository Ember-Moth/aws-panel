package contract

import "time"

type Image struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Family       string `json:"family"`
	Architecture string `json:"architecture"`
	RootDevice   string `json:"root_device"`
	MinDiskGB    int32  `json:"min_disk_gb"`
	Owner        string `json:"owner"`
	CreatedAt    string `json:"created_at"`
}
type InstanceType struct {
	ID            string   `json:"id"`
	VCPU          int32    `json:"vcpu"`
	MemoryMiB     int64    `json:"memory_mib"`
	Architectures []string `json:"architectures"`
	IPv6          bool     `json:"ipv6"`
}
type Subnet struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	VPCID           string `json:"vpc_id"`
	Zone            string `json:"zone"`
	IPv4CIDR        string `json:"ipv4_cidr"`
	IPv6CIDR        string `json:"ipv6_cidr"`
	AvailableIPs    int32  `json:"available_ips"`
	PublicIPv4Route bool   `json:"public_ipv4_route"`
	PublicIPv6Route bool   `json:"public_ipv6_route"`
}
type SecurityGroup struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	VPCID   string   `json:"vpc_id"`
	SSHIPv4 []string `json:"ssh_ipv4"`
	SSHIPv6 []string `json:"ssh_ipv6"`
}
type Instance struct {
	ID                      string            `json:"id"`
	ARN                     string            `json:"arn"`
	Name                    string            `json:"name"`
	State                   string            `json:"state"`
	ImageID                 string            `json:"image_id"`
	Type                    string            `json:"type"`
	Zone                    string            `json:"zone"`
	VPCID                   string            `json:"vpc_id"`
	SubnetID                string            `json:"subnet_id"`
	SecurityGroupIDs        []string          `json:"security_group_ids"`
	PrimaryENI              string            `json:"primary_eni"`
	PublicIP                string            `json:"public_ip"`
	PrivateIP               string            `json:"private_ip"`
	IPv6                    []string          `json:"ipv6"`
	KeyName                 string            `json:"key_name"`
	RootDevice              string            `json:"root_device"`
	RootVolumeID            string            `json:"root_volume_id"`
	DeleteRootOnTermination bool              `json:"delete_root_on_termination"`
	LaunchTime              time.Time         `json:"launch_time"`
	ClientToken             string            `json:"-"`
	Tags                    map[string]string `json:"-"`
}
type Catalogue struct {
	ConnectionID   string          `json:"connection_id"`
	Region         string          `json:"region"`
	Images         []Image         `json:"images"`
	Types          []InstanceType  `json:"types"`
	Subnets        []Subnet        `json:"subnets"`
	SecurityGroups []SecurityGroup `json:"security_groups"`
	KeyPairs       []string        `json:"key_pairs"`
	Zones          []string        `json:"zones"`
	CheckedAt      time.Time       `json:"checked_at"`
	WritesEnabled  bool            `json:"writes_enabled"`
}
type Listing struct {
	ConnectionID string     `json:"connection_id"`
	Region       string     `json:"region"`
	Items        []Instance `json:"items"`
	CheckedAt    time.Time  `json:"checked_at"`
}
type Quota struct {
	Key        string  `json:"key"`
	Limit      float64 `json:"limit"`
	Used       float64 `json:"used"`
	LimitKnown bool    `json:"limit_known"`
	UsageKnown bool    `json:"usage_known"`
	Note       string  `json:"note,omitempty"`
}
type Network struct {
	VPCID           string `json:"vpc_id"`
	SubnetID        string `json:"subnet_id"`
	GatewayID       string `json:"gateway_id"`
	RouteTableID    string `json:"route_table_id"`
	SecurityGroupID string `json:"security_group_id"`
	Zone            string `json:"zone"`
	IPv4CIDR        string `json:"ipv4_cidr"`
	SubnetIPv4CIDR  string `json:"subnet_ipv4_cidr"`
	IPv6CIDR        string `json:"ipv6_cidr"`
	SubnetIPv6CIDR  string `json:"subnet_ipv6_cidr"`
	SSHIPv4CIDR     string `json:"ssh_ipv4_cidr"`
	SSHIPv6CIDR     string `json:"ssh_ipv6_cidr"`
}
type Target struct {
	Zone        string `json:"zone,omitempty"`
	SSHIPv4CIDR string `json:"ssh_ipv4_cidr,omitempty"`
	SSHIPv6CIDR string `json:"ssh_ipv6_cidr,omitempty"`

	ConnectionID     string   `json:"connection_id"`
	Region           string   `json:"region"`
	Action           string   `json:"action"`
	Name             string   `json:"name,omitempty"`
	InstanceID       string   `json:"instance_id,omitempty"`
	ImageID          string   `json:"image_id,omitempty"`
	InstanceType     string   `json:"instance_type,omitempty"`
	SubnetID         string   `json:"subnet_id,omitempty"`
	SecurityGroupIDs []string `json:"security_group_ids,omitempty"`
	DiskGB           int32    `json:"disk_gb,omitempty"`
	PublicIPv4       bool     `json:"public_ipv4"`
	IPv6             bool     `json:"ipv6"`
	LoginMethod      string   `json:"login_method,omitempty"`
	KeyPairName      string   `json:"key_pair_name,omitempty"`
	LaunchSecretID   string   `json:"launch_secret_id,omitempty"`
}
type InputTarget struct {
	Target
	RootPassword string `json:"root_password,omitempty" format:"password"`
}
type Request struct {
	Targets []InputTarget `json:"targets"`
}
type PlanInput struct {
	Targets []Target `json:"targets"`
}
type Spec struct {
	Network        *Network        `json:"network,omitempty"`
	NetworkQuotas  []Quota         `json:"network_quotas,omitempty"`
	Target         Target          `json:"target"`
	Image          *Image          `json:"image,omitempty"`
	Type           *InstanceType   `json:"type,omitempty"`
	Subnet         *Subnet         `json:"subnet,omitempty"`
	SecurityGroups []SecurityGroup `json:"security_groups,omitempty"`
	Instance       *Instance       `json:"instance,omitempty"`
	Quota          Quota           `json:"quota"`
	Warnings       []string        `json:"warnings"`
	CheckedAt      time.Time       `json:"checked_at"`
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
	ErrInvalid     = &Error{"INVALID_INPUT", "EC2 参数不正确"}
	ErrForbidden   = &Error{"FORBIDDEN", "没有管理这些账号实例的权限"}
	ErrUnavailable = &Error{"DEPENDENCY_UNAVAILABLE", "EC2 服务暂不可用"}
	ErrResponse    = &Error{"INVALID_AWS_RESPONSE", "AWS 实例数据无法完整核对"}
	ErrNotFound    = &Error{"NOT_FOUND", "实例不存在"}
	ErrConflict    = &Error{"RESOURCE_CHANGED", "实例或网络状态已变化，请重新预览"}
	ErrCatalogue   = &Error{"CATALOGUE_CHANGED", "镜像、规格或网络已变化，请重新选择"}
	ErrQuota       = &Error{"QUOTA_INSUFFICIENT", "可查询的 EC2 配额不足"}
	ErrLogin       = &Error{"INVALID_LOGIN_CONFIG", "请选择 SSH 密钥或 15 至 128 字符的 root 密码"}
	ErrNetwork     = &Error{"NETWORK_NOT_READY", "子网、规格或安全组不满足所选 IPv6/公网连接要求"}
)
