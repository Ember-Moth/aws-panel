package http

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/platform"
	"github.com/herta-network/aws-panel/internal/transport/http/view"
	"github.com/herta-network/aws-panel/web"
)

func Dashboard(service platform.Service, renderer *web.Renderer) app.HandlerFunc {
	return render(service, renderer, "dashboard")
}

func StatusPartial(service platform.Service, renderer *web.Renderer) app.HandlerFunc {
	return render(service, renderer, "system-status")
}

func render(service platform.Service, renderer *web.Renderer, name string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		principal, _ := auth.Current(ctx)
		model := web.SystemView{CanOperate: principal.Role == "admin" || principal.Role == "operator"}
		status := 200
		value, err := service.Status(ctx)
		if err != nil {
			model.Error = true
			status = 503
		} else {
			model.Name = value.Name
			model.Version = value.Version
			model.ServerTime = value.ServerTime.Format("2006-01-02 15:04:05")
			if heartbeat := value.WorkerHeartbeat; heartbeat != nil {
				model.Heartbeat = heartbeat.Format("2006-01-02 15:04:05")
				model.WorkerHealthy = value.WorkerHealthy
			}
		}
		var body []byte
		var renderErr error
		if name == "dashboard" {
			body, renderErr = renderer.Page(name, view.Layout(ctx, "工作台", "dashboard"), model)
		} else {
			body, renderErr = renderer.Render(name, model)
		}
		if renderErr != nil {
			c.String(500, "页面暂不可用")
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Data(status, "text/html; charset=utf-8", body)
	}
}
