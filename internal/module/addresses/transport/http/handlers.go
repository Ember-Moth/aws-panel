package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/addresses"
	"github.com/herta-network/aws-panel/internal/module/addresses/contract"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/transport/http/view"
	"github.com/herta-network/aws-panel/pkg/httpx"
	"github.com/herta-network/aws-panel/web"
)

type Handler struct {
	service  addresses.Service
	renderer *web.Renderer
}

func New(service addresses.Service, renderer *web.Renderer) *Handler {
	return &Handler{service: service, renderer: renderer}
}

type PreviewResponse struct {
	Data      contract.Preview `json:"data"`
	RequestID string           `json:"request_id"`
}
type OptionsResponse struct {
	Data      contract.Options `json:"data"`
	RequestID string           `json:"request_id"`
}
type JobResponse struct {
	Data      oc.Job `json:"data"`
	RequestID string `json:"request_id"`
}
type SubmitRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
}
type formView struct {
	View        string
	Options     contract.Options
	Value       contract.Target
	Catalogue   *contract.Catalogue
	CSRF, Error string
}
type previewView struct {
	Preview   contract.Preview
	CSRF, Key string
}

func actor(ctx context.Context) int64 { value, _ := auth.Current(ctx); return value.ID }
func api(c *app.RequestContext) bool  { return strings.HasPrefix(string(c.Path()), "/api/") }
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
	case "INVALID_INPUT", "UNSUPPORTED_TARGET", "AWS_INVALID_REQUEST":
		return 400, fault
	case "TARGET_BUSY", "EFFECT_MISMATCH", "RESOURCE_CHANGED", "ADDRESS_ATTACHED", "QUOTA_INSUFFICIENT", "CONFLICT", "PLAN_EXPIRED", "CONNECTION_CHANGED", "CONNECTION_UNVERIFIED", "CONNECTION_DISABLED", "CLOUD_WRITES_DISABLED":
		return 409, fault
	default:
		return 503, fault
	}
}
func decode(c *app.RequestContext, value any) error {
	if strings.Split(string(c.GetHeader("Content-Type")), ";")[0] != "application/json" {
		return contract.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(c.Request.Body()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return contract.ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return contract.ErrInvalid
	}
	return nil
}
func (h *Handler) render(ctx context.Context, c *app.RequestContext, code int, name string, model any) {
	body, err := h.renderer.Page(name, view.Layout(ctx, "公网 IP", "addresses"), model)
	view.HTML(c, code, body, err)
}
func (h *Handler) fail(ctx context.Context, c *app.RequestContext, err error) {
	code, fault := status(err)
	if api(c) {
		httpx.Failure(c, code, fault.Code, fault.Message)
		return
	}
	h.render(ctx, c, code, "inventory-error", fault.Message)
}
func (h *Handler) Form(ctx context.Context, c *app.RequestContext) {
	options, err := h.service.Options(ctx, actor(ctx))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	value := contract.Target{ConnectionID: c.Query("connection_id"), Region: c.Query("region"), Service: c.DefaultQuery("service", "lightsail"), Action: c.DefaultQuery("action", "allocate"), InstanceID: c.Query("instance_id"), AddressID: c.Query("address_id"), NamePrefix: "panel-ip"}
	if value.Service != "lightsail" && value.Service != "ec2" {
		value.Service = "lightsail"
	}
	mode := "list"
	if c.Query("action") != "" {
		mode = "editor"
	}
	data := formView{View: mode, Options: options, Value: value, CSRF: auth.CSRF(ctx)}

	h.render(ctx, c, 200, "addresses", data)
}

// Options returns commercial-account connection choices for region operations.
// @Summary Get address management choices
// @Tags addresses
// @Produce json
// @Security SessionCookie
// @Success 200 {object} OptionsResponse
// @Failure 401,403,503 {object} httpx.Envelope
// @Router /addresses/options [get]
func (h *Handler) Options(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Options(ctx, actor(ctx))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}

// Prepare reads current AWS states without enabling any region.
// @Summary Preview address operations
// @Tags addresses
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param request body contract.Request true "Address allocation and association targets"
// @Success 201 {object} PreviewResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /addresses/plans [post]
func (h *Handler) Prepare(ctx context.Context, c *app.RequestContext) {
	var req contract.Request
	var err error
	value := contract.Target{}
	if api(c) {
		err = decode(c, &req)
	} else {
		value = contract.Target{ConnectionID: c.PostForm("connection_id"), Region: c.PostForm("region"), Service: c.PostForm("service"), Action: c.PostForm("action"), InstanceID: c.PostForm("instance_id"), AddressID: c.PostForm("address_id"), NamePrefix: c.PostForm("name_prefix"), ReleasePrevious: c.PostForm("release_previous") == "true"}
		req.Targets = []contract.Target{value}
	}

	if err == nil {
		result, callErr := h.service.Prepare(ctx, actor(ctx), req)
		err = callErr
		if err == nil {
			if api(c) {
				httpx.Success(c, 201, result)
			} else {
				view.Redirect(c, "/addresses/plans/"+result.ID)
			}
			return
		}
	}
	if api(c) {
		h.fail(ctx, c, err)
		return
	}
	options, optionsErr := h.service.Options(ctx, actor(ctx))
	if optionsErr != nil {
		h.fail(ctx, c, optionsErr)
		return
	}
	code, fault := status(err)
	body, renderErr := h.renderer.Render("address-form", formView{View: "editor", Options: options, Value: value, CSRF: auth.CSRF(ctx), Error: previewMessage(fault)})
	view.HTML(c, code, body, renderErr)
}

// Plan returns the current member's region preview.
// @Summary Get address operation preview
// @Tags addresses
// @Produce json
// @Security SessionCookie
// @Param id path string true "Plan UUID"
// @Success 200 {object} PreviewResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /addresses/plans/{id} [get]
func (h *Handler) Plan(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Plan(ctx, actor(ctx), c.Param("id"))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	if api(c) {
		httpx.Success(c, 200, result)
		return
	}
	h.render(ctx, c, 200, "address-preview", previewView{Preview: result, CSRF: auth.CSRF(ctx), Key: uuid.NewString()})
}

// Submit creates the durable addresses task when cloud writes are enabled.
// @Summary Submit address operation task
// @Tags addresses
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Plan UUID"
// @Param request body SubmitRequest true "Idempotency key"
// @Success 202 {object} JobResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /addresses/plans/{id}/submit [post]
func (h *Handler) Submit(ctx context.Context, c *app.RequestContext) {
	var req SubmitRequest
	if api(c) {
		if err := decode(c, &req); err != nil {
			h.fail(ctx, c, err)
			return
		}
	} else {
		req.IdempotencyKey = c.PostForm("idempotency_key")
	}
	job, err := h.service.Submit(ctx, actor(ctx), c.Param("id"), req.IdempotencyKey, auth.Meta(ctx))
	if err != nil {
		if api(c) {
			h.fail(ctx, c, err)
			return
		}
		code, fault := status(err)
		body, renderErr := h.renderer.Render("job-action-error", fault.Message)
		view.HTML(c, code, body, renderErr)
		return
	}
	if api(c) {
		c.Header("Location", "/api/v1/jobs/"+job.ID)
		httpx.Success(c, 202, job)
		return
	}
	view.Redirect(c, "/jobs/"+job.ID)
}

type CatalogueResponse struct {
	Data      contract.Catalogue `json:"data"`
	RequestID string             `json:"request_id"`
}

// Catalogue lists the selected account's current region opt-in states.
// @Summary List addresses and eligible instances
// @Tags addresses
// @Produce json
// @Security SessionCookie
// @Param connection_id query string true "Connection UUID"
// @Param region query string true "AWS region"
// @Param service query string true "lightsail or ec2"
// @Success 200 {object} CatalogueResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /addresses/catalogue [get]
func (h *Handler) Catalogue(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Catalogue(ctx, actor(ctx), c.Query("connection_id"), c.Query("region"), c.Query("service"))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}

func previewMessage(fault *contract.Error) string {
	if strings.HasPrefix(fault.Code, "CONNECTION_") {
		return "访问连接当前不可用或已变更。请到 AWS 账号检查连接状态，再重新预览。"
	}
	return fault.Message
}
