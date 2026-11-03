package http

import (
	"context"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/herta-network/aws-panel/internal/module/platform"
	"github.com/herta-network/aws-panel/internal/module/platform/contract"
	"github.com/herta-network/aws-panel/internal/transport/http/view"
	"github.com/herta-network/aws-panel/pkg/httpx"
	"github.com/herta-network/aws-panel/web"
)

type AuditResponse struct {
	Data      contract.AuditList `json:"data"`
	RequestID string             `json:"request_id"`
}

// Audit lists audit events for an administrator.
// @Summary List audit events
// @Tags audit
// @Produce json
// @Security SessionCookie
// @Param before query int false "Return entries older than this ID"
// @Success 200 {object} AuditResponse
// @Failure 400,401,403,503 {object} httpx.Envelope
// @Router /audit [get]
func Audit(service platform.Service, renderer *web.Renderer) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		var before int64
		if value := c.Query("before"); value != "" {
			var err error
			before, err = strconv.ParseInt(value, 10, 64)
			if err != nil || before < 1 {
				httpx.Failure(c, 400, "INVALID_INPUT", "分页参数不正确")
				return
			}
		}
		result, err := service.ListAudit(ctx, before)
		if err != nil {
			httpx.Failure(c, 503, "DEPENDENCY_UNAVAILABLE", "服务暂不可用")
			return
		}
		if strings.HasPrefix(string(c.Path()), "/api/") {
			httpx.Success(c, 200, result)
			return
		}
		body, err := renderer.Page("audit", view.Layout(ctx, "审计记录", "audit"), result)
		view.HTML(c, 200, body, err)
	}
}
