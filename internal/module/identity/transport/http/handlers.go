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
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/auth/token"
	"github.com/herta-network/aws-panel/internal/module/identity"
	"github.com/herta-network/aws-panel/internal/module/identity/contract"
	"github.com/herta-network/aws-panel/internal/transport/http/security"
	"github.com/herta-network/aws-panel/internal/transport/http/view"
	"github.com/herta-network/aws-panel/pkg/httpx"
	"github.com/herta-network/aws-panel/web"
)

type Handler struct {
	service  identity.Service
	policy   *security.Policy
	renderer *web.Renderer
}

func New(service identity.Service, policy *security.Policy, renderer *web.Renderer) *Handler {
	return &Handler{service, policy, renderer}
}

type SessionData struct {
	Member    contract.Member `json:"member"`
	ExpiresAt time.Time       `json:"expires_at"`
	CSRFToken string          `json:"csrf_token"`
}
type IdentitySessionResponse struct {
	Data      SessionData `json:"data"`
	RequestID string      `json:"request_id"`
}
type IdentityMemberResponse struct {
	Data      contract.Member `json:"data"`
	RequestID string          `json:"request_id"`
}
type IdentityMembersResponse struct {
	Data      contract.MemberList `json:"data"`
	RequestID string              `json:"request_id"`
}
type CSRFData struct {
	CSRFToken string `json:"csrf_token"`
}
type IdentityCSRFResponse struct {
	Data      CSRFData `json:"data"`
	RequestID string   `json:"request_id"`
}
type loginView struct{ Username, CSRF, Error, Notice string }
type membersView struct {
	List           *contract.MemberList
	Previous, Next int
}
type memberView struct {
	New                 bool
	Member              contract.Member
	CSRF, Action, Error string
}
type profileView struct {
	User        auth.Principal
	CSRF, Error string
}

func status(err error) (int, *contract.Error) {
	var fault *contract.Error
	if !errors.As(err, &fault) {
		return 503, contract.ErrUnavailable
	}
	switch fault.Code {
	case "UNAUTHENTICATED", "INVALID_CREDENTIALS":
		return 401, fault
	case "FORBIDDEN":
		return 403, fault
	case "NOT_FOUND":
		return 404, fault
	case "CONFLICT", "LAST_ADMIN", "ALREADY_INITIALIZED":
		return 409, fault
	case "RATE_LIMITED":
		return 429, fault
	case "INVALID_INPUT":
		return 400, fault
	default:
		return 503, contract.ErrUnavailable
	}
}
func fail(c *app.RequestContext, err error) {
	code, fault := status(err)
	httpx.Failure(c, code, fault.Code, fault.Message)
}
func decode(c *app.RequestContext, value any) error {
	if strings.Split(string(c.GetHeader("Content-Type")), ";")[0] != "application/json" {
		return contract.Invalid("请求需要使用 application/json")
	}
	decoder := json.NewDecoder(bytes.NewReader(c.Request.Body()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return contract.ErrValidation
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return contract.ErrValidation
	}
	return nil
}
func isAPI(c *app.RequestContext) bool { return strings.HasPrefix(string(c.Path()), "/api/") }
func memberID(c *app.RequestContext) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, contract.ErrValidation
	}
	return id, nil
}
func page(c *app.RequestContext) int {
	value := c.Query("page")
	if value == "" {
		return 1
	}
	number, _ := strconv.Atoi(value)
	return number
}

func (h *Handler) LoginPage(ctx context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	if !h.policy.Canonical(c) {
		c.String(400, "请通过配置的面板地址访问，HTTPS 部署需要受信代理")
		return
	}
	if _, err := h.service.Authenticate(ctx, h.policy.SessionToken(c)); err == nil {
		view.Redirect(c, "/")
		return
	}
	csrf, err := h.policy.LoginCSRF(c)
	if err != nil {
		fail(c, contract.ErrUnavailable)
		return
	}
	model := loginView{CSRF: csrf}
	if c.Query("changed") == "1" {
		model.Notice = "密码已更新，请重新登录"
	}
	body, err := h.renderer.Page("login", web.Layout{Title: "登录", CSRF: csrf}, model)
	view.HTML(c, 200, body, err)
}

