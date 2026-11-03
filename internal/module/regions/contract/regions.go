package contract

import "time"

type Target struct {
	ConnectionID string `json:"connection_id"`
	Region       string `json:"region"`
}
type EnableRequest struct {
	Targets []Target `json:"targets"`
}
type StepSpec struct {
	Region        string    `json:"region"`
	ObservedState string    `json:"observed_state"`
	CheckedAt     time.Time `json:"checked_at"`
}
type PreviewTarget struct {
	AccountID    string    `json:"account_id"`
	AWSAccountID string    `json:"aws_account_id"`
	Region       string    `json:"region"`
	State        string    `json:"state"`
	CheckedAt    time.Time `json:"checked_at"`
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
type Region struct {
	Code       string `json:"code"`
	State      string `json:"state"`
	Selectable bool   `json:"selectable"`
}
type Catalogue struct {
	ConnectionID string    `json:"connection_id"`
	Regions      []Region  `json:"regions"`
	CheckedAt    time.Time `json:"checked_at"`
}
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }
func (e *Error) Is(other error) bool {
	target, ok := other.(*Error)
	return ok && target.Code == e.Code
}

var (
	ErrInvalid     = &Error{"INVALID_INPUT", "请选择连接和有效的 AWS 商业区域"}
	ErrForbidden   = &Error{"FORBIDDEN", "没有启用这些账号区域的权限"}
	ErrUnavailable = &Error{"DEPENDENCY_UNAVAILABLE", "区域操作服务暂不可用"}
	ErrState       = &Error{"REGION_STATE_CONFLICT", "区域正在停用或状态不支持此操作，请核对后重新预览"}
	ErrResponse    = &Error{"INVALID_AWS_RESPONSE", "AWS 返回的区域状态无法校验"}
)
