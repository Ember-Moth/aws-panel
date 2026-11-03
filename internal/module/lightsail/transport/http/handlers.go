package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/module/lightsail"
	"github.com/Ember-Moth/aws-panel/internal/module/lightsail/contract"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/transport/http/view"
	"github.com/Ember-Moth/aws-panel/pkg/httpx"
	"github.com/Ember-Moth/aws-panel/web"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/google/uuid"
)

type Handler struct {
	service  lightsail.Service
	renderer *web.Renderer
}

func New(service lightsail.Service, renderer *web.Renderer) *Handler {
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
	Options     contract.Options
	Rows        []formRow
	CSRF, Error string
}
type formRow struct {
	Target contract.Target
	Names  string
}
type CatalogueResponse struct {
	Data      contract.Catalogue `json:"data"`
	RequestID string             `json:"request_id"`
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
	case "INVALID_INPUT", "INVALID_LOGIN_CONFIG", "ROOT_PASSWORD_UNSUPPORTED", "AWS_INVALID_REQUEST":
		return 400, fault
	case "CONFLICT", "NAME_CONFLICT", "CATALOGUE_CHANGED", "QUOTA_INSUFFICIENT", "REGION_NOT_READY", "PLAN_EXPIRED", "CONNECTION_CHANGED", "CONNECTION_UNVERIFIED", "CONNECTION_DISABLED", "CLOUD_WRITES_DISABLED":
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
	body, err := h.renderer.Page(name, view.Layout(ctx, "创建 Lightsail 实例", "lightsail"), model)
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
	h.render(ctx, c, 200, "lightsail-create", formView{Options: options, CSRF: auth.CSRF(ctx), Rows: []formRow{{Target: contract.Target{IPType: "dualstack"}}}})
}

// Options returns connections permitted for Lightsail creation.
// @Summary Get Lightsail creation choices
// @Tags lightsail
// @Produce json
// @Security SessionCookie
// @Success 200 {object} OptionsResponse
// @Failure 401,403,503 {object} httpx.Envelope
// @Router /lightsail/options [get]
func (h *Handler) Options(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Options(ctx, actor(ctx))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}

// Prepare checks live catalogues and names without creating resources.
// @Summary Preview a Lightsail creation batch
// @Tags lightsail
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param request body contract.CreateRequest true "Per-instance connection, region and launch parameters"
// @Success 201 {object} PreviewResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /lightsail/plans [post]
func (h *Handler) Prepare(ctx context.Context, c *app.RequestContext) {
	var req contract.CreateRequest
	var err error
	rows := []formRow{}
	if api(c) {
		err = decode(c, &req)
	} else {
		fields := []string{"connection_id", "region", "zone", "blueprint_id", "bundle_id", "ip_type", "key_pair_name", "names", "login_method", "root_password", "root_password_confirm"}
		values := make([][][]byte, len(fields))
		for i, name := range fields {
			values[i] = c.PostArgs().PeekAll(name)
		}
		count := len(values[0])
		if count < 1 || count > 50 {
			err = contract.ErrInvalid
		}
		for index, value := range values {
			if index >= 8 && len(value) == 0 {
				values[index] = make([][]byte, count)
				continue
			}
			if len(value) != count {
				err = contract.ErrInvalid
			}
		}
		if err == nil {
			for i := 0; i < count; i++ {
				target := contract.Target{ConnectionID: string(values[0][i]), Region: string(values[1][i]), Zone: string(values[2][i]), BlueprintID: string(values[3][i]), BundleID: string(values[4][i]), IPType: string(values[5][i]), KeyPairName: string(values[6][i]), LoginMethod: string(values[8][i])}
				if target.LoginMethod == "root_password" && string(values[9][i]) != string(values[10][i]) {
					err = contract.ErrLogin
				}
				names := string(values[7][i])
				rows = append(rows, formRow{Target: target, Names: names})
				parts := strings.FieldsFunc(names, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' ' })
				if len(parts) == 0 {
					err = contract.ErrInvalid
				}
				for _, name := range parts {
					copy := target
					copy.Name = name
					req.Targets = append(req.Targets, contract.CreateTarget{Target: copy, RootPassword: string(values[9][i])})
				}
			}
		}
	}
	if err == nil {
		result, callErr := h.service.Prepare(ctx, actor(ctx), req)
		err = callErr
		if err == nil {
			if api(c) {
				httpx.Success(c, 201, result)
			} else {
				view.Redirect(c, "/lightsail/plans/"+result.ID)
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
	if len(rows) == 0 {
		rows = []formRow{{Target: contract.Target{IPType: "dualstack"}}}
	}
	code, fault := status(err)
	body, renderErr := h.renderer.Render("lightsail-form", formView{Options: options, Rows: rows, CSRF: auth.CSRF(ctx), Error: fault.Message})
	view.HTML(c, code, body, renderErr)
}

// Plan returns the current member's Lightsail preview.
// @Summary Get Lightsail creation preview
// @Tags lightsail
// @Produce json
// @Security SessionCookie
// @Param id path string true "Plan UUID"
// @Success 200 {object} PreviewResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /lightsail/plans/{id} [get]
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
	h.render(ctx, c, 200, "lightsail-preview", previewView{Preview: result, CSRF: auth.CSRF(ctx), Key: uuid.NewString()})
}

// Submit creates the durable lightsail-create task when cloud writes are enabled.
// @Summary Submit Lightsail creation task
// @Tags lightsail
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Plan UUID"
// @Param request body SubmitRequest true "Idempotency key"
// @Success 202 {object} JobResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /lightsail/plans/{id}/submit [post]
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

// Catalogue reads live, compatible launch choices for one connection and region.
// @Summary Get Lightsail launch catalogue
// @Tags lightsail
// @Produce json
// @Security SessionCookie
// @Param connection_id query string true "Connection UUID"
// @Param region query string true "AWS region"
// @Param refresh query boolean false "Refresh directory data from AWS"
// @Success 200 {object} CatalogueResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /lightsail/catalogue [get]
func (h *Handler) Catalogue(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Catalogue(ctx, actor(ctx), c.Query("connection_id"), c.Query("region"), c.Query("refresh") == "true")
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}

type RegionsResponse struct {
	Data      contract.Regions `json:"data"`
	RequestID string           `json:"request_id"`
}

// Regions lists Lightsail-supported regions and the current account opt-in states.
// @Summary List Lightsail region choices
// @Tags lightsail
// @Produce json
// @Security SessionCookie
// @Param connection_id query string true "Connection UUID"
// @Param refresh query boolean false "Refresh directory data from AWS"
// @Success 200 {object} RegionsResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /lightsail/regions [get]
func (h *Handler) Regions(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Regions(ctx, actor(ctx), c.Query("connection_id"), c.Query("refresh") == "true")
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}