// CSRFToken initializes the login CSRF cookie.
// @Summary Get a pre-login CSRF token
// @Tags auth
// @Produce json
// @Success 200 {object} IdentityCSRFResponse
// @Failure 400 {object} httpx.Envelope
// @Router /auth/csrf [get]
func (h *Handler) CSRFToken(_ context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	if !h.policy.Canonical(c) {
		httpx.Failure(c, 400, "INVALID_ORIGIN", "请通过配置的面板地址访问")
		return
	}
	csrf, err := h.policy.LoginCSRF(c)
	if err != nil {
		fail(c, contract.ErrUnavailable)
		return
	}
	httpx.Success(c, 200, CSRFData{CSRFToken: csrf})
}

// Login authenticates and sets an HttpOnly session cookie.
// @Summary Sign in using username and password
// @Tags auth
// @Accept json
// @Produce json
// @Param X-CSRF-Token header string true "Token from GET /auth/csrf; send its pre-login cookie too"
// @Param request body contract.LoginRequest true "Credentials"
// @Success 200 {object} IdentitySessionResponse
// @Failure 400,401,403,429,503 {object} httpx.Envelope
// @Router /auth/login [post]
func (h *Handler) Login(ctx context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	var req contract.LoginRequest
	if isAPI(c) {
		if err := decode(c, &req); err != nil {
			fail(c, err)
			return
		}
	} else {
		req = contract.LoginRequest{Username: c.PostForm("username"), Password: c.PostForm("password")}
	}
	grant, err := h.service.Login(ctx, req, auth.Meta(ctx))
	if err != nil {
		code, fault := status(err)
		if code == 429 {
			c.Header("Retry-After", strconv.Itoa(int(h.policy.LoginWindow.Seconds())))
		}
		if isAPI(c) {
			fail(c, err)
			return
		}
		csrf, csrfErr := h.policy.LoginCSRF(c)
		if csrfErr != nil {
			fail(c, contract.ErrUnavailable)
			return
		}
		model := loginView{Username: req.Username, CSRF: csrf, Error: fault.Message}
		var body []byte
		if string(c.GetHeader("HX-Request")) == "true" {
			body, err = h.renderer.Render("login-form", model)
		} else {
			body, err = h.renderer.Page("login", web.Layout{Title: "登录", CSRF: csrf}, model)
		}
		view.HTML(c, code, body, err)
		return
	}
	h.policy.SetSession(c, grant.Token, grant.Session.ExpiresAt)
	if isAPI(c) {
		httpx.Success(c, 200, SessionData{Member: grant.Session.Member, ExpiresAt: grant.Session.ExpiresAt, CSRFToken: token.CSRF(grant.Token, "session")})
		return
	}
	view.Redirect(c, "/")
}

// Session returns the current member and a session-bound CSRF token.
// @Summary Get current session
// @Tags auth
// @Produce json
// @Security SessionCookie
// @Success 200 {object} IdentitySessionResponse
// @Failure 401,503 {object} httpx.Envelope
// @Router /auth/session [get]
func (h *Handler) Session(ctx context.Context, c *app.RequestContext) {
	session, err := h.service.Authenticate(ctx, h.policy.SessionToken(c))
	if err != nil {
		fail(c, err)
		return
	}
	httpx.Success(c, 200, SessionData{Member: session.Member, ExpiresAt: session.ExpiresAt, CSRFToken: auth.CSRF(ctx)})
}

