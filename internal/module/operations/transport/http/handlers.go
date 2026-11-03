package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/sse"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	ic "github.com/herta-network/aws-panel/internal/module/identity/contract"
	"github.com/herta-network/aws-panel/internal/module/operations"
	"github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/transport/http/security"
	"github.com/herta-network/aws-panel/internal/transport/http/view"
	"github.com/herta-network/aws-panel/pkg/httpx"
	"github.com/herta-network/aws-panel/web"
)

type Authenticator interface {
	Authenticate(context.Context, string) (ic.Session, error)
}
type Handler struct {
	service     operations.Service
	renderer    *web.Renderer
	auth        Authenticator
	policy      *security.Policy
	diagnostics bool
	shutdown    <-chan struct{}
}

func New(service operations.Service, renderer *web.Renderer, authenticator Authenticator, policy *security.Policy, diagnostics bool, shutdown <-chan struct{}) *Handler {
	return &Handler{service: service, renderer: renderer, auth: authenticator, policy: policy, diagnostics: diagnostics, shutdown: shutdown}
}

type PlanResponse struct {
	Data      contract.Plan `json:"data"`
	RequestID string        `json:"request_id"`
}
type JobResponse struct {
	Data      contract.Job `json:"data"`
	RequestID string       `json:"request_id"`
}
type JobListResponse struct {
	Data      contract.JobList `json:"data"`
	RequestID string           `json:"request_id"`
}
type DetailResponse struct {
	Data      contract.Detail `json:"data"`
	RequestID string          `json:"request_id"`
}
type EventsResponse struct {
	Data      contract.EventPage `json:"data"`
	RequestID string             `json:"request_id"`
}
type listView struct {
	List        contract.JobList
	Diagnostics bool
}
type detailView struct {
	Detail             contract.Detail
	CSRF               string
	Terminal, CanRetry bool
}
type planView struct {
	Plan      contract.Plan
	CSRF, Key string
}
type diagnosticView struct {
	Request     contract.DiagnosticRequest
	CSRF, Error string
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
	case contract.ErrInvalid.Code:
		return 400, fault
	case contract.ErrForbidden.Code:
		return 403, fault
	case contract.ErrNotFound.Code:
		return 404, fault
	case contract.ErrConflict.Code, contract.ErrExpired.Code, contract.ErrBinding.Code, contract.ErrCancelled.Code, contract.ErrWritesDisabled.Code:
		return 409, fault
	default:
		return 503, fault
	}
}
func fail(c *app.RequestContext, err error) {
	code, fault := status(err)
	httpx.Failure(c, code, fault.Code, fault.Message)
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
func (h *Handler) render(ctx context.Context, c *app.RequestContext, code int, title, name string, value any) {
	body, err := h.renderer.Page(name, view.Layout(ctx, title, "jobs"), value)
	view.HTML(c, code, body, err)
}
func (h *Handler) actionError(c *app.RequestContext, err error) {
	if api(c) {
		fail(c, err)
		return
	}
	code, fault := status(err)
	body, renderErr := h.renderer.Render("job-action-error", fault.Message)
	view.HTML(c, code, body, renderErr)
}

// List returns tasks visible to all of the member's account scopes.
// @Summary List durable tasks
// @Tags jobs
// @Produce json
// @Security SessionCookie
// @Param page query int false "Page number"
// @Success 200 {object} JobListResponse
// @Failure 400,401,403,503 {object} httpx.Envelope
// @Router /jobs [get]
func (h *Handler) List(ctx context.Context, c *app.RequestContext) {
	page := 1
	if value := c.Query("page"); value != "" {
		page, _ = strconv.Atoi(value)
	}
	result, err := h.service.List(ctx, actor(ctx), page)
	if err != nil {
		fail(c, err)
		return
	}
	if api(c) {
		httpx.Success(c, 200, result)
		return
	}
	principal, _ := auth.Current(ctx)
	h.render(ctx, c, 200, "任务中心", "jobs", listView{List: result, Diagnostics: h.diagnostics && principal.Role == "admin"})
}
func makeDetail(value contract.Detail, csrf string) detailView {
	terminal := value.Job.Status == contract.Succeeded || value.Job.Status == contract.Failed || value.Job.Status == contract.Cancelled || value.Job.Status == contract.PartialSuccess
	canRetry := value.CanManage && (value.Job.Status == contract.Attention || !value.Job.CancelRequested && (value.Job.Status == contract.Failed || value.Job.Status == contract.PartialSuccess))
	available := false
	for _, item := range value.Items {
		if !item.CanRecover {
			continue
		}
		for _, step := range item.Steps {
			if value.Job.CancelRequested && step.Status != contract.Attention {
				continue
			}
			if step.Status == contract.Failed || step.Status == contract.Attention {
				available = true
			}
		}
	}
	canRetry = canRetry && available
	return detailView{Detail: value, CSRF: csrf, Terminal: terminal, CanRetry: canRetry}
}

// Detail returns a task and its immutable attempt history.
// @Summary Get task details
// @Tags jobs
// @Produce json
// @Security SessionCookie
// @Param id path string true "Task UUID"
// @Success 200 {object} DetailResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /jobs/{id} [get]
func (h *Handler) Detail(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Detail(ctx, actor(ctx), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	if api(c) {
		httpx.Success(c, 200, result)
		return
	}
	model := makeDetail(result, auth.CSRF(ctx))
	if strings.HasPrefix(string(c.Path()), "/partials/") {
		body, err := h.renderer.Render("job-content", model)
		view.HTML(c, 200, body, err)
		return
	}
	h.render(ctx, c, 200, result.Job.Title, "job-detail", model)
}
func (h *Handler) DiagnosticForm(ctx context.Context, c *app.RequestContext) {
	if !h.diagnostics {
		fail(c, contract.ErrNotFound)
		return
	}
	h.render(ctx, c, 200, "任务自检", "job-diagnostic", diagnosticView{Request: contract.DiagnosticRequest{Count: 2, Behavior: "success"}, CSRF: auth.CSRF(ctx)})
}

// PrepareDiagnostic creates a typed local diagnostic plan, only when enabled.
// @Summary Preview a local task diagnostic
// @Tags jobs
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param request body contract.DiagnosticRequest true "Local diagnostic parameters"
// @Success 201 {object} PlanResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /operations/diagnostics/plans [post]
func (h *Handler) PrepareDiagnostic(ctx context.Context, c *app.RequestContext) {
	if !h.diagnostics {
		fail(c, contract.ErrNotFound)
		return
	}
	var req contract.DiagnosticRequest
	var err error
	if api(c) {
		err = decode(c, &req)
	} else {
		req.Count, _ = strconv.Atoi(c.PostForm("count"))
		req.Behavior = c.PostForm("behavior")
	}
	var result contract.Plan
	if err == nil {
		raw, _ := json.Marshal(req)
		result, err = h.service.Prepare(ctx, actor(ctx), contract.PlanRequest{Operation: "system.diagnostic", InputVersion: 1, Input: raw})
	}
	if err != nil {
		if api(c) {
			fail(c, err)
			return
		}
		code, fault := status(err)
		body, renderErr := h.renderer.Render("job-diagnostic-form", diagnosticView{Request: req, CSRF: auth.CSRF(ctx), Error: fault.Message})
		view.HTML(c, code, body, renderErr)
		return
	}
	if api(c) {
		httpx.Success(c, 201, result)
		return
	}
	view.Redirect(c, "/jobs/plans/"+result.ID)
}

// GetPlan returns an unsubmitted plan owned by the current member.
// @Summary Get operation plan
// @Tags jobs
// @Produce json
// @Security SessionCookie
// @Param id path string true "Plan UUID"
// @Success 200 {object} PlanResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /plans/{id} [get]
func (h *Handler) GetPlan(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Plan(ctx, actor(ctx), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	if api(c) {
		httpx.Success(c, 200, result)
		return
	}
	h.render(ctx, c, 200, "任务预览", "job-plan", planView{Plan: result, CSRF: auth.CSRF(ctx), Key: uuid.NewString()})
}

// Submit atomically creates a task and its first outbox entries.
// @Summary Submit a previewed task
// @Tags jobs
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param request body contract.SubmitRequest true "Plan and idempotency key"
// @Success 202 {object} JobResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /jobs [post]
func (h *Handler) Submit(ctx context.Context, c *app.RequestContext) {
	var req contract.SubmitRequest
	if api(c) {
		if err := decode(c, &req); err != nil {
			fail(c, err)
			return
		}
	} else {
		req.PlanID = c.PostForm("plan_id")
		req.IdempotencyKey = c.PostForm("idempotency_key")
	}
	result, err := h.service.Submit(ctx, actor(ctx), req, auth.Meta(ctx))
	if err != nil {
		h.actionError(c, err)
		return
	}
	if api(c) {
		c.Header("Location", "/api/v1/jobs/"+result.ID)
		httpx.Success(c, 202, result)
		return
	}
	view.Redirect(c, "/jobs/"+result.ID)
}

// Cancel stops unissued steps and retains reconciliation for issued requests.
// @Summary Request task cancellation
// @Tags jobs
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Task UUID"
// @Success 204
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /jobs/{id}/cancel [post]
func (h *Handler) Cancel(ctx context.Context, c *app.RequestContext) {
	if err := h.service.Cancel(ctx, actor(ctx), c.Param("id"), auth.Meta(ctx)); err != nil {
		h.actionError(c, err)
		return
	}
	if api(c) {
		c.Status(204)
		return
	}
	view.Redirect(c, "/jobs/"+c.Param("id"))
}

// Retry schedules reconciliation for selected failed or uncertain steps.
// @Summary Recover selected task targets
// @Tags jobs
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Task UUID"
// @Param request body contract.RetryRequest true "Steps and optional current connection binding"
// @Success 204
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /jobs/{id}/retry [post]
func (h *Handler) Retry(ctx context.Context, c *app.RequestContext) {
	var req contract.RetryRequest
	if api(c) {
		if err := decode(c, &req); err != nil {
			fail(c, err)
			return
		}
	} else {
		req.Version, _ = strconv.ParseInt(c.PostForm("version"), 10, 64)
		req.RefreshConnections = c.PostForm("refresh_connections") == "true"
		for _, value := range c.PostArgs().PeekAll("step_ids") {
			req.StepIDs = append(req.StepIDs, string(value))
		}
	}
	if err := h.service.Retry(ctx, actor(ctx), c.Param("id"), req, auth.Meta(ctx)); err != nil {
		h.actionError(c, err)
		return
	}
	if api(c) {
		c.Status(204)
		return
	}
	view.Redirect(c, "/jobs/"+c.Param("id"))
}
func cursor(c *app.RequestContext) (int64, error) {
	value := c.Query("after")
	if last := string(c.GetHeader("Last-Event-ID")); last != "" {
		value = last
	}
	if value == "" {
		return 0, nil
	}
	after, err := strconv.ParseInt(value, 10, 64)
	if err != nil || after < 0 {
		return 0, contract.ErrInvalid
	}
	return after, nil
}

// Events returns durable progress events after a per-task sequence.
// @Summary Replay task events
// @Tags jobs
// @Produce json
// @Security SessionCookie
// @Param id path string true "Task UUID"
// @Param after query int false "Last received event sequence"
// @Success 200 {object} EventsResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /jobs/{id}/events [get]
func (h *Handler) Events(ctx context.Context, c *app.RequestContext) {
	after, err := cursor(c)
	if err != nil {
		fail(c, err)
		return
	}
	result, err := h.service.Events(ctx, actor(ctx), c.Param("id"), after)
	if err != nil {
		fail(c, err)
		return
	}
	httpx.Success(c, 200, result)
}

// Stream rechecks the live session and account scopes before each event batch.
// @Summary Stream task progress with Last-Event-ID replay
// @Tags jobs
// @Produce text/event-stream
// @Security SessionCookie
// @Param id path string true "Task UUID"
// @Param after query int false "Last received event sequence"
// @Success 200 {string} string "Server-sent progress, complete or access_revoked events"
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /jobs/{id}/stream [get]
func (h *Handler) Stream(ctx context.Context, c *app.RequestContext) {
	after, err := cursor(c)
	if err != nil {
		fail(c, err)
		return
	}
	id := c.Param("id")
	actorID := actor(ctx)
	first, err := h.service.Events(ctx, actorID, id, after)
	if err != nil {
		fail(c, err)
		return
	}
	c.Header("X-Accel-Buffering", "no")
	writer := sse.NewWriter(c)
	c.Header("Cache-Control", "no-store")
	defer writer.Close()
	sessionToken := h.policy.SessionToken(c)
	stream, stop := context.WithTimeout(ctx, 45*time.Second)
	defer stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	page := first
	for {
		check, cancel := context.WithTimeout(stream, 3*time.Second)
		session, err := h.auth.Authenticate(check, sessionToken)
		if err == nil && session.Member.ID != actorID {
			err = contract.ErrForbidden
		}
		if err == nil {
			page, err = h.service.Events(check, actorID, id, after)
		}
		cancel()
		if err != nil {
			_ = writer.WriteEvent("", "access_revoked", []byte(`{}`))
			return
		}
		for _, event := range page.Events {
			data, _ := json.Marshal(event)
			if writer.WriteEvent(strconv.FormatInt(event.Seq, 10), "progress", data) != nil {
				return
			}
			after = event.Seq
		}
		if len(page.Events) == 100 {
			continue
		}
		if page.Terminal {
			_ = writer.WriteEvent("", "complete", []byte(`{}`))
			return
		}
		if writer.WriteKeepAlive() != nil {
			return
		}
		select {
		case <-stream.Done():
			return
		case <-h.shutdown:
			return
		case <-ticker.C:
		}
	}
}
