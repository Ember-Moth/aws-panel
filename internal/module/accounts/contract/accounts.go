package contract

import "time"

type Account struct {
	ID           string    `json:"id"`
	Partition    string    `json:"partition"`
	AWSAccountID string    `json:"aws_account_id"`
	Name         string    `json:"name"`
	Active       bool      `json:"active"`
	Version      int64     `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
type AccountRequest struct {
	Partition    string `json:"partition"`
	AWSAccountID string `json:"aws_account_id"`
	Name         string `json:"name"`
	Active       *bool  `json:"active,omitempty"`
	Version      int64  `json:"version,omitempty"`
}
type ProxyAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type ProxyRequest struct {
	Name      string     `json:"name"`
	Host      string     `json:"host"`
	Port      int        `json:"port"`
	Auth      *ProxyAuth `json:"auth,omitempty"`
	ClearAuth bool       `json:"clear_auth,omitempty"`
	Active    *bool      `json:"active,omitempty"`
	Version   int64      `json:"version,omitempty"`
}
type Proxy struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Host        string       `json:"host"`
	Port        int          `json:"port"`
	HasAuth     bool         `json:"has_auth"`
	Active      bool         `json:"active"`
	Version     int64        `json:"version"`
	Connections int64        `json:"connections"`
	Affected    []Connection `json:"affected_connections,omitempty"`
}
type Credentials struct {
	AccessKeyID     string     `json:"access_key_id,omitempty"`
	SecretAccessKey string     `json:"secret_access_key,omitempty"`
	SessionToken    string     `json:"session_token,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	ExternalID      string     `json:"external_id,omitempty"`
}
type ConnectionRequest struct {
	Name               string       `json:"name"`
	AuthType           string       `json:"auth_type"`
	Region             string       `json:"region"`
	RouteMode          string       `json:"route_mode"`
	ProxyID            *string      `json:"proxy_id,omitempty"`
	SourceConnectionID *string      `json:"source_connection_id,omitempty"`
	RoleARN            string       `json:"role_arn,omitempty"`
	Credentials        *Credentials `json:"credentials,omitempty"`
	Active             *bool        `json:"active,omitempty"`
	Version            int64        `json:"version,omitempty"`
}
type Connection struct {
	ID                  string     `json:"id"`
	AccountID           string     `json:"account_id"`
	Name                string     `json:"name"`
	AuthType            string     `json:"auth_type"`
	Region              string     `json:"region"`
	RouteMode           string     `json:"route_mode"`
	ProxyID             *string    `json:"proxy_id,omitempty"`
	SourceConnectionID  *string    `json:"source_connection_id,omitempty"`
	RoleARN             string     `json:"role_arn,omitempty"`
	AccessKeyMasked     string     `json:"access_key_masked,omitempty"`
	CredentialExpiresAt *time.Time `json:"credential_expires_at,omitempty"`
	Active              bool       `json:"active"`
	Version             int64      `json:"version"`
	Status              string     `json:"status"`
	Stage               string     `json:"stage,omitempty"`
	Code                string     `json:"code,omitempty"`
	PrincipalARN        string     `json:"principal_arn,omitempty"`
	VerifiedAt          *time.Time `json:"verified_at,omitempty"`
}
type Verification struct {
	OK           bool      `json:"ok"`
	Stage        string    `json:"stage"`
	Code         string    `json:"code"`
	Message      string    `json:"message"`
	AccountID    string    `json:"aws_account_id,omitempty"`
	PrincipalARN string    `json:"principal_arn,omitempty"`
	RequestID    string    `json:"aws_request_id,omitempty"`
	CheckedAt    time.Time `json:"checked_at"`
}
type AccountDetail struct {
	Account     Account      `json:"account"`
	Connections []Connection `json:"connections"`
	CanManage   bool         `json:"can_manage"`
	VaultReady  bool         `json:"vault_ready"`
}
type ConnectionVersion struct {
	ID           string
	Version      int64
	ProxyID      string
	ProxyVersion int64
}
type Snapshot struct {
	AccountID, ConnectionID, AWSAccountID, Partition, Region, Fingerprint string
	Chain                                                                 []ConnectionVersion
}

// LaunchBinding scopes a non-retrievable UI credential to one actor/account/target.
type LaunchBinding struct {
	AccountID  string `json:"account_id"`
	TargetHash string `json:"target_hash"`
}
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && e.Code == other.Code
}

var (
	ErrForbidden   = &Error{"FORBIDDEN", "没有访问此账号或管理连接的权限"}
	ErrNotFound    = &Error{"NOT_FOUND", "账号、连接或代理不存在"}
	ErrConflict    = &Error{"CONFLICT", "记录已变更或 AWS 账号已存在，请刷新后重试"}
	ErrInvalid     = &Error{"INVALID_INPUT", "输入不正确"}
	ErrDisabled    = &Error{"CONNECTION_DISABLED", "连接、来源账号或代理已停用"}
	ErrExpired     = &Error{"CREDENTIALS_EXPIRED", "临时凭据已过期"}
	ErrVault       = &Error{"VAULT_UNAVAILABLE", "凭据存储不可用，请检查主密钥配置"}
	ErrStale       = &Error{"CONNECTION_CHANGED", "连接配置已变更，请重新发起操作"}
	ErrUnverified  = &Error{"CONNECTION_UNVERIFIED", "连接或角色来源尚未验证，请先验证账号身份"}
	ErrUnavailable = &Error{"DEPENDENCY_UNAVAILABLE", "服务暂不可用"}
)

func Invalid(message string) error { return &Error{ErrInvalid.Code, message} }
