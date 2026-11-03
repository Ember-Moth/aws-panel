package contract

import "time"

const (
	AccountRegion     = "account_region"
	LightsailRegion   = "lightsail_region"
	LightsailInstance = "lightsail_instance"
	LightsailStaticIP = "lightsail_static_ip"
	EC2Instance       = "ec2_instance"
	EC2Address        = "ec2_address"
)

type RefreshTarget struct {
	ConnectionID string `json:"connection_id"`
	Kind         string `json:"kind"`
	Region       string `json:"region,omitempty"`
}
type RefreshRequest struct {
	Targets        []RefreshTarget `json:"targets"`
	IdempotencyKey string          `json:"idempotency_key"`
}
type RefreshInput struct {
	Targets []RefreshTarget `json:"targets"`
}
type ConnectionChoice struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}
type AccountChoice struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	AWSAccountID string             `json:"aws_account_id"`
	Partition    string             `json:"partition"`
	Connections  []ConnectionChoice `json:"connections"`
}
type Options struct {
	Accounts []AccountChoice `json:"accounts"`
	Regions  []string        `json:"regions"`
}
type Filter struct {
	AccountID      string `json:"account_id,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Region         string `json:"region,omitempty"`
	State          string `json:"state,omitempty"`
	Query          string `json:"query,omitempty"`
	IncludeMissing bool   `json:"include_missing"`
	Page           int    `json:"page"`
}
type Scope struct {
	ID            string     `json:"id"`
	AccountID     string     `json:"account_id"`
	AccountName   string     `json:"account_name"`
	Kind          string     `json:"kind"`
	Region        string     `json:"region"`
	Status        string     `json:"status"`
	Code          string     `json:"code,omitempty"`
	Message       string     `json:"message,omitempty"`
	ResourceCount int        `json:"resource_count"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastJobID     string     `json:"last_job_id,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
	Stale         bool       `json:"stale"`
}
type RegionDetails struct {
	OptStatus         string `json:"opt_status,omitempty"`
	DisplayName       string `json:"display_name,omitempty"`
	AvailabilityZones []Zone `json:"availability_zones,omitempty"`
}
type Zone struct {
	Name  string `json:"name"`
	State string `json:"state"`
}
type Disk struct {
	SizeGB int32 `json:"size_gb"`
	System bool  `json:"system"`
}
type Port struct {
	Protocol  string   `json:"protocol"`
	From      int32    `json:"from"`
	To        int32    `json:"to"`
	CIDRs     []string `json:"cidrs,omitempty"`
	IPv6CIDRs []string `json:"ipv6_cidrs,omitempty"`
}
type InstanceDetails struct {
	BlueprintID      string     `json:"blueprint_id,omitempty"`
	BlueprintName    string     `json:"blueprint_name,omitempty"`
	BundleID         string     `json:"bundle_id,omitempty"`
	AvailabilityZone string     `json:"availability_zone,omitempty"`
	CPU              int32      `json:"cpu"`
	RAMGB            float32    `json:"ram_gb"`
	IPType           string     `json:"ip_type,omitempty"`
	PublicIP         string     `json:"public_ip,omitempty"`
	PrivateIP        string     `json:"private_ip,omitempty"`
	IPv6             []string   `json:"ipv6,omitempty"`
	StaticIP         bool       `json:"static_ip"`
	Username         string     `json:"username,omitempty"`
	SSHKeyName       string     `json:"ssh_key_name,omitempty"`
	CreatedAt        *time.Time `json:"created_at,omitempty"`
	Disks            []Disk     `json:"disks,omitempty"`
	Ports            []Port     `json:"ports,omitempty"`
}
type EC2Details struct {
	CarrierIP               string    `json:"carrier_ip,omitempty"`
	ID                      string    `json:"id"`
	ImageID                 string    `json:"image_id"`
	InstanceType            string    `json:"instance_type"`
	AvailabilityZone        string    `json:"availability_zone"`
	VPCID                   string    `json:"vpc_id"`
	SubnetID                string    `json:"subnet_id"`
	SecurityGroupIDs        []string  `json:"security_group_ids"`
	PrimaryENI              string    `json:"primary_eni"`
	PublicIP                string    `json:"public_ip"`
	PrivateIP               string    `json:"private_ip"`
	IPv6                    []string  `json:"ipv6"`
	RootVolumeID            string    `json:"root_volume_id"`
	DeleteRootOnTermination bool      `json:"delete_root_on_termination"`
	CreatedAt               time.Time `json:"created_at"`
}
type AddressDetails struct {
	Kind               string `json:"kind,omitempty"`
	ID                 string `json:"id"`
	IP                 string `json:"ip"`
	InstanceID         string `json:"instance_id"`
	NetworkInterfaceID string `json:"network_interface_id,omitempty"`
	AssociationID      string `json:"association_id,omitempty"`
	BorderGroup        string `json:"border_group,omitempty"`
	Attached           bool   `json:"attached"`
}
type Details struct {
	EC2     *EC2Details     `json:"ec2,omitempty"`
	Address *AddressDetails `json:"address,omitempty"`

	Region   *RegionDetails   `json:"region,omitempty"`
	Instance *InstanceDetails `json:"instance,omitempty"`
}
type Resource struct {
	ID           string     `json:"id"`
	ScopeID      string     `json:"scope_id"`
	AccountID    string     `json:"account_id"`
	AccountName  string     `json:"account_name"`
	Kind         string     `json:"kind"`
	ResourceID   string     `json:"resource_id"`
	Name         string     `json:"name"`
	Region       string     `json:"region"`
	State        string     `json:"state"`
	Details      Details    `json:"details"`
	ObservedAt   time.Time  `json:"observed_at"`
	Present      bool       `json:"present"`
	MissingSince *time.Time `json:"missing_since,omitempty"`
}
type ResourceList struct {
	Items    []Resource `json:"items"`
	Scopes   []Scope    `json:"scopes"`
	Page     int        `json:"page"`
	Previous int        `json:"previous,omitempty"`
	Next     int        `json:"next,omitempty"`
}
type ScopeList struct {
	Items    []Scope `json:"items"`
	Page     int     `json:"page"`
	Previous int     `json:"previous,omitempty"`
	Next     int     `json:"next,omitempty"`
}
type ResourceDetail struct {
	Resource Resource `json:"resource"`
	Scope    Scope    `json:"scope"`
}
type ScheduleRequest struct {
	Name            string          `json:"name"`
	Targets         []RefreshTarget `json:"targets"`
	IntervalMinutes int             `json:"interval_minutes"`
	Active          bool            `json:"active"`
	Version         int64           `json:"version,omitempty"`
}
type Schedule struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	ActorID         int64           `json:"actor_id"`
	ActorName       string          `json:"actor_name,omitempty"`
	Targets         []RefreshTarget `json:"targets"`
	IntervalMinutes int             `json:"interval_minutes"`
	Active          bool            `json:"active"`
	Version         int64           `json:"version"`
	NextRunAt       time.Time       `json:"next_run_at"`
	LastRunAt       *time.Time      `json:"last_run_at,omitempty"`
	LastJobID       *string         `json:"last_job_id,omitempty"`
	Code            string          `json:"code,omitempty"`
	Message         string          `json:"message,omitempty"`
}
type ScheduleList struct {
	Items    []Schedule `json:"items"`
	Page     int        `json:"page"`
	Previous int        `json:"previous,omitempty"`
	Next     int        `json:"next,omitempty"`
}
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }
func (e *Error) Is(other error) bool {
	target, ok := other.(*Error)
	return ok && e.Code == target.Code
}

var (
	ErrInvalid     = &Error{"INVALID_INPUT", "同步范围或参数不正确"}
	ErrForbidden   = &Error{"FORBIDDEN", "没有访问此资源或同步设置的权限"}
	ErrNotFound    = &Error{"NOT_FOUND", "资源快照或同步设置不存在"}
	ErrConflict    = &Error{"CONFLICT", "同步范围正在处理或配置已变更，请刷新后重试"}
	ErrUnavailable = &Error{"DEPENDENCY_UNAVAILABLE", "资源查询服务暂不可用"}
	ErrStale       = &Error{"SYNC_SUPERSEDED", "同步已经失效，结果不会覆盖当前快照"}
	ErrPartition   = &Error{"PARTITION_UNSUPPORTED", "首批资源查询仅支持 AWS 商业分区"}
	ErrIncomplete  = &Error{"PAGINATION_INCOMPLETE", "分页不完整，本次同步未替换已有快照"}
	ErrResponse    = &Error{"INVALID_AWS_RESPONSE", "AWS 返回的数据无法完整校验，本次同步未替换快照"}
)

// Observation is a permission-scoped aggregate of a previously completed snapshot.
type Observation struct {
	CarrierCount  int64      `json:"carrier_count,omitempty"`
	CarrierKnown  bool       `json:"carrier_known,omitempty"`
	AccountID     string     `json:"account_id"`
	Kind          string     `json:"kind"`
	Region        string     `json:"region"`
	Count         int64      `json:"count"`
	Running       int64      `json:"running"`
	IPv4          []string   `json:"ipv4"`
	Regions       []string   `json:"regions"`
	Status        string     `json:"status"`
	Code          string     `json:"code,omitempty"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
}
