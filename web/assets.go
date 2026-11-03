package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"time"

	"github.com/herta-network/aws-panel/internal/infra/clock"
)

//go:embed templates/*.html static
var assets embed.FS

type SystemView struct {
	Name          string
	Version       string
	ServerTime    string
	Heartbeat     string
	WorkerHealthy bool
	Error         bool
}

type Renderer struct{ templates *template.Template }

type UserView struct {
	ID                          int64
	Username, DisplayName, Role string
}
type Layout struct {
	Title, Active, CSRF string
	User                *UserView
}
type layoutModel struct {
	Layout
	Content template.HTML
}

func RoleLabel(role string) string {
	switch role {
	case "admin":
		return "管理员"
	case "operator":
		return "操作员"
	case "viewer":
		return "只读成员"
	default:
		return "未知角色"
	}
}

func ActionLabel(action string) string {
	switch action {
	case "accounts.launch_secret.created":
		return "保存实例登录配置"
	case "accounts.launch_secret.deleted":
		return "清理实例登录配置"
	case "identity.member.initialized":
		return "初始化管理员"
	case "identity.member.created":
		return "新增成员"
	case "identity.member.updated":
		return "更新成员"
	case "identity.login":
		return "登录"
	case "identity.logout":
		return "退出"
	case "identity.password.changed":
		return "修改密码"
	case "operations.job.submitted":
		return "提交任务"
	case "operations.job.cancelled":
		return "请求取消任务"
	case "operations.job.retried":
		return "恢复任务"
	case "inventory.schedule.created":
		return "新增定时同步"
	case "inventory.schedule.updated":
		return "更新定时同步"
	case "inventory.schedule.deleted":
		return "删除定时同步"
	default:
		return action
	}
}
func ResourceKind(value string) string {
	switch value {
	case "account_region":
		return "区域启用状态"
	case "lightsail_region":
		return "Lightsail 可用区域"
	case "lightsail_instance":
		return "Lightsail 实例"
	default:
		return value
	}
}
func ResourceState(value string) string {
	switch value {
	case "ENABLED_BY_DEFAULT":
		return "默认启用"
	case "ENABLED":
		return "已启用"
	case "ENABLING":
		return "启用中"
	case "DISABLING":
		return "停用中"
	case "DISABLED":
		return "未启用"
	case "available":
		return "服务可用"
	case "running":
		return "运行中"
	case "stopped":
		return "已停止"
	case "pending":
		return "准备中"
	case "stopping":
		return "停止中"
	case "shutting-down":
		return "关闭中"
	case "terminated":
		return "已终止"
	case "unknown":
		return "未知"
	default:
		return value
	}
}
func ResourceColor(value string) string {
	switch value {
	case "ENABLED_BY_DEFAULT", "ENABLED", "available", "running":
		return "bg-green-lt"
	case "ENABLING", "DISABLING", "pending", "stopping", "shutting-down":
		return "bg-yellow-lt"
	default:
		return "bg-secondary-lt"
	}
}
func SyncStatus(value string) string {
	switch value {
	case "ready":
		return "已同步"
	case "syncing":
		return "同步中"
	case "failed":
		return "同步失败"
	case "cancelled":
		return "已取消"
	default:
		return "尚未同步"
	}
}
func SyncColor(value string) string {
	switch value {
	case "ready":
		return "bg-green-lt"
	case "syncing":
		return "bg-blue-lt"
	case "failed":
		return "bg-red-lt"
	default:
		return "bg-secondary-lt"
	}
}
func JobStatus(value string) string {
	switch value {
	case "queued":
		return "等待调度"
	case "running":
		return "正在处理"
	case "waiting":
		return "等待再次检查"
	case "succeeded":
		return "已完成"
	case "failed":
		return "失败"
	case "needs_attention":
		return "需要处理"
	case "cancelled":
		return "已取消"
	case "partial_success":
		return "部分成功"
	case "lease_lost":
		return "执行中断"
	default:
		return value
	}
}
func JobColor(value string) string {
	switch value {
	case "succeeded":
		return "bg-green-lt"
	case "failed":
		return "bg-red-lt"
	case "needs_attention", "partial_success":
		return "bg-yellow-lt"
	case "running", "waiting":
		return "bg-blue-lt"
	default:
		return "bg-secondary-lt"
	}
}

func NewRenderer() (*Renderer, error) {
	templates, err := template.New("pages").Funcs(template.FuncMap{
		"usd":           func(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) },
		"roleLabel":     RoleLabel,
		"actionLabel":   ActionLabel,
		"resourceKind":  ResourceKind,
		"resourceState": ResourceState,
		"resourceColor": ResourceColor,
		"syncStatus":    SyncStatus,
		"syncColor":     SyncColor,
		"jobStatus":     JobStatus,
		"jobColor":      JobColor,
		"jobMode": func(value string) string {
			if value == "reconcile" {
				return "核对结果"
			}
			return "执行步骤"
		},
		"jobRetryable": func(value string) bool { return value == "failed" || value == "needs_attention" },
		"authType": func(value string) string {
			return map[string]string{"access_key": "Access Key", "temporary": "临时凭据", "assume_role": "AssumeRole"}[value]
		},
		"routeMode": func(value string) string {
			return map[string]string{"direct": "直连", "socks5": "SOCKS5 代理"}[value]
		},
		"verificationStage": func(value string) string {
			return map[string]string{"configuration": "配置检查", "proxy": "代理连接", "network": "网络连接", "tls": "TLS 链路", "aws": "AWS 响应", "identity": "账号身份"}[value]
		},
		"textptr": func(value *string) string {
			if value == nil {
				return ""
			}
			return *value
		},
		"connectionStatus": func(value string) string {
			switch value {
			case "verified":
				return "已验证"
			case "failed":
				return "验证失败"
			case "disabled":
				return "已停用"
			case "expired":
				return "已过期"
			default:
				return "待验证"
			}
		},
		"beijing": func(value time.Time) string { return clock.In(value).Format("2006-01-02 15:04:05") },
	}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Renderer{templates: templates}, nil
}

func (r *Renderer) Render(name string, value any) ([]byte, error) {
	var buffer bytes.Buffer
	if err := r.templates.ExecuteTemplate(&buffer, name, value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (r *Renderer) Page(name string, layout Layout, value any) ([]byte, error) {
	content, err := r.Render(name, value)
	if err != nil {
		return nil, err
	}
	// Only already-escaped output from an embedded template is trusted here.
	return r.Render("layout", layoutModel{Layout: layout, Content: template.HTML(content)})
}

func Static() fs.FS {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
