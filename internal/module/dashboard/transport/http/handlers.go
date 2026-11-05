package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/platform"
	"github.com/Ember-Moth/aws-panel/internal/transport/http/view"
	"github.com/Ember-Moth/aws-panel/pkg/httpx"
	"github.com/Ember-Moth/aws-panel/web"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/google/uuid"
	"io"
	"net/url"
	"strings"
)

type Handler struct {
	service  dashboard.Service
	platform platform.Service
	renderer *web.Renderer
}

func New(service dashboard.Service, platform platform.Service, renderer *web.Renderer) *Handler {
	return &Handler{service, platform, renderer}
}

type OverviewResponse struct {
	Data      contract.Overview `json:"data"`
	RequestID string            `json:"request_id"`
}
type RefreshResponse struct {
	Data      contract.Refresh `json:"data"`
	RequestID string           `json:"request_id"`
}
type pageView struct {
	Overview              contract.Overview
	System                web.SystemView
	CSRF, Key, PartialURL string
	Error                 string
}

func actor(ctx context.Context) int64 { p, _ := auth.Current(ctx); return p.ID }
func status(err error) (int, string, string) {
	var own *contract.Error
	if !errors.As(err, &own) {
		own = contract.ErrUnavailable
	}
	switch own.Code {
	case "INVALID_INPUT":
		return 400, own.Code, own.Message
	case "FORBIDDEN":
		return 403, own.Code, own.Message
	case "NO_VERIFIED_CONNECTION", "TARGET_BUSY", "CONFLICT", "CONNECTION_CHANGED", "CONNECTION_UNVERIFIED", "CONNECTION_DISABLED":
		return 409, own.Code, own.Message
	}
	return 503, own.Code, own.Message
}
func query(c *app.RequestContext) contract.Query {
	return contract.Query{AccountID: c.Query("account_id"), Region: c.Query("region")}
}

// Read shows current permission-scoped account-pool aggregates.
// @Summary Get account-pool dashboard
// @Tags dashboard
// @Produce json
// @Security SessionCookie
// @Param account_id query string false "Filter account UUID"
// @Param region query string false "Filter AWS region"
// @Success 200 {object} OverviewResponse
// @Failure 400,401,403,503 {object} httpx.Envelope
// @Router /dashboard [get]
func (h *Handler) Read(ctx context.Context, c *app.RequestContext) {
	v, err := h.service.Read(ctx, actor(ctx), query(c))
	if err != nil {
		code, key, message := status(err)
		httpx.Failure(c, code, key, message)
		return
	}
	httpx.Success(c, 200, v)
}
func (h *Handler) Page(ctx context.Context, c *app.RequestContext)    { h.page(ctx, c, false) }
func (h *Handler) Partial(ctx context.Context, c *app.RequestContext) { h.page(ctx, c, true) }
func (h *Handler) page(ctx context.Context, c *app.RequestContext, partial bool) {
	q := query(c)
	result, err := h.service.Read(ctx, actor(ctx), q)
	if err != nil {
		code, _, message := status(err)
		if partial {
			c.Header("HX-Reswap", "none")
			if code == 400 || code == 403 {
				c.Header("HX-Redirect", "/")
			} else {
				c.Header("HX-Trigger", "dashboard:error")
			}
			c.Data(code, "text/html; charset=utf-8", nil)
			return
		}
		body, e := h.renderer.Page("inventory-error", view.Layout(ctx, "概览", "dashboard"), message)
		view.HTML(c, code, body, e)
		return
	}

	params := url.Values{}
	if q.AccountID != "" {
		params.Set("account_id", q.AccountID)
	}
	if q.Region != "" {
		params.Set("region", q.Region)
	}
	model := pageView{Overview: result, CSRF: auth.CSRF(ctx), Key: uuid.NewString(), PartialURL: "/partials/dashboard?" + params.Encode()}
	var body []byte
	var renderErr error
	if partial {
		body, renderErr = h.renderer.Render("dashboard-content", model)
	} else {
		system, err := h.platform.Status(ctx)
		if err != nil {
			model.System.Error = true
		} else {
			model.System.Name = system.Name
			model.System.Version = system.Version
			model.System.ServerTime = system.ServerTime.Format("2006-01-02 15:04:05")
			model.System.WorkerHealthy = system.WorkerHealthy
			if system.WorkerHeartbeat != nil {
				model.System.Heartbeat = system.WorkerHeartbeat.Format("2006-01-02 15:04:05")
			}
		}
		body, renderErr = h.renderer.Page("dashboard", view.Layout(ctx, "概览", "dashboard"), model)
	}
	view.HTML(c, 200, body, renderErr)
}

// Refresh starts durable, read-only collection for the selected account pool.
// @Summary Refresh resource and quota aggregates
// @Tags dashboard
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param request body contract.RefreshRequest true "Account-pool collection scope"
// @Success 202 {object} RefreshResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /dashboard/refresh [post]
func (h *Handler) Refresh(ctx context.Context, c *app.RequestContext) {
	var req contract.RefreshRequest
	api := strings.HasPrefix(string(c.Path()), "/api/")
	var err error
	if api {
		if strings.Split(string(c.GetHeader("Content-Type")), ";")[0] != "application/json" {
			err = contract.ErrInvalid
		} else {
			d := json.NewDecoder(bytes.NewReader(c.Request.Body()))
			d.DisallowUnknownFields()
			err = d.Decode(&req)
			var extra any
			if err != nil || d.Decode(&extra) != io.EOF {
				err = contract.ErrInvalid
			}
		}
	} else {
		req = contract.RefreshRequest{AccountID: c.PostForm("account_id"), Region: c.PostForm("region"), IdempotencyKey: c.PostForm("idempotency_key")}
	}
	var result contract.Refresh
	if err == nil {
		result, err = h.service.Refresh(ctx, actor(ctx), req, auth.Meta(ctx))
	}
	if err != nil {
		code, key, message := status(err)
		if api {
			httpx.Failure(c, code, key, message)
		} else {
			body, e := h.renderer.Render("job-action-error", message)
			view.HTML(c, code, body, e)
		}
		return
	}
	if api {
		httpx.Success(c, 202, result)
		return
	}
	body, e := h.renderer.Render("dashboard-refresh-result", result)
	view.HTML(c, 202, body, e)
}
