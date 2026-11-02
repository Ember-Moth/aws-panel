package routes

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"mime"
	"path"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/herta-network/aws-panel/internal/module/platform"
	platformhttp "github.com/herta-network/aws-panel/internal/module/platform/transport/http"
	"github.com/herta-network/aws-panel/web"
)

type Dependencies struct {
	Platform platform.Service
	Renderer *web.Renderer
	Ready    func(context.Context) bool
}

func Register(h *server.Hertz, deps Dependencies) {
	h.GET("/health/live", func(_ context.Context, c *app.RequestContext) {
		c.JSON(200, map[string]string{"status": "alive"})
	})
	h.GET("/health/ready", func(ctx context.Context, c *app.RequestContext) {
		c.Header("Cache-Control", "no-store")
		if !deps.Ready(ctx) {
			c.JSON(503, map[string]string{"status": "not_ready"})
			return
		}
		c.JSON(200, map[string]string{"status": "ready"})
	})
	h.GET("/api/v1/system", platformhttp.SystemStatus(deps.Platform))
	h.GET("/", platformhttp.Dashboard(deps.Platform, deps.Renderer))
	h.GET("/partials/system-status", platformhttp.StatusPartial(deps.Platform, deps.Renderer))
	h.GET("/static/*path", static(web.Static()))
}

func static(files fs.FS) app.HandlerFunc {
	return func(_ context.Context, c *app.RequestContext) {
		name := strings.TrimPrefix(c.Param("path"), "/")
		if !fs.ValidPath(name) {
			c.Status(404)
			return
		}
		body, err := fs.ReadFile(files, name)
		if err != nil {
			c.Status(404)
			return
		}
		etag := fmt.Sprintf("\"%x\"", sha256.Sum256(body))
		c.Header("ETag", etag)
		c.Header("Cache-Control", "no-cache")
		if string(c.Request.Header.Peek("If-None-Match")) == etag {
			c.Status(304)
			return
		}
		contentType := mime.TypeByExtension(path.Ext(name))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		c.Data(200, contentType, body)
	}
}
