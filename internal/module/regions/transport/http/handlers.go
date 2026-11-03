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
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/module/regions"
	"github.com/herta-network/aws-panel/internal/module/regions/contract"
	"github.com/herta-network/aws-panel/internal/transport/http/view"
	"github.com/herta-network/aws-panel/pkg/httpx"
	"github.com/herta-network/aws-panel/web"
)

type Handler struct {
	service  regions.Service
	renderer *web.Renderer
}

func New(service regions.Service, renderer *web.Renderer) *Handler {
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
	Options                    contract.Options
	Selected                   map[string]bool
	SelectionJSON, CSRF, Error string
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
	case "INVALID_INPUT", "REGION_STATE_CONFLICT", "AWS_INVALID_REQUEST":
		return 400, fault
	case "CONFLICT", "PLAN_EXPIRED", "CONNECTION_CHANGED", "CONNECTION_UNVERIFIED", "CONNECTION_DISABLED", "CLOUD_WRITES_DISABLED":
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
	body, err := h.renderer.Page(name, view.Layout(ctx, "启用 AWS 区域", "regions"), model)
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
	h.render(ctx, c, 200, "region-enable", formView{Options: options, SelectionJSON: "[]", CSRF: auth.CSRF(ctx), Selected: map[string]bool{}})
}

// Options returns commercial-account connection choices for region operations.
// @Summary Get region operation choices
// @Tags regions
// @Produce json
// @Security SessionCookie
// @Success 200 {object} OptionsResponse
// @Failure 401,403,503 {object} httpx.Envelope
// @Router /regions/options [get]
func (h *Handler) Options(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Options(ctx, actor(ctx))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}

// Prepare reads current AWS states without enabling any region.
// @Summary Preview a batch region enable operation
// @Tags regions
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param request body contract.EnableRequest true "Account connections and target Regions"
// @Success 201 {object} PreviewResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /regions/plans [post]
func (h *Handler) Prepare(ctx context.Context, c *app.RequestContext) {
	var req contract.EnableRequest
	var err error
	selected := map[string]bool{}
	selection := []string{}
	if api(c) {
		err = decode(c, &req)
	} else {
		for _, pair := range c.PostArgs().PeekAll("targets") {
			id, region, ok := strings.Cut(string(pair), "/")
			if !ok {
				err = contract.ErrInvalid
				break
			}
			selected[id] = true
			selection = append(selection, string(pair))
			req.Targets = append(req.Targets, contract.Target{ConnectionID: id, Region: region})
		}
		for _, id := range c.PostArgs().PeekAll("connection_ids") {
			selected[string(id)] = true
		}
	}
	if err == nil {
		result, callErr := h.service.Prepare(ctx, actor(ctx), req)
		err = callErr
		if err == nil {
			if api(c) {
				httpx.Success(c, 201, result)
			} else {
				view.Redirect(c, "/regions/plans/"+result.ID)
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
	selectionJSON, _ := json.Marshal(selection)
	body, renderErr := h.renderer.Render("region-enable-form", formView{Options: options, Selected: selected, SelectionJSON: string(selectionJSON), CSRF: auth.CSRF(ctx), Error: fault.Message})
	view.HTML(c, code, body, renderErr)
}

// Plan returns the current member's region preview.
// @Summary Get region enable preview
// @Tags regions
// @Produce json
// @Security SessionCookie
// @Param id path string true "Plan UUID"
// @Success 200 {object} PreviewResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /regions/plans/{id} [get]
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
	h.render(ctx, c, 200, "region-preview", previewView{Preview: result, CSRF: auth.CSRF(ctx), Key: uuid.NewString()})
}

// Submit creates the durable region-enable task when cloud writes are enabled.
// @Summary Submit region enable task
// @Tags regions
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Plan UUID"
// @Param request body SubmitRequest true "Idempotency key"
// @Success 202 {object} JobResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /regions/plans/{id}/submit [post]
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
// @Summary List selectable AWS regions
// @Tags regions
// @Produce json
// @Security SessionCookie
// @Param connection_id query string true "Connection UUID"
// @Param refresh query boolean false "Refresh region status from AWS"
// @Success 200 {object} CatalogueResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /regions/catalogue [get]
func (h *Handler) Catalogue(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Catalogue(ctx, actor(ctx), c.Query("connection_id"), c.Query("refresh") == "true")
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}
