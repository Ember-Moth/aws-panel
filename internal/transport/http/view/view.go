package view

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/web"
	"github.com/cloudwego/hertz/pkg/app"
)

func Layout(ctx context.Context, title, active string) web.Layout {
	layout := web.Layout{Title: title, Active: active, CSRF: auth.CSRF(ctx)}
	if user, ok := auth.Current(ctx); ok {
		layout.User = &web.UserView{ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, Role: user.Role}
	}
	return layout
}
func HTML(c *app.RequestContext, status int, body []byte, err error) {
	c.Header("Cache-Control", "no-store")
	if err != nil {
		c.String(500, "页面暂不可用")
		return
	}
	c.Data(status, "text/html; charset=utf-8", body)
}
func Redirect(c *app.RequestContext, path string) {
	if string(c.GetHeader("HX-Request")) == "true" {
		c.Header("HX-Redirect", path)
		c.Status(200)
		return
	}
	c.Redirect(303, []byte(path))
}
