package contract

import (
	"encoding/json"
	"time"
)

type PlanRequest struct {
	Operation    string
	InputVersion int
	Input        json.RawMessage
	Targets      []Target
}
type Plan struct {
	ID              string     `json:"id"`
	Operation       string     `json:"operation"`
	Title           string     `json:"title"`
	Diagnostic      bool       `json:"diagnostic"`
	WritesCloud     bool       `json:"writes_cloud"`
	CanSubmit       bool       `json:"can_submit"`
	InputVersion    int        `json:"input_version"`
	ExecutorVersion int        `json:"executor_version"`
	Items           []ItemPlan `json:"items"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
}
type SubmitRequest struct {
	PlanID         string `json:"plan_id"`
	IdempotencyKey string `json:"idempotency_key"`
}
type RetryRequest struct {
	Version            int64    `json:"version"`
	StepIDs            []string `json:"step_ids"`
	RefreshConnections bool     `json:"refresh_connections"`
}
type Job struct {
	ID              string     `json:"id"`
	Operation       string     `json:"operation"`
	Title           string     `json:"title"`
	ActorName       string     `json:"actor_name"`
	Status          string     `json:"status"`
	Diagnostic      bool       `json:"diagnostic"`
	CancelRequested bool       `json:"cancel_requested"`
	ExecutorVersion int        `json:"executor_version"`
	InputVersion    int        `json:"input_version"`
	Version         int64      `json:"version"`
	EventSeq        int64      `json:"event_seq"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}
type Item struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Status     string  `json:"status"`
	CanRecover bool    `json:"can_recover"`
	Binding    Binding `json:"binding"`
	Steps      []Step  `json:"steps"`
}
type Step struct {
	ID           string     `json:"id"`
	Label        string     `json:"label"`
	Status       string     `json:"status"`
	Mode         string     `json:"mode"`
	Code         string     `json:"code,omitempty"`
	AttemptCount int        `json:"attempt_count"`
	AttemptLimit int        `json:"attempt_limit"`
	DeadlineAt   time.Time  `json:"deadline_at"`
	NextRunAt    *time.Time `json:"next_run_at,omitempty"`
	Output       Output     `json:"output"`
	Attempts     []Attempt  `json:"attempts"`
}
type Attempt struct {
	Number     int        `json:"number"`
	Mode       string     `json:"mode"`
	Outcome    string     `json:"outcome"`
	Code       string     `json:"code,omitempty"`
	Binding    Binding    `json:"binding"`
	Output     Output     `json:"output"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}
type Event struct {
	Seq        int64     `json:"seq"`
	Type       string    `json:"type"`
	StepID     *string   `json:"step_id,omitempty"`
	ItemID     *string   `json:"item_id,omitempty"`
	Code       string    `json:"code,omitempty"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurred_at"`
}
type Detail struct {
	Job       Job     `json:"job"`
	Items     []Item  `json:"items"`
	Events    []Event `json:"events"`
	CanManage bool    `json:"can_manage"`
}
type JobList struct {
	Items    []Job `json:"items"`
	Page     int   `json:"page"`
	Previous int   `json:"previous,omitempty"`
	Next     int   `json:"next,omitempty"`
}
type EventPage struct {
	Events   []Event `json:"events"`
	LastSeq  int64   `json:"last_seq"`
	Terminal bool    `json:"terminal"`
}

// TargetRef/TargetState are for trusted module observers; public callers still
// go through account authorization in their owning service.
type TargetRef struct {
	AccountID string `json:"account_id"`
	TargetKey string `json:"target_key"`
}
type TargetState struct {
	AccountID, TargetKey, JobID, StepID, Status, Code string
	CreatedAt, UpdatedAt                              time.Time
	NextRunAt                                         *time.Time
}
type DiagnosticRequest struct {
	Count    int    `json:"count"`
	Behavior string `json:"behavior"`
}
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }
func (e *Error) Is(other error) bool {
	target, ok := other.(*Error)
	return ok && target.Code == e.Code
}

var (
	ErrInvalid        = &Error{"INVALID_INPUT", "任务参数不正确"}
	ErrForbidden      = &Error{"FORBIDDEN", "没有查看或操作此任务的权限"}
	ErrNotFound       = &Error{"NOT_FOUND", "任务或计划不存在"}
	ErrConflict       = &Error{"CONFLICT", "任务已变更或幂等键对应不同参数，请刷新后重试"}
	ErrExpired        = &Error{"PLAN_EXPIRED", "计划已过期，请重新预览"}
	ErrUnavailable    = &Error{"DEPENDENCY_UNAVAILABLE", "任务服务暂不可用"}
	ErrExecutor       = &Error{"EXECUTOR_UNAVAILABLE", "任务所需的执行器版本不可用"}
	ErrLease          = &Error{"LEASE_LOST", "执行租约已失效"}
	ErrCancelled      = &Error{"CANCEL_REQUESTED", "已请求停止新的执行步骤"}
	ErrBinding        = &Error{"CONNECTION_CHANGED", "连接配置已变化，请检查后恢复任务"}
	ErrWritesDisabled = &Error{"CLOUD_WRITES_DISABLED", "云端写入尚未启用，当前仅可预览和读取"}
	ErrTargetBusy     = &Error{"TARGET_BUSY", "相关实例或地址正在被其他任务处理，请等待后重试"}
	ErrEffect         = &Error{"EFFECT_MISMATCH", "操作记录与目标不一致，已停止新的写入"}
)