// Logout revokes the calling session.
// @Summary Sign out
// @Tags auth
// @Security SessionCookie && CSRFToken
// @Success 204
// @Failure 401,403,503 {object} httpx.Envelope
// @Router /auth/logout [post]
func (h *Handler) Logout(ctx context.Context, c *app.RequestContext) {
	if err := h.service.Logout(ctx, h.policy.SessionToken(c), auth.Meta(ctx)); err != nil {
		fail(c, err)
		return
	}
	h.policy.ClearSession(c)
	if isAPI(c) {
		c.Status(204)
		return
	}
	view.Redirect(c, "/login")
}

func (h *Handler) Profile(ctx context.Context, c *app.RequestContext) {
	user, _ := auth.Current(ctx)
	body, err := h.renderer.Page("profile", view.Layout(ctx, "个人设置", "profile"), profileView{User: user, CSRF: auth.CSRF(ctx)})
	view.HTML(c, 200, body, err)
}

// ChangePassword revokes every session after updating the password.
// @Summary Change own password and sign out all sessions
// @Tags auth
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param request body contract.ChangePasswordRequest true "Current and new password"
// @Success 204
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /auth/password [post]
func (h *Handler) ChangePassword(ctx context.Context, c *app.RequestContext) {
	var req contract.ChangePasswordRequest
	var err error
	if isAPI(c) {
		err = decode(c, &req)
	} else {
		req = contract.ChangePasswordRequest{CurrentPassword: c.PostForm("current_password"), NewPassword: c.PostForm("new_password")}
		if req.NewPassword != c.PostForm("confirm_password") {
			err = contract.Invalid("两次输入的新密码不一致")
		}
	}
	user, _ := auth.Current(ctx)
	if err == nil {
		err = h.service.ChangePassword(ctx, user.ID, req, auth.Meta(ctx))
	}
	if err != nil {
		if isAPI(c) {
			fail(c, err)
			return
		}
		code, fault := status(err)
		model := profileView{User: user, CSRF: auth.CSRF(ctx), Error: fault.Message}
		var body []byte
		if string(c.GetHeader("HX-Request")) == "true" {
			body, err = h.renderer.Render("password-form", model)
		} else {
			body, err = h.renderer.Page("profile", view.Layout(ctx, "个人设置", "profile"), model)
		}
		view.HTML(c, code, body, err)
		return
	}
	h.policy.ClearSession(c)
	if isAPI(c) {
		c.Status(204)
		return
	}
	view.Redirect(c, "/login?changed=1")
}

// ListMembers lists team members.
// @Summary List members
// @Tags members
// @Produce json
// @Security SessionCookie
// @Param page query int false "Page number" default(1)
// @Success 200 {object} IdentityMembersResponse
// @Failure 400,401,403,503 {object} httpx.Envelope
// @Router /members [get]
func (h *Handler) ListMembers(ctx context.Context, c *app.RequestContext) {
	user, _ := auth.Current(ctx)
	list, err := h.service.ListMembers(ctx, user.ID, page(c))
	if err != nil {
		fail(c, err)
		return
	}
	if isAPI(c) {
		httpx.Success(c, 200, list)
		return
	}
	model := membersView{List: list}
	if list.Page > 1 {
		model.Previous = list.Page - 1
	}
	if int64(list.Page*list.PageSize) < list.Total {
		model.Next = list.Page + 1
	}
	body, err := h.renderer.Page("members", view.Layout(ctx, "成员管理", "members"), model)
	view.HTML(c, 200, body, err)
}

func (h *Handler) NewMemberPage(ctx context.Context, c *app.RequestContext) {
	model := memberView{New: true, Member: contract.Member{Role: contract.RoleViewer, Active: true}, CSRF: auth.CSRF(ctx), Action: "/members"}
	body, err := h.renderer.Page("member-edit", view.Layout(ctx, "新增成员", "members"), model)
	view.HTML(c, 200, body, err)
}
func (h *Handler) EditMemberPage(ctx context.Context, c *app.RequestContext) {
	id, err := memberID(c)
	if err != nil {
		fail(c, err)
		return
	}
	user, _ := auth.Current(ctx)
	member, err := h.service.Member(ctx, user.ID, id)
	if err != nil {
		fail(c, err)
		return
	}
	model := memberView{Member: member, CSRF: auth.CSRF(ctx), Action: "/members/" + strconv.FormatInt(id, 10)}
	body, err := h.renderer.Page("member-edit", view.Layout(ctx, "编辑成员", "members"), model)
	view.HTML(c, 200, body, err)
}

