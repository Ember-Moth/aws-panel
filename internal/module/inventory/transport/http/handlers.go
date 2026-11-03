package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/inventory"
	"github.com/herta-network/aws-panel/internal/module/inventory/contract"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/transport/http/view"
	"github.com/herta-network/aws-panel/pkg/httpx"
	"github.com/herta-network/aws-panel/web"
)

type Handler struct {
	service  inventory.Service
	renderer *web.Renderer
}

func New(service inventory.Service, renderer *web.Renderer) *Handler {
	return &Handler{service: service, renderer: renderer}
}

type ResourcesResponse struct {
	Data      contract.ResourceList `json:"data"`
	RequestID string                `json:"request_id"`
}
type ScopesResponse struct {
	Data      contract.ScopeList `json:"data"`
	RequestID string             `json:"request_id"`
}
type DetailResponse struct {
	Data      contract.ResourceDetail `json:"data"`
	RequestID string                  `json:"request_id"`
}
type OptionsResponse struct {
	Data      contract.Options `json:"data"`
	RequestID string           `json:"request_id"`
}
type ScheduleResponse struct {
	Data      contract.Schedule `json:"data"`
	RequestID string            `json:"request_id"`
}
type SchedulesResponse struct {
	Data      contract.ScheduleList `json:"data"`
	RequestID string                `json:"request_id"`
}
type DeleteRequest struct {
	Version int64 `json:"version"`
}
type RefreshResponse struct {
	Data      oc.Job `json:"data"`
	RequestID string `json:"request_id"`
}
type listView struct {
	List           contract.ResourceList
	Options        contract.Options
	Filter         contract.Filter
	Scopes         map[string]contract.Scope
	Warnings       []contract.Scope
	Previous, Next string
	Pending        bool
	CanOperate     bool
	Lightsail      bool
}
type scopesView struct {
	List           contract.ScopeList
	Options        contract.Options
	Filter         contract.Filter
	Previous, Next string
}
type detailView struct {
	Detail contract.ResourceDetail
	Back   string
}
type targetRow struct {
	Target  contract.RefreshTarget
	Missing bool
}
type formView struct {
	Options                  contract.Options
	Rows                     []targetRow
	CSRF, Error, Action, Key string
	Schedule                 bool
	New                      bool
	Value                    contract.Schedule
}
type schedulesView struct {
	List contract.ScheduleList
	CSRF string
}

func actor(ctx context.Context) int64 { value, _ := auth.Current(ctx); return value.ID }
func api(c *app.RequestContext) bool  { return strings.HasPrefix(string(c.Path()), "/api/") }
func status(err error) (int, *contract.Error) {
	fault := contract.ErrUnavailable
	var known *contract.Error
	if errors.As(err, &known) {
		fault = known
	}
	switch fault.Code {
	case "FORBIDDEN":
		return 403, fault
	case "NOT_FOUND":
		return 404, fault
	case "INVALID_INPUT", "PARTITION_UNSUPPORTED":
		return 400, fault
	case "CONFLICT", "CONNECTION_CHANGED", "CONNECTION_DISABLED", "CONNECTION_UNVERIFIED", "CREDENTIALS_EXPIRED":
		return 409, fault
	default:
		return 503, fault
	}
}
func (h *Handler) fail(ctx context.Context, c *app.RequestContext, err error) {
	code, fault := status(err)
	if api(c) {
		httpx.Failure(c, code, fault.Code, fault.Message)
		return
	}
	h.render(ctx, c, code, "资源查询", "inventory-error", fault.Message)
}
func (h *Handler) render(ctx context.Context, c *app.RequestContext, code int, title, name string, model any) {
	active := "resources"
	if string(c.Path()) == "/lightsail" {
		active = "lightsail"
	}
	body, err := h.renderer.Page(name, view.Layout(ctx, title, active), model)
	view.HTML(c, code, body, err)
}
func decode(c *app.RequestContext, value any) error {
	if strings.Split(string(c.GetHeader("Content-Type")), ";")[0] != "application/json" {
		return contract.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(c.Request.Body()))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil {
		return contract.ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return contract.ErrInvalid
	}
	return nil
}
func page(c *app.RequestContext) int {
	if value := c.Query("page"); value != "" {
		n, _ := strconv.Atoi(value)
		return n
	}
	return 1
}
func preserveRegion(options *contract.Options, region string) {
	if region == "" {
		return
	}
	for _, value := range options.Regions {
		if value == region {
			return
		}
	}
	options.Regions = append(options.Regions, region)
}
func filter(c *app.RequestContext, defaultKind string) contract.Filter {
	kind := c.Query("kind")
	if kind == "" {
		kind = defaultKind
	}
	return contract.Filter{AccountID: c.Query("account_id"), Kind: kind, Region: c.Query("region"), State: c.Query("state"), Query: c.Query("q"), IncludeMissing: c.Query("include_missing") == "true", Page: page(c)}
}
func pageURL(path string, filter contract.Filter, page int) string {
	if page == 0 {
		return ""
	}
	q := url.Values{"page": {strconv.Itoa(page)}}
	if filter.AccountID != "" {
		q.Set("account_id", filter.AccountID)
	}
	if filter.Kind != "" {
		q.Set("kind", filter.Kind)
	}
	if filter.Region != "" {
		q.Set("region", filter.Region)
	}
	if filter.State != "" {
		q.Set("state", filter.State)
	}
	if filter.Query != "" {
		q.Set("q", filter.Query)
	}
	if filter.IncludeMissing {
		q.Set("include_missing", "true")
	}
	return path + "?" + q.Encode()
}

