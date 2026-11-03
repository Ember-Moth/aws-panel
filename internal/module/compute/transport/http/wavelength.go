package http

import (
	"context"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/compute/contract"
	"github.com/herta-network/aws-panel/internal/transport/http/view"
	"github.com/herta-network/aws-panel/pkg/httpx"
)

func (h *Handler) Wavelength(ctx context.Context, c *app.RequestContext) {
	options, err := h.service.Options(ctx, actor(ctx))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	body, err := h.renderer.Page("wavelength", view.Layout(ctx, "Wavelength", "wavelength"), formView{Options: options, Value: contract.Target{ConnectionID: c.Query("connection_id"), Region: c.Query("region")}, CSRF: auth.CSRF(ctx)})
	view.HTML(c, 200, body, err)
}

type WavelengthResponse struct {
	Data      contract.WavelengthListing `json:"data"`
	RequestID string                     `json:"request_id"`
}

// WavelengthZones lists enabled and not yet enabled zones in the parent region.
// @Summary List Wavelength Zones and opt-in states
// @Tags wavelength
// @Produce json
// @Security SessionCookie
// @Param connection_id query string true "Connection UUID"
// @Param region query string true "Parent AWS region"
// @Param refresh query boolean false "Refresh zone status from AWS"
// @Success 200 {object} WavelengthResponse
// @Failure 400,401,403,503 {object} httpx.Envelope
// @Router /wavelength/zones [get]
func (h *Handler) WavelengthZones(ctx context.Context, c *app.RequestContext) {
	v, err := h.service.WavelengthZones(ctx, actor(ctx), c.Query("connection_id"), c.Query("region"), c.Query("refresh") == "true")
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, v)
}
