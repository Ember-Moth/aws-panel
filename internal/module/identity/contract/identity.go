package contract

import "time"

const (
	RoleAdmin               = "admin"
	RoleOperator            = "operator"
	RoleViewer              = "viewer"
	ActionSystemRead        = "system.read"
	ActionMembersManage     = "members.manage"
	ActionAuditRead         = "audit.read"
	ActionConnectionsManage = "connections.manage"
	ActionAccountsRead      = "accounts.read"
	ActionJobsRead          = "jobs.read"
	ActionJobsDiagnose      = "jobs.diagnose"
	ActionCloudOperate      = "cloud.operate"
)

type Member struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	Active      bool      `json:"active"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type CreateMemberRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Role        string `json:"role"`
}
type UpdateMemberRequest struct {
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Active      *bool  `json:"active"`
	Version     int64  `json:"version"`
}
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}
type Session struct {
	Member    Member    `json:"member"`
	ExpiresAt time.Time `json:"expires_at"`
}
type LoginGrant struct {
	Token   string  `json:"-"`
	Session Session `json:"session"`
}
type MemberList struct {
	Items    []Member `json:"items"`
	Total    int64    `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
}

type AccountGrant struct {
	AccountID string    `json:"account_id"`
	Access    string    `json:"access"`
	UpdatedAt time.Time `json:"updated_at"`
}
type GrantRequest struct {
	AccountID string `json:"account_id"`
	Access    string `json:"access"`
}
type GrantMember struct {
	Member Member
	Access string
}
type GrantPage struct {
	AccountID, Label     string
	Items                []GrantMember
	Page, Previous, Next int
}

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && e.Code == other.Code
}

var (
	ErrUnauthenticated    = &Error{"UNAUTHENTICATED", "请先登录"}
	ErrInvalidCredentials = &Error{"INVALID_CREDENTIALS", "账号或密码错误"}
	ErrForbidden          = &Error{"FORBIDDEN", "没有执行此操作的权限"}
	ErrValidation         = &Error{"INVALID_INPUT", "输入不正确"}
	ErrConflict           = &Error{"CONFLICT", "记录已变更或用户名已存在，请刷新后重试"}
	ErrLastAdmin          = &Error{"LAST_ADMIN", "至少需要保留一个可用管理员"}
	ErrInitialized        = &Error{"ALREADY_INITIALIZED", "管理员已初始化"}
	ErrRateLimited        = &Error{"RATE_LIMITED", "尝试过于频繁，请稍后再试"}
	ErrUnavailable        = &Error{"DEPENDENCY_UNAVAILABLE", "服务暂不可用"}
	ErrNotFound           = &Error{"NOT_FOUND", "成员不存在"}
)

func Invalid(message string) error { return &Error{ErrValidation.Code, message} }