// Resources returns authorized cached resources and their refresh health.
// @Summary List resource snapshots
// @Tags inventory
// @Produce json
// @Security SessionCookie
// @Param account_id query string false "Account UUID"
// @Param kind query string false "account_region, lightsail_region, lightsail_instance, lightsail_static_ip, ec2_instance or ec2_address"
// @Param region query string false "AWS Region"
// @Param state query string false "AWS state"
// @Param q query string false "Name or resource ID substring"
// @Param include_missing query bool false "Include last-seen resources"
// @Param page query int false "Page number"
// @Success 200 {object} ResourcesResponse
// @Failure 400,401,403,503 {object} httpx.Envelope
// @Router /resources [get]
func (h *Handler) Resources(ctx context.Context, c *app.RequestContext) {
	f := filter(c, contract.LightsailInstance)
	if string(c.Path()) == "/lightsail" {
		f.Kind = contract.LightsailInstance
	}
	result, err := h.service.Resources(ctx, actor(ctx), f)
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	if api(c) {
		httpx.Success(c, 200, result)
		return
	}
	options, err := h.service.Options(ctx, actor(ctx))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	model := listView{List: result, Options: options, Filter: f, Scopes: map[string]contract.Scope{}, Previous: pageURL("/resources", f, result.Previous), Next: pageURL("/resources", f, result.Next)}
	principal, _ := auth.Current(ctx)
	model.CanOperate = principal.Role == "admin" || principal.Role == "operator"
	model.Lightsail = string(c.Path()) == "/lightsail"
	if model.Lightsail {
		model.Previous = pageURL("/lightsail", f, result.Previous)
		model.Next = pageURL("/lightsail", f, result.Next)
	}
	preserveRegion(&model.Options, f.Region)
	for _, scope := range result.Scopes {
		model.Scopes[scope.ID] = scope
		if scope.Status == "failed" || scope.Status == "cancelled" {
			model.Warnings = append(model.Warnings, scope)
		}
		if scope.Status == "syncing" {
			model.Pending = true
		}
	}
	title := "资源快照"
	if model.Lightsail {
		title = "Lightsail 实例"
	}
	h.render(ctx, c, 200, title, "resources", model)
}

// Scopes returns sync outcomes independently from cached resource presence.
// @Summary List inventory sync scopes
// @Tags inventory
// @Produce json
// @Security SessionCookie
// @Param account_id query string false "Account UUID"
// @Param kind query string false "Resource kind"
// @Param region query string false "AWS Region"
// @Param page query int false "Page number"
// @Success 200 {object} ScopesResponse
// @Failure 400,401,403,503 {object} httpx.Envelope
// @Router /resources/scopes [get]
func (h *Handler) Scopes(ctx context.Context, c *app.RequestContext) {
	f := filter(c, "")
	result, err := h.service.Scopes(ctx, actor(ctx), f)
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	if api(c) {
		httpx.Success(c, 200, result)
		return
	}
	options, err := h.service.Options(ctx, actor(ctx))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	preserveRegion(&options, f.Region)
	h.render(ctx, c, 200, "同步范围", "inventory-scopes", scopesView{List: result, Options: options, Filter: f, Previous: pageURL("/resources/scopes", f, result.Previous), Next: pageURL("/resources/scopes", f, result.Next)})
}

// Resource returns a cached resource and the state of its complete sync scope.
// @Summary Get resource snapshot
// @Tags inventory
// @Produce json
// @Security SessionCookie
// @Param id path string true "Snapshot UUID"
// @Success 200 {object} DetailResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /resources/{id} [get]
func (h *Handler) Resource(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Resource(ctx, actor(ctx), c.Param("id"))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	if api(c) {
		httpx.Success(c, 200, result)
		return
	}
	h.render(ctx, c, 200, result.Resource.Name, "resource-detail", detailView{Detail: result, Back: pageURL("/resources", contract.Filter{AccountID: result.Resource.AccountID, Kind: result.Resource.Kind, Region: result.Resource.Region}, 1)})
}

