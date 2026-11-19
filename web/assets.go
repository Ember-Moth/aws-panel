package web

import (
	"bytes"
	"embed"
	"fmt"
	dc "github.com/Ember-Moth/aws-panel/internal/module/dashboard/contract"
	idc "github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	"html/template"
	"io/fs"
	"strconv"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
)

//go:embed templates/*.html static
var assets embed.FS

type SystemView struct {
	CanOperate    bool
	Name          string
	Version       string
	ServerTime    string
	Heartbeat     string
	WorkerHealthy bool
	Error         bool
}

type Renderer struct {
	templates *template.Template
	// PreviewMode marks the whole workspace as read-and-preview only when cloud writes are disabled.
	PreviewMode bool
}

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
	PreviewMode bool
	Content     template.HTML
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
	case "lightsail_static_ip":
		return "Lightsail 静态 IP"
	case "ec2_instance":
		return "EC2 实例"
	case "ec2_address":
		return "EC2 弹性 IP"
	case "instances":
		return "实例"
	case "lightsail_traffic":
		return "Lightsail 实例流量"
	case "ec2_traffic":
		return "EC2 实例流量"
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

// FormatBytes renders a byte count with binary units, as AWS bills data transfer.
func FormatBytes(value int64) string {
	if value < 1024 {
		return strconv.FormatInt(value, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	size := float64(value) / 1024
	unit := 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	if size >= 100 {
		return strconv.FormatFloat(size, 'f', 0, 64) + " " + units[unit]
	}
	return strconv.FormatFloat(size, 'f', 1, 64) + " " + units[unit]
}

// TrafficBar is one day of the detail panel chart, scaled to the busiest day.
type TrafficBar struct {
	Day, Label          string
	BytesIn, BytesOut   int64
	InHeight, OutHeight float64
}

// TrafficBars scales daily totals into stacked bar heights (percent of the tallest day).
func TrafficBars(days []idc.TrafficDay) []TrafficBar {
	var peak int64
	for _, d := range days {
		peak = max(peak, d.BytesIn+d.BytesOut)
	}
	bars := make([]TrafficBar, 0, len(days))
	for _, d := range days {
		bar := TrafficBar{Day: d.Day, BytesIn: d.BytesIn, BytesOut: d.BytesOut}
		if len(d.Day) == 10 {
			bar.Label = d.Day[5:]
		}
		if peak > 0 {
			bar.InHeight = float64(d.BytesIn*1000/peak) / 10
			bar.OutHeight = float64(d.BytesOut*1000/peak) / 10
		}
		bars = append(bars, bar)
	}
	return bars
}

func NewRenderer() (*Renderer, error) {
	templates, err := template.New("pages").Funcs(template.FuncMap{
		"dashboardMetric": func(values []dc.Metric, key string) dc.Metric {
			for _, v := range values {
				if v.Key == key {
					return v
				}
			}
			return dc.Metric{Key: key}
		},
		"dashboardQuota": func(values []dc.PoolQuota, key string) dc.PoolQuota {
			for _, v := range values {
				if v.Key == key {
					return v
				}
			}
			return dc.PoolQuota{Key: key}
		},
		"number": func(value any) string {
			switch v := value.(type) {
			case float64:
				return strconv.FormatFloat(v, 'f', -1, 64)
			case int64:
				return strconv.FormatInt(v, 10)
			case int:
				return strconv.Itoa(v)
			default:
				return "—"
			}
		},
		"dashboardAccountStatus": func(value string) string {
			return map[string]string{"ready": "可刷新", "disabled": "已停用", "unsupported": "暂不支持的分区", "needs_verification": "待验证连接"}[value]
		},

		"ec2Actions": func() []string {
			return []string{"create", "network", "start", "stop", "reboot", "terminate", "enable_ipv6"}
		},
		"ec2Action": func(v string) string {
			return map[string]string{"network": "准备双栈网络", "create": "创建实例", "start": "启动实例", "stop": "停止实例", "reboot": "重启实例", "terminate": "终止实例", "enable_ipv6": "启用 IPv6", "wavelength_opt_in": "开通 Wavelength"}[v]
		},
		"addressAction": func(value string) string {
			return map[string]string{"allocate": "创建静态 IP", "replace": "更换实例 IP", "attach": "绑定地址", "detach": "解绑地址", "release": "释放地址"}[value]
		},
		"bytes":        FormatBytes,
		"trafficBars":  TrafficBars,
		"trafficTotal": func(in, out int64) int64 { return in + out },
		"initial": func(value string) string {
			for _, r := range value {
				return string(r)
			}
			return "?"
		},
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
			return map[string]string{"direct": "直连", "socks5": "SOCKS5 代理", "http": "HTTP 代理", "proxy": "指定代理", "inherit": "账号默认出口"}[value]
		},
		"verificationStage": func(value string) string {
			return map[string]string{"configuration": "配置检查", "proxy": "代理连接", "network": "网络连接", "tls": "TLS 链路", "aws": "AWS 响应", "identity": "账号身份"}[value]
		},
		"verificationCode": VerificationCode,
		"partitionName": func(value string) string {
			if name, ok := map[string]string{"aws": "AWS 商业区域", "aws-cn": "AWS 中国", "aws-us-gov": "AWS GovCloud"}[value]; ok {
				return name
			}
			return value
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
	return r.Render("layout", layoutModel{Layout: layout, PreviewMode: r.PreviewMode, Content: template.HTML(content)})
}

func Static() fs.FS {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

var verificationCodes = map[string]string{
	"VERIFIED":                "已验证",
	"ACCOUNT_MISMATCH":        "凭据所属账号与配置不一致",
	"CONNECTION_DISABLED":     "连接、账号或代理已停用",
	"CREDENTIALS_EXPIRED":     "临时凭据已过期",
	"CONNECTION_UNVERIFIED":   "角色来源连接尚未验证",
	"CONNECTION_CHANGED":      "配置在验证期间发生变化，请重新验证",
	"VAULT_UNAVAILABLE":       "凭据存储不可用",
	"DEPENDENCY_UNAVAILABLE":  "服务暂不可用",
	"INVALID_INPUT":           "连接配置不完整",
	"NOT_FOUND":               "连接或来源不存在",
	"FORBIDDEN":               "没有权限",
	"TLS_VALIDATION_FAILED":   "AWS TLS 证书验证失败",
	"PROXY_AUTH_FAILED":       "代理用户名或密码错误",
	"PROXY_CONNECTION_FAILED": "代理不可用",
	"TIMEOUT":                 "连接超时",
	"CONNECTION_FAILED":       "无法连接 AWS",
	"InvalidClientTokenId":    "Access Key ID 无效",
	"SignatureDoesNotMatch":   "Secret Access Key 不正确",
	"ExpiredToken":            "临时凭据已过期",
	"AccessDenied":            "AWS 拒绝访问",
}

// VerificationCode turns a stored connection verification code into a short
// Chinese description; unknown AWS error codes are shown as AWS 拒绝 with the code.
func VerificationCode(code string) string {
	if text, ok := verificationCodes[code]; ok {
		return text
	}
	if code == "" {
		return ""
	}
	return "AWS 拒绝：" + code
}
