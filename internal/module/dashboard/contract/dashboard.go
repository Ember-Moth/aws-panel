package contract

import "time"

const (
	EC2Instances       = "ec2_instances"
	LightsailInstances = "lightsail_instances"
	EC2IPs             = "ec2_ips"
	LightsailIPs       = "lightsail_ips"
	Catalog            = "catalog"
	EC2Quotas          = "ec2_quotas"
	LightsailQuotas    = "lightsail_quotas"
	BedrockQuotas      = "bedrock_quotas"
)

type Quota struct {
	Key        string  `json:"key"`
	Name       string  `json:"name"`
	Code       string  `json:"code"`
	Unit       string  `json:"unit"`
	Period     string  `json:"period,omitempty"`
	Global     bool    `json:"global"`
	Limit      float64 `json:"limit"`
	Used       float64 `json:"used"`
	LimitKnown bool    `json:"limit_known"`
	UsageKnown bool    `json:"usage_known"`
}
type Data struct {
	CarrierCount     int64    `json:"carrier_count,omitempty"`
	CarrierKnown     bool     `json:"carrier_known,omitempty"`
	Count            int64    `json:"count"`
	Running          int64    `json:"running"`
	IPv4             []string `json:"ipv4,omitempty"`
	Regions          []string `json:"regions,omitempty"`
	LightsailRegions []string `json:"lightsail_regions,omitempty"`
	LightsailKnown   bool     `json:"lightsail_known"`
	Quotas           []Quota  `json:"quotas,omitempty"`
}
type Metric struct {
	Key             string     `json:"key"`
	Name            string     `json:"name"`
	Count           int64      `json:"count"`
	Running         int64      `json:"running"`
	Known           bool       `json:"known"`
	Partial         bool       `json:"partial"`
	Stale           bool       `json:"stale"`
	KnownScopes     int        `json:"known_scopes"`
	ExpectedScopes  int        `json:"expected_scopes"`
	UnknownAccounts int        `json:"unknown_accounts"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
}
type PoolQuota struct {
	Key             string     `json:"key"`
	Name            string     `json:"name"`
	Service         string     `json:"service"`
	Code            string     `json:"code"`
	Unit            string     `json:"unit"`
	Period          string     `json:"period,omitempty"`
	Global          bool       `json:"global"`
	Limit           float64    `json:"limit"`
	Used            float64    `json:"used"`
	Available       float64    `json:"available"`
	Known           bool       `json:"known"`
	UsageKnown      bool       `json:"usage_known"`
	Complete        bool       `json:"complete"`
	Stale           bool       `json:"stale"`
	KnownScopes     int        `json:"known_scopes"`
	UsageScopes     int        `json:"usage_scopes"`
	ExpectedScopes  int        `json:"expected_scopes"`
	UnknownAccounts int        `json:"unknown_accounts"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
	Percent         float64    `json:"percent"`
}
type Account struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	AWSAccountID   string     `json:"aws_account_id"`
	Active         bool       `json:"active"`
	Status         string     `json:"status"`
	ConnectionID   string     `json:"connection_id,omitempty"`
	ConnectionName string     `json:"connection_name,omitempty"`
	RouteMode      string     `json:"route_mode"`
	ProxyName      string     `json:"proxy_name,omitempty"`
	EnabledRegions int        `json:"enabled_regions"`
	RegionsKnown   bool       `json:"regions_known"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
	FailedScopes   int        `json:"failed_scopes"`
}
type Query struct {
	AccountID string `json:"account_id,omitempty"`
	Region    string `json:"region,omitempty"`
}
type Overview struct {
	AccountOptions  []Account   `json:"account_options"`
	BedrockCoverage Metric      `json:"bedrock_coverage"`
	Accounts        []Account   `json:"accounts"`
	AccountCount    int         `json:"account_count"`
	ActiveAccounts  int         `json:"active_accounts"`
	ReadyAccounts   int         `json:"ready_accounts"`
	Metrics         []Metric    `json:"metrics"`
	Quotas          []PoolQuota `json:"quotas"`
	DistinctRegions int         `json:"distinct_regions"`
	Regions         []string    `json:"regions"`
	CanRefresh      bool        `json:"can_refresh"`
	Query           Query       `json:"query"`
	CheckedAt       time.Time   `json:"checked_at"`
	Warnings        []string    `json:"warnings"`
}
type RefreshRequest struct {
	AccountID      string `json:"account_id,omitempty"`
	Region         string `json:"region,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}
type RefreshJob struct {
	ID       string `json:"id"`
	Accounts int    `json:"accounts"`
}
type Skipped struct {
	AccountID string `json:"account_id"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
}
type Refresh struct {
	Jobs    []RefreshJob `json:"jobs"`
	Skipped []Skipped    `json:"skipped"`
}
type Target struct {
	ConnectionID string `json:"connection_id"`
	Region       string `json:"region,omitempty"`
}
type Input struct {
	Targets []Target `json:"targets"`
}
type Error struct{ Code, Message string }

func (e *Error) Error() string       { return e.Message }
func (e *Error) Is(other error) bool { v, ok := other.(*Error); return ok && v.Code == e.Code }

var (
	ErrInvalid      = &Error{"INVALID_INPUT", "请选择有效的仪表盘范围"}
	ErrForbidden    = &Error{"FORBIDDEN", "没有读取该账号池的权限"}
	ErrUnavailable  = &Error{"DEPENDENCY_UNAVAILABLE", "仪表盘暂不可用"}
	ErrResponse     = &Error{"INVALID_AWS_RESPONSE", "AWS 返回的数据不完整"}
	ErrNoConnection = &Error{"NO_VERIFIED_CONNECTION", "没有可用的已验证连接，请先检查 AWS 账号"}
)