// Options returns scoped account and connection choices without stored secrets.
// @Summary Get resource query choices
// @Tags inventory
// @Produce json
// @Security SessionCookie
// @Success 200 {object} OptionsResponse
// @Failure 401,403,503 {object} httpx.Envelope
// @Router /resources/options [get]
func (h *Handler) Options(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Options(ctx, actor(ctx))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}
func (h *Handler) form(ctx context.Context, targets []contract.RefreshTarget) (formView, error) {
	if len(targets) == 0 {
		targets = []contract.RefreshTarget{{Kind: contract.LightsailInstance, Region: "us-east-1"}}
	}
	options, err := h.service.Options(ctx, actor(ctx))
	if err != nil {
		return formView{}, err
	}
	known := map[string]bool{}
	for _, account := range options.Accounts {
		for _, connection := range account.Connections {
			known[connection.ID] = true
		}
	}
	model := formView{Options: options, CSRF: auth.CSRF(ctx), Rows: []targetRow{}}
	for _, target := range targets {
		model.Rows = append(model.Rows, targetRow{Target: target, Missing: target.ConnectionID != "" && !known[target.ConnectionID]})
	}
	return model, nil
}
func (h *Handler) RefreshForm(ctx context.Context, c *app.RequestContext) {
	kind := c.Query("kind")
	if kind == "" {
		kind = contract.LightsailInstance
	}
	region := c.Query("region")
	if region == "" {
		region = "us-east-1"
	}
	model, err := h.form(ctx, []contract.RefreshTarget{{Kind: kind, Region: region}})
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	model.Action = "/resources/refresh"
	model.Key = uuid.NewString()
	if accountID := c.Query("account_id"); accountID != "" {
		for _, account := range model.Options.Accounts {
			if account.ID != accountID {
				continue
			}
			if len(account.Connections) == 1 {
				model.Rows[0].Target.ConnectionID = account.Connections[0].ID
			}
		}
	}
	h.render(ctx, c, 200, "同步资源", "inventory-refresh", model)
}
func targetsFromForm(c *app.RequestContext) ([]contract.RefreshTarget, error) {
	ids, kinds, regions := c.PostArgs().PeekAll("connection_id"), c.PostArgs().PeekAll("kind"), c.PostArgs().PeekAll("region")
	if len(ids) == 0 || len(ids) > 50 || len(kinds) != len(ids) || len(regions) != len(ids) {
		return nil, contract.ErrInvalid
	}
	targets := make([]contract.RefreshTarget, 0, len(ids))
	for i, id := range ids {
		targets = append(targets, contract.RefreshTarget{ConnectionID: string(id), Kind: string(kinds[i]), Region: string(regions[i])})
	}
	return targets, nil
}

// Refresh submits a typed, read-only inventory job.
// @Summary Refresh selected inventory scopes
// @Tags inventory
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param request body contract.RefreshRequest true "Scopes and idempotency key"
// @Success 202 {object} RefreshResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /resources/refresh [post]
func (h *Handler) Refresh(ctx context.Context, c *app.RequestContext) {
	var req contract.RefreshRequest
	var err error
	if api(c) {
		err = decode(c, &req)
	} else {
		req.Targets, err = targetsFromForm(c)
		req.IdempotencyKey = c.PostForm("idempotency_key")
	}
	if err == nil {
		job, callErr := h.service.Refresh(ctx, actor(ctx), req, auth.Meta(ctx))
		err = callErr
		if err == nil {
			if api(c) {
				c.Header("Location", "/api/v1/jobs/"+job.ID)
				httpx.Success(c, 202, job)
			} else {
				view.Redirect(c, "/jobs/"+job.ID)
			}
			return
		}
	}
	if api(c) {
		h.fail(ctx, c, err)
		return
	}
	model, modelErr := h.form(ctx, req.Targets)
	if modelErr != nil {
		h.fail(ctx, c, modelErr)
		return
	}
	code, fault := status(err)
	model.Error = fault.Message
	model.Action = "/resources/refresh"
	model.Key = req.IdempotencyKey
	body, renderErr := h.renderer.Render("inventory-sync-form", model)
	view.HTML(c, code, body, renderErr)
}

// Schedules lists personal schedules; administrators can inspect all schedules.
// @Summary List recurring inventory syncs
// @Tags inventory
// @Produce json
// @Security SessionCookie
// @Param page query int false "Page number"
// @Success 200 {object} SchedulesResponse
// @Failure 400,401,403,503 {object} httpx.Envelope
// @Router /inventory/schedules [get]
func (h *Handler) Schedules(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Schedules(ctx, actor(ctx), page(c))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	if api(c) {
		httpx.Success(c, 200, result)
		return
	}
	h.render(ctx, c, 200, "定时同步", "inventory-schedules", schedulesView{List: result, CSRF: auth.CSRF(ctx)})
}

