package middleware

import (
	"context"
	"errors"
	"strings"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/auth/token"
	"github.com/Ember-Moth/aws-panel/internal/module/identity"
	"github.com/Ember-Moth/aws-panel/internal/module/identity/contract"
	"github.com/Ember-Moth/aws-panel/internal/transport/http/security"
	"github.com/Ember-Moth/aws-panel/pkg/httpx"
	"github.com/cloudwego/hertz/pkg/app"
)

type Authenticator interface {
	Authenticate(context.Context, string) (contract.Session, error)
}

func RequireSession(service Authenticator, policy *security.Policy, action string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Header("Cache-Control", "no-store")
		if !policy.Canonical(c) {
			httpx.Failure(c, 400, "INVALID_ORIGIN", "请通过配置的面板地址访问，HTTPS 部署需要受信代理")
			c.Abort()
			return
		}
		raw := policy.SessionToken(c)
		session, err := service.Authenticate(ctx, raw)
		if err != nil {
			if errors.Is(err, contract.ErrUnauthenticated) {
				policy.ClearSession(c)
				if strings.EqualFold(string(c.GetHeader("HX-Request")), "true") {
					c.Header("HX-Redirect", "/login")
				}
				if strings.HasPrefix(string(c.Path()), "/api/") || strings.EqualFold(string(c.GetHeader("HX-Request")), "true") {
					httpx.Failure(c, 401, "UNAUTHENTICATED", "请先登录")
				} else {
					c.Redirect(303, []byte("/login"))
				}
			} else {
				httpx.Failure(c, 503, "DEPENDENCY_UNAVAILABLE", "服务暂不可用")
			}
			c.Abort()
			return
		}
		if action != "" && !identity.Allowed(session.Member.Role, action) {
			if strings.HasPrefix(string(c.Path()), "/api/") {
				httpx.Failure(c, 403, "FORBIDDEN", "没有执行此操作的权限")
			} else {
				c.String(403, "没有执行此操作的权限")
			}
			c.Abort()
			return
		}
		member := session.Member
		ctx = auth.WithPrincipal(ctx, auth.Principal{ID: member.ID, Username: member.Username, DisplayName: member.DisplayName, Role: member.Role}, token.CSRF(raw, "session"))
		c.Next(ctx)
	}
}

func CSRF(policy *security.Policy, login bool) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if !policy.CheckCSRF(c, login) {
			if strings.HasPrefix(string(c.Path()), "/api/") {
				httpx.Failure(c, 403, "CSRF_REJECTED", "请求验证失败，请刷新页面后重试")
			} else {
				c.Data(403, "text/html; charset=utf-8", []byte("<div class=\"alert alert-danger\" role=\"alert\">请求验证失败，请刷新页面后重试。</div>"))
			}
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}
