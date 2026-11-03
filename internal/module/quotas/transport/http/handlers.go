package http

import (
	"context"
	"errors"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/quotas"
	"github.com/herta-network/aws-panel/internal/module/quotas/contract"
	"github.com/herta-network/aws-panel/internal/transport/http/view"
	"github.com/herta-network/aws-panel/pkg/httpx"
	"github.com/herta-network/aws-panel/web"
	"strings"
)

type Handler struct {
	service  quotas.Service
	renderer *web.Renderer
}

func New(service quotas.Service, renderer *web.Renderer) *Handler { return &Handler{service, renderer} }

type model struct {
	Options contract.Options
	Query   contract.Query
	Report  *contract.Report
	Error   string
}
type ReportResponse struct {
	Data      contract.Report `json:"data"`
	RequestID string          `json:"request_id"`
}
type OptionsResponse struct {
	Data      contract.Options `json:"data"`
	RequestID string           `json:"request_id"`
}
type RegionsResponse struct {
	Data      contract.Regions `json:"data"`
	RequestID string           `json:"request_id"`
}

func actor(ctx context.Context) int64 { value, _ := auth.Current(ctx); return value.ID }
func status(err error) (int, *contract.Error) {
	fault := contract.ErrUnavailable
	var own *contract.Error
	if errors.As(err, &own) {
		fault = own
	}
	switch fault.Code {
	case "FORBIDDEN":
		return 403, fault
	case "NOT_FOUND":
		return 404, fault
	case "INVALID_INPUT":
		return 400, fault
	case "CONNECTION_CHANGED", "CONNECTION_UNVERIFIED", "CONNECTION_DISABLED":
		return 409, fault
	}
	return 503, fault
}
func (h *Handler) fail(ctx context.Context, c *app.RequestContext, err error) {
	code, fault := status(err)
	if strings.HasPrefix(string(c.Path()), "/api/") {
		httpx.Failure(c, code, fault.Code, fault.Message)
		return
	}
	body, renderErr := h.renderer.Page("inventory-error", view.Layout(ctx, "配额检测", "quotas"), fault.Message)
	view.HTML(c, code, body, renderErr)
}
func (h *Handler) Page(ctx context.Context, c *app.RequestContext) {
	options, err := h.service.Options(ctx, actor(ctx))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	query := contract.Query{ConnectionID: c.Query("connection_id"), Region: c.Query("region"), Service: c.DefaultQuery("service", "lightsail")}
	data := model{Options: options, Query: query}
	if query.ConnectionID != "" && query.Region != "" {
		report, err := h.service.Read(ctx, actor(ctx), query)
		if err != nil {
			_, fault := status(err)
			data.Error = fault.Message
		} else {
			data.Report = &report
		}
	}
	body, err := h.renderer.Page("quotas", view.Layout(ctx, "配额检测", "quotas"), data)
	view.HTML(c, 200, body, err)
}

// Read returns applied quota and independently verified usage, with explicit unknown fields.
// @Summary Inspect compute and address quotas
// @Tags quotas
// @Produce json
// @Security SessionCookie
// @Param connection_id query string true "Connection UUID"
// @Param region query string true "AWS region"
// @Param service query string true "lightsail, ec2, vpc or bedrock"
// @Success 200 {object} ReportResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /quotas [get]
func (h *Handler) Read(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Read(ctx, actor(ctx), contract.Query{ConnectionID: c.Query("connection_id"), Region: c.Query("region"), Service: c.Query("service")})
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}

// Options returns readable account connections.
// @Summary Get quota query choices
// @Tags quotas
// @Produce json
// @Security SessionCookie
// @Success 200 {object} OptionsResponse
// @Failure 401,403,503 {object} httpx.Envelope
// @Router /quotas/options [get]
func (h *Handler) Options(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Options(ctx, actor(ctx))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}

// Regions lists account-enabled regions for read-only queries.
// @Summary Get quota region choices
// @Tags quotas
// @Produce json
// @Security SessionCookie
// @Param connection_id query string true "Connection UUID"
// @Success 200 {object} RegionsResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /quotas/regions [get]
func (h *Handler) Regions(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Regions(ctx, actor(ctx), c.Query("connection_id"))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}