// GetSchedule gets a recurring sync owned by this member or visible to an admin.
// @Summary Get recurring inventory sync
// @Tags inventory
// @Produce json
// @Security SessionCookie
// @Param id path string true "Schedule UUID"
// @Success 200 {object} ScheduleResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /inventory/schedules/{id} [get]
func (h *Handler) GetSchedule(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Schedule(ctx, actor(ctx), c.Param("id"))
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	httpx.Success(c, 200, result)
}
func (h *Handler) ScheduleForm(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	value := contract.Schedule{IntervalMinutes: 15, Active: true, Targets: []contract.RefreshTarget{{Kind: contract.LightsailInstance, Region: "us-east-1"}}}
	if id != "" {
		var err error
		value, err = h.service.Schedule(ctx, actor(ctx), id)
		if err != nil {
			h.fail(ctx, c, err)
			return
		}
	}
	model, err := h.form(ctx, value.Targets)
	if err != nil {
		h.fail(ctx, c, err)
		return
	}
	model.Schedule = true
	model.New = id == ""
	model.Value = value
	model.Action = "/resources/schedules"
	if id != "" {
		model.Action += "/" + id
	}
	h.render(ctx, c, 200, "定时同步配置", "inventory-schedule-edit", model)
}

// SaveSchedule creates a schedule using the current member as executing owner.
// @Summary Create recurring inventory sync
// @Tags inventory
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param request body contract.ScheduleRequest true "Complete schedule settings"
// @Success 201 {object} ScheduleResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /inventory/schedules [post]
func (h *Handler) SaveSchedule(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	var req contract.ScheduleRequest
	var err error
	if api(c) {
		err = decode(c, &req)
	} else {
		req.Targets, err = targetsFromForm(c)
		req.Name = c.PostForm("name")
		req.IntervalMinutes, _ = strconv.Atoi(c.PostForm("interval_minutes"))
		req.Active = c.PostForm("active") == "true"
		req.Version, _ = strconv.ParseInt(c.PostForm("version"), 10, 64)
	}
	if err == nil {
		result, callErr := h.service.SaveSchedule(ctx, actor(ctx), id, req, auth.Meta(ctx))
		err = callErr
		if err == nil {
			if api(c) {
				code := 201
				if id != "" {
					code = 200
				}
				httpx.Success(c, code, result)
			} else {
				view.Redirect(c, "/resources/schedules")
			}
			return
		}
	}
	if api(c) {
		h.fail(ctx, c, err)
		return
	}
	model, modelErr := h.form(ctx, req.Targets)
	if modelErr != nil {
		h.fail(ctx, c, modelErr)
		return
	}
	code, fault := status(err)
	model.Error = fault.Message
	model.Schedule = true
	model.New = id == ""
	model.Value = contract.Schedule{ID: id, Name: req.Name, Targets: req.Targets, IntervalMinutes: req.IntervalMinutes, Active: req.Active, Version: req.Version}
	model.Action = "/resources/schedules"
	if id != "" {
		model.Action += "/" + id
	}
	body, renderErr := h.renderer.Render("inventory-schedule-form", model)
	view.HTML(c, code, body, renderErr)
}

// UpdateSchedule replaces settings while preserving the original executing owner.
// @Summary Update recurring inventory sync
// @Tags inventory
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Schedule UUID"
// @Param request body contract.ScheduleRequest true "Complete settings and current version"
// @Success 200 {object} ScheduleResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /inventory/schedules/{id} [patch]
func (h *Handler) UpdateSchedule(ctx context.Context, c *app.RequestContext) { h.SaveSchedule(ctx, c) }

// DeleteSchedule removes only future scheduling, not submitted jobs or AWS resources.
// @Summary Delete recurring inventory sync
// @Tags inventory
// @Accept json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Schedule UUID"
// @Param request body DeleteRequest true "Current version"
// @Success 204
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /inventory/schedules/{id} [delete]
func (h *Handler) DeleteSchedule(ctx context.Context, c *app.RequestContext) {
	var req DeleteRequest
	if api(c) {
		if err := decode(c, &req); err != nil {
			h.fail(ctx, c, err)
			return
		}
	} else {
		req.Version, _ = strconv.ParseInt(c.PostForm("version"), 10, 64)
	}
	if err := h.service.DeleteSchedule(ctx, actor(ctx), c.Param("id"), req.Version, auth.Meta(ctx)); err != nil {
		h.fail(ctx, c, err)
		return
	}
	if api(c) {
		c.Status(204)
		return
	}
	view.Redirect(c, "/resources/schedules")
}
