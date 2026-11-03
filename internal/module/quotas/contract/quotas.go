package contract

import (
	"github.com/herta-network/aws-panel/internal/catalogue"
	"time"
)

type Entry struct {
	Global     bool    `json:"global"`
	Period     string  `json:"period,omitempty"`
	Key        string  `json:"key"`
	Name       string  `json:"name"`
	QuotaCode  string  `json:"quota_code,omitempty"`
	Unit       string  `json:"unit"`
	Limit      float64 `json:"limit"`
	LimitKnown bool    `json:"limit_known"`
	Used       float64 `json:"used"`
	UsageKnown bool    `json:"usage_known"`
	Available  float64 `json:"available"`
	Adjustable bool    `json:"adjustable"`
	Note       string  `json:"note,omitempty"`
}
type Report struct {
	Freshness    map[string]catalogue.Freshness `json:"freshness,omitempty"`
	LimitsRead   bool                           `json:"limits_read"`
	ConnectionID string                         `json:"connection_id"`
	AccountID    string                         `json:"account_id"`
	AWSAccountID string                         `json:"aws_account_id"`
	Region       string                         `json:"region"`
	Service      string                         `json:"service"`
	Entries      []Entry                        `json:"entries"`
	CheckedAt    time.Time                      `json:"checked_at"`
	Warnings     []string                       `json:"warnings"`
}
type Query struct {
	ConnectionID, Region, Service string
	Refresh                       bool
}
type Error struct{ Code, Message string }

func (e *Error) Error() string       { return e.Message }
func (e *Error) Is(other error) bool { v, ok := other.(*Error); return ok && v.Code == e.Code }

var (
	ErrInvalid      = &Error{"INVALID_INPUT", "请选择有效的连接、区域和服务"}
	ErrForbidden    = &Error{"FORBIDDEN", "没有读取此账号配额的权限"}
	ErrUnavailable  = &Error{"DEPENDENCY_UNAVAILABLE", "配额查询暂不可用"}
	ErrInsufficient = &Error{"QUOTA_INSUFFICIENT", "可确认的配额不足，请调整数量或处理配额"}
	ErrResponse     = &Error{"INVALID_AWS_RESPONSE", "AWS 配额或用量数据不完整"}
)

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
	Accounts []Account `json:"accounts"`
}
type Region struct {
	Code      string `json:"code"`
	State     string `json:"state"`
	Available bool   `json:"available"`
}
type Regions struct {
	Freshness map[string]catalogue.Freshness `json:"freshness,omitempty"`
	Items     []Region                       `json:"items"`
	CheckedAt time.Time                      `json:"checked_at"`
}
