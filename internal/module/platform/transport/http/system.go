package http

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/herta-network/aws-panel/internal/module/platform"
	"github.com/herta-network/aws-panel/internal/module/platform/contract"
	"github.com/herta-network/aws-panel/pkg/httpx"
)

type SystemResponse struct {
	Data      *contract.SystemStatus `json:"data"`
	RequestID string                 `json:"request_id"`
}

// SystemStatus returns non-sensitive foundation status.
// @Summary Get system status
// @Tags system
// @Produce json
// @Success 200 {object} SystemResponse
// @Failure 503 {object} httpx.Envelope
// @Router /system [get]
func SystemStatus(service platform.Service) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		value, err := service.Status(ctx)
		if err != nil {
			httpx.Failure(c, 503, "DEPENDENCY_UNAVAILABLE", "服务暂不可用")
			return
		}
		httpx.Success(c, 200, value)
	}
}