// CreateMember creates an active member.
// @Summary Create member
// @Tags members
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param request body contract.CreateMemberRequest true "Member"
// @Success 201 {object} IdentityMemberResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /members [post]
func (h *Handler) CreateMember(ctx context.Context, c *app.RequestContext) {
	var req contract.CreateMemberRequest
	if isAPI(c) {
		if err := decode(c, &req); err != nil {
			fail(c, err)
			return
		}
	} else {
		req = contract.CreateMemberRequest{Username: c.PostForm("username"), DisplayName: c.PostForm("display_name"), Role: c.PostForm("role"), Password: c.PostForm("password")}
	}
	user, _ := auth.Current(ctx)
	member, err := h.service.CreateMember(ctx, user.ID, req, auth.Meta(ctx))
	if err != nil {
		if isAPI(c) {
			fail(c, err)
			return
		}
		h.memberError(ctx, c, err, memberView{New: true, Member: contract.Member{Username: req.Username, DisplayName: req.DisplayName, Role: req.Role, Active: true}, Action: "/members"})
		return
	}
	if isAPI(c) {
		httpx.Success(c, 201, member)
		return
	}
	view.Redirect(c, "/members")
}

// UpdateMember updates display name, role and availability.
// @Summary Update member
// @Tags members
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param id path int true "Member ID"
// @Param request body contract.UpdateMemberRequest true "Member update, including current version"
// @Success 200 {object} IdentityMemberResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /members/{id} [patch]
func (h *Handler) UpdateMember(ctx context.Context, c *app.RequestContext) {
	id, err := memberID(c)
	if err != nil {
		fail(c, err)
		return
	}
	var req contract.UpdateMemberRequest
	if isAPI(c) {
		if err := decode(c, &req); err != nil {
			fail(c, err)
			return
		}
	} else {
		active := c.PostForm("active") == "true"
		version, _ := strconv.ParseInt(c.PostForm("version"), 10, 64)
		req = contract.UpdateMemberRequest{DisplayName: c.PostForm("display_name"), Role: c.PostForm("role"), Active: &active, Version: version}
	}
	user, _ := auth.Current(ctx)
	member, err := h.service.UpdateMember(ctx, user.ID, id, req, auth.Meta(ctx))
	if err != nil {
		if isAPI(c) {
			fail(c, err)
			return
		}
		active := req.Active != nil && *req.Active
		h.memberError(ctx, c, err, memberView{Member: contract.Member{ID: id, Username: c.PostForm("username"), DisplayName: req.DisplayName, Role: req.Role, Active: active, Version: req.Version}, Action: "/members/" + strconv.FormatInt(id, 10)})
		return
	}
	revoked := id == user.ID && (!member.Active || member.Role != user.Role)
	if revoked {
		h.policy.ClearSession(c)
	}
	if isAPI(c) {
		httpx.Success(c, 200, member)
		return
	}
	if revoked {
		view.Redirect(c, "/login")
		return
	}
	view.Redirect(c, "/members")
}
func (h *Handler) memberError(ctx context.Context, c *app.RequestContext, err error, model memberView) {
	code, fault := status(err)
	model.Error = fault.Message
	model.CSRF = auth.CSRF(ctx)
	var body []byte
	if string(c.GetHeader("HX-Request")) == "true" {
		body, err = h.renderer.Render("member-form", model)
	} else {
		body, err = h.renderer.Page("member-edit", view.Layout(ctx, "成员管理", "members"), model)
	}
	view.HTML(c, code, body, err)
}
