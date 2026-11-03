package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/compute"
	"github.com/herta-network/aws-panel/internal/module/compute/contract"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/transport/http/view"
	"github.com/herta-network/aws-panel/pkg/httpx"
	"github.com/herta-network/aws-panel/web"
)

type Handler struct {
	service  compute.Service
	renderer *web.Renderer
}

func New(service compute.Service, renderer *web.Renderer) *Handler {
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
	case "INVALID_INPUT", "INVALID_LOGIN_CONFIG", "NETWORK_NOT_READY", "AWS_INVALID_REQUEST":
		return 400, fault
	case "TARGET_BUSY", "EFFECT_MISMATCH", "RESOURCE_CHANGED", "CATALOGUE_CHANGED", "QUOTA_INSUFFICIENT", "CONFLICT", "PLAN_EXPIRED", "CONNECTION_CHANGED", "CONNECTION_UNVERIFIED", "CONNECTION_DISABLED", "CLOUD_WRITES_DISABLED":
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
	body, err := h.renderer.Page(name, view.Layout(ctx, "EC2 实例", "ec2"), model)
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
	value := contract.Target{ConnectionID: c.Query("connection_id"), Region: c.Query("region"), Action: c.DefaultQuery("action", "create"), InstanceID: c.Query("instance_id"), Name: "panel-ec2", DiskGB: 16, PublicIPv4: true, IPv6: true, LoginMethod: "ssh_key"}
	mode := "instances"
	if c.Query("action") != "" {
		mode = computeView(value.Action)
	}
	data := formView{View: mode, Options: options, Value: value, CSRF: auth.CSRF(ctx)}

	h.render(ctx, c, 200, "ec2", data)
}

// Options returns commercial-account connection choices for region operations.
// @Summary Get EC2 management choices
// @Tags ec2
// @Produce json
// @Security SessionCookie
// @Success 200 {object} OptionsResponse
// @Failure 401,403,503 {object} httpx.Envelope
// @Router /ec2/options [get]
func (h *Handler) Options(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Options(ctx, actor(ctx))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}

// Prepare reads current AWS states without enabling any region.
// @Summary Preview EC2 operations
// @Tags ec2
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param request body contract.Request true "EC2 create and lifecycle targets"
// @Success 201 {object} PreviewResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /ec2/plans [post]
func (h *Handler) Prepare(ctx context.Context, c *app.RequestContext) {
	var req contract.Request
	var err error
	value := contract.Target{}
	if api(c) {
		err = decode(c, &req)
	} else {
		value = contract.Target{ConnectionID: c.PostForm("connection_id"), Region: c.PostForm("region"), Action: c.PostForm("action"), InstanceID: c.PostForm("instance_id")}
		var password string
		if value.Action == "network" {
			value.Name = c.PostForm("network_name")
			value.Zone = c.PostForm("zone")
			value.SSHIPv4CIDR = c.PostForm("ssh_ipv4_cidr")
			value.SSHIPv6CIDR = c.PostForm("ssh_ipv6_cidr")
		}
		if value.Action == "create" {
			disk, parseErr := strconv.ParseInt(c.PostForm("disk_gb"), 10, 32)
			if parseErr != nil {
				err = contract.ErrInvalid
			}
			value.DiskGB = int32(disk)
			value.Name = c.PostForm("name")
			value.ImageID = c.PostForm("image_id")
			value.InstanceType = c.PostForm("instance_type")
			value.SubnetID = c.PostForm("subnet_id")
			value.PublicIPv4 = c.PostForm("public_ipv4") == "true"
			value.IPv6 = c.PostForm("ipv6") == "true"
			value.LoginMethod = c.PostForm("login_method")
			value.KeyPairName = c.PostForm("key_pair_name")
			for _, id := range c.PostArgs().PeekAll("security_group_ids") {
				value.SecurityGroupIDs = append(value.SecurityGroupIDs, string(id))
			}
			password = c.PostForm("root_password")
			if password != c.PostForm("root_password_confirm") {
				err = contract.ErrLogin
			}
		}
		req.Targets = []contract.InputTarget{{Target: value, RootPassword: password}}

	}

	if err == nil {
		result, callErr := h.service.Prepare(ctx, actor(ctx), req)
		err = callErr
		if err == nil {
			if api(c) {
				httpx.Success(c, 201, result)
			} else {
				view.Redirect(c, "/ec2/plans/"+result.ID)
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
	body, renderErr := h.renderer.Render("ec2-form", formView{View: computeView(value.Action), Options: options, Value: value, CSRF: auth.CSRF(ctx), Error: previewMessage(fault)})
	view.HTML(c, code, body, renderErr)
}

// Plan returns the current member's region preview.
// @Summary Get EC2 operation preview
// @Tags ec2
// @Produce json
// @Security SessionCookie
// @Param id path string true "Plan UUID"
// @Success 200 {object} PreviewResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /ec2/plans/{id} [get]
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
	h.render(ctx, c, 200, "ec2-preview", previewView{Preview: result, CSRF: auth.CSRF(ctx), Key: uuid.NewString()})
}

// Submit creates the durable addresses task when cloud writes are enabled.
// @Summary Submit EC2 operation task
// @Tags ec2
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Plan UUID"
// @Param request body SubmitRequest true "Idempotency key"
// @Success 202 {object} JobResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /ec2/plans/{id}/submit [post]
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
// @Summary List EC2 launch choices
// @Tags ec2
// @Produce json
// @Security SessionCookie
// @Param connection_id query string true "Connection UUID"
// @Param region query string true "AWS region"
// @Success 200 {object} CatalogueResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /ec2/catalogue [get]
func (h *Handler) Catalogue(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Catalogue(ctx, actor(ctx), c.Query("connection_id"), c.Query("region"))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}

type ListingResponse struct {
	Data      contract.Listing `json:"data"`
	RequestID string           `json:"request_id"`
}

// Instances lists current EC2 instances.
// @Summary List EC2 instances
// @Tags ec2
// @Produce json
// @Security SessionCookie
// @Param connection_id query string true "Connection UUID"
// @Param region query string true "AWS region"
// @Success 200 {object} ListingResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /ec2/instances [get]
func (h *Handler) Instances(ctx context.Context, c *app.RequestContext) {
	v, err := h.service.Instances(ctx, actor(ctx), c.Query("connection_id"), c.Query("region"))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, v)
}

func computeView(action string) string {
	if action == "create" || action == "network" {
		return action
	}
	return "manage"
}

func previewMessage(fault *contract.Error) string {
	if strings.HasPrefix(fault.Code, "CONNECTION_") {
		return "访问连接当前不可用或已变更。请到 AWS 账号检查连接状态，再重新预览。"
	}
	return fault.Message
}
