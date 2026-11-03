package contract

import "time"

type Blueprint struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Version               string `json:"version"`
	MinPower              int32  `json:"min_power"`
	LicenseURL            string `json:"license_url,omitempty"`
	RootPasswordSupported bool   `json:"root_password_supported"`
}
type Bundle struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	CPU          int32   `json:"cpu"`
	RAMGB        float32 `json:"ram_gb"`
	DiskGB       int32   `json:"disk_gb"`
	Power        int32   `json:"power"`
	MonthlyCents int64   `json:"monthly_cents"`
	PublicIPv4   bool    `json:"public_ipv4"`
}
type Quota struct {
	Known     bool   `json:"known"`
	LimitVCPU int64  `json:"limit_vcpu,omitempty"`
	UsedVCPU  int64  `json:"used_vcpu"`
	Code      string `json:"code,omitempty"`
}
type Catalogue struct {
	ConnectionID  string      `json:"connection_id"`
	Region        string      `json:"region"`
	Zones         []string    `json:"zones"`
	Blueprints    []Blueprint `json:"blueprints"`
	Bundles       []Bundle    `json:"bundles"`
	KeyPairs      []string    `json:"key_pairs"`
	Quota         Quota       `json:"quota"`
	CheckedAt     time.Time   `json:"checked_at"`
	WritesEnabled bool        `json:"writes_enabled"`
}
type Region struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Available bool   `json:"available"`
}
type Regions struct {
	ConnectionID string    `json:"connection_id"`
	Items        []Region  `json:"items"`
	CheckedAt    time.Time `json:"checked_at"`
}
type Target struct {
	ConnectionID   string `json:"connection_id"`
	Region         string `json:"region"`
	Name           string `json:"name"`
	Zone           string `json:"zone"`
	BlueprintID    string `json:"blueprint_id"`
	BundleID       string `json:"bundle_id"`
	IPType         string `json:"ip_type"`
	KeyPairName    string `json:"key_pair_name,omitempty"`
	LoginMethod    string `json:"login_method,omitempty"`
	LaunchSecretID string `json:"launch_secret_id,omitempty"`
}
type CreateTarget struct {
	Target
	RootPassword string `json:"root_password,omitempty" format:"password"`
}
type CreateRequest struct {
	Targets []CreateTarget `json:"targets"`
}
type PlanInput struct {
	Targets []Target `json:"targets"`
}
type StepSpec struct {
	Target    Target    `json:"target"`
	Blueprint Blueprint `json:"blueprint"`
	Bundle    Bundle    `json:"bundle"`
	Quota     Quota     `json:"quota"`
	CheckedAt time.Time `json:"checked_at"`
}
type PreviewTarget struct {
	AccountID    string   `json:"account_id"`
	AWSAccountID string   `json:"aws_account_id"`
	Spec         StepSpec `json:"spec"`
}
type Preview struct {
	ID           string          `json:"id"`
	Targets      []PreviewTarget `json:"targets"`
	MonthlyCents int64           `json:"monthly_cents"`
	ExpiresAt    time.Time       `json:"expires_at"`
	CanSubmit    bool            `json:"can_submit"`
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

func (e *Error) Error() string { return e.Message }
func (e *Error) Is(other error) bool {
	target, ok := other.(*Error)
	return ok && target.Code == e.Code
}

var (
	ErrInvalid       = &Error{"INVALID_INPUT", "创建参数不正确；名称需为 3 至 63 位小写字母、数字或中划线"}
	ErrForbidden     = &Error{"FORBIDDEN", "没有创建这些账号实例的权限"}
	ErrUnavailable   = &Error{"DEPENDENCY_UNAVAILABLE", "Lightsail 创建服务暂不可用"}
	ErrConflict      = &Error{"NAME_CONFLICT", "实例名称已被使用，请选择其他名称"}
	ErrCatalogue     = &Error{"CATALOGUE_CHANGED", "镜像、套餐或价格已变化，请重新预览"}
	ErrQuota         = &Error{"QUOTA_INSUFFICIENT", "当前可查询的 vCPU 配额不足，请调整目标或处理配额"}
	ErrResponse      = &Error{"INVALID_AWS_RESPONSE", "AWS 返回的数据无法完整校验"}
	ErrOwnership     = &Error{"OWNERSHIP_UNCONFIRMED", "同名实例的归属或参数无法确认，请人工核对"}
	ErrLease         = &Error{"LEASE_LOST", "执行租约已失效"}
	ErrLogin         = &Error{"INVALID_LOGIN_CONFIG", "请选择有效的登录方式；root 密码需为 15 至 128 个字符且两次输入一致"}
	ErrPasswordImage = &Error{"ROOT_PASSWORD_UNSUPPORTED", "此镜像尚不支持自动配置 root 密码，请选择 Debian、Ubuntu 或 Amazon Linux 系统镜像"}
)
