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
	"github.com/herta-network/aws-panel/internal/infra/clock"
	"github.com/herta-network/aws-panel/internal/module/accounts"
	"github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/transport/http/view"
	"github.com/herta-network/aws-panel/pkg/httpx"
	"github.com/herta-network/aws-panel/web"
)

type Handler struct {
	service  accounts.Service
	renderer *web.Renderer
}

func New(service accounts.Service, renderer *web.Renderer) *Handler {
	return &Handler{service, renderer}
}

type AccountResponse struct {
	Data      contract.Account `json:"data"`
	RequestID string           `json:"request_id"`
}
type AccountsResponse struct {
	Data      []contract.Account `json:"data"`
	RequestID string             `json:"request_id"`
}
type AccountDetailResponse struct {
	Data      contract.AccountDetail `json:"data"`
	RequestID string                 `json:"request_id"`
}
type ProxyResponse struct {
	Data      contract.Proxy `json:"data"`
	RequestID string         `json:"request_id"`
}
type ProxiesResponse struct {
	Data      []contract.Proxy `json:"data"`
	RequestID string           `json:"request_id"`
}
type ConnectionResponse struct {
	Data      contract.Connection `json:"data"`
	RequestID string              `json:"request_id"`
}
type VerificationResponse struct {
	Data      contract.Verification `json:"data"`
	RequestID string                `json:"request_id"`
}
type DeleteRequest struct {
	Version int64 `json:"version"`
}
type accountsView struct {
	Items                 []contract.Account
	CanManage, VaultReady bool
}
type accountForm struct {
	Account             contract.Account
	New                 bool
	CSRF, Error, Action string
}
type proxyForm struct {
	Proxy               contract.Proxy
	New, VaultReady     bool
	CSRF, Error, Action string
}
type proxiesView struct {
	Items      []contract.Proxy
	VaultReady bool
}
type connectionForm struct {
	Connection          contract.Connection
	Account             contract.Account
	Proxies             []contract.Proxy
	Sources             []contract.Connection
	New, VaultReady     bool
	CSRF, Error, Action string
}
type detailView struct {
	Detail *contract.AccountDetail
	CSRF   string
}
type verificationView struct {
	Result contract.Verification
	ID     string
	CSRF   string
}

func api(c *app.RequestContext) bool  { return strings.HasPrefix(string(c.Path()), "/api/") }
func actor(ctx context.Context) int64 { value, _ := auth.Current(ctx); return value.ID }
func status(err error) (int, *contract.Error) {
	var fault *contract.Error
	if !errors.As(err, &fault) {
		return 503, contract.ErrUnavailable
	}
	switch fault.Code {
	case "FORBIDDEN":
		return 403, fault
	case "NOT_FOUND":
		return 404, fault
	case "CONFLICT", "CONNECTION_CHANGED":
		return 409, fault
	case "INVALID_INPUT":
		return 400, fault
	case "CONNECTION_DISABLED", "CREDENTIALS_EXPIRED", "CONNECTION_UNVERIFIED":
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
func version(c *app.RequestContext) int64 {
	v, _ := strconv.ParseInt(c.PostForm("version"), 10, 64)
	return v
}
func active(c *app.RequestContext) *bool { v := c.PostForm("active") == "true"; return &v }
func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func (h *Handler) render(ctx context.Context, c *app.RequestContext, code int, title, page, fragment, nav string, model any) {
	var body []byte
	var err error
	if fragment != "" && string(c.GetHeader("HX-Request")) == "true" {
		body, err = h.renderer.Render(fragment, model)
	} else {
		body, err = h.renderer.Page(page, view.Layout(ctx, title, nav), model)
	}
	view.HTML(c, code, body, err)
}

// ListAccounts lists only accounts visible to the current member.
// @Summary List AWS accounts
// @Tags accounts
// @Produce json
// @Security SessionCookie
// @Success 200 {object} AccountsResponse
// @Failure 401,403,503 {object} httpx.Envelope
// @Router /accounts [get]
func (h *Handler) ListAccounts(ctx context.Context, c *app.RequestContext) {
	values, err := h.service.ListAccounts(ctx, actor(ctx))
	if err != nil {
		fail(c, err)
		return
	}
	if api(c) {
		httpx.Success(c, 200, values)
		return
	}
	user, _ := auth.Current(ctx)
	h.render(ctx, c, 200, "AWS 账号", "accounts", "", "accounts", accountsView{Items: values, CanManage: user.Role == "admin", VaultReady: h.service.VaultReady()})
}

// Detail shows an authorized account and redacted connection information.
// @Summary Get AWS account
// @Tags accounts
// @Produce json
// @Security SessionCookie
// @Param id path string true "Account UUID"
// @Success 200 {object} AccountDetailResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /accounts/{id} [get]
func (h *Handler) Detail(ctx context.Context, c *app.RequestContext) {
	value, err := h.service.AccountDetail(ctx, actor(ctx), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	if api(c) {
		httpx.Success(c, 200, value)
		return
	}
	h.render(ctx, c, 200, value.Account.Name, "account-detail", "", "accounts", detailView{Detail: value, CSRF: auth.CSRF(ctx)})
}
func (h *Handler) AccountForm(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	model := accountForm{New: id == "", Account: contract.Account{Partition: "aws", Active: true}, CSRF: auth.CSRF(ctx), Action: "/accounts"}
	if id != "" {
		detail, err := h.service.AccountDetail(ctx, actor(ctx), id)
		if err != nil {
			fail(c, err)
			return
		}
		model.Account = detail.Account
		model.Action += "/" + id
	}
	h.render(ctx, c, 200, "账号配置", "account-edit", "", "accounts", model)
}

// SaveAccount creates or updates AWS account metadata.
// @Summary Create AWS account
// @Tags accounts
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param request body contract.AccountRequest true "Account"
// @Success 201 {object} AccountResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /accounts [post]
func (h *Handler) SaveAccount(ctx context.Context, c *app.RequestContext) {
	var req contract.AccountRequest
	id := c.Param("id")
	if api(c) {
		if err := decode(c, &req); err != nil {
			fail(c, err)
			return
		}
	} else {
		req = contract.AccountRequest{Name: c.PostForm("name"), Partition: c.PostForm("partition"), AWSAccountID: c.PostForm("aws_account_id"), Active: active(c), Version: version(c)}
	}
	value, err := h.service.SaveAccount(ctx, actor(ctx), id, req, auth.Meta(ctx))
	if err != nil {
		if api(c) {
			fail(c, err)
			return
		}
		code, fault := status(err)
		action := "/accounts"
		if id != "" {
			action += "/" + id
		}
		h.render(ctx, c, code, "账号配置", "account-edit", "account-form", "accounts", accountForm{New: id == "", Account: contract.Account{ID: id, Name: req.Name, Partition: req.Partition, AWSAccountID: req.AWSAccountID, Active: req.Active != nil && *req.Active, Version: req.Version}, Action: action, CSRF: auth.CSRF(ctx), Error: fault.Message})
		return
	}
	if api(c) {
		code := 201
		if id != "" {
			code = 200
		}
		httpx.Success(c, code, value)
		return
	}
	view.Redirect(c, "/accounts/"+value.ID)
}

// UpdateAccount updates an existing account.
// @Summary Update AWS account
// @Tags accounts
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Account UUID"
// @Param request body contract.AccountRequest true "Account"
// @Success 200 {object} AccountResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /accounts/{id} [patch]
func (h *Handler) UpdateAccount(ctx context.Context, c *app.RequestContext) { h.SaveAccount(ctx, c) }

// ListProxies lists proxy profiles without authentication values.
// @Summary List proxy profiles
// @Tags proxies
// @Produce json
// @Security SessionCookie
// @Success 200 {object} ProxiesResponse
// @Failure 401,403,503 {object} httpx.Envelope
// @Router /proxies [get]
func (h *Handler) ListProxies(ctx context.Context, c *app.RequestContext) {
	values, err := h.service.ListProxies(ctx, actor(ctx))
	if err != nil {
		fail(c, err)
		return
	}
	if api(c) {
		httpx.Success(c, 200, values)
		return
	}
	h.render(ctx, c, 200, "代理配置", "proxies", "", "proxies", proxiesView{Items: values, VaultReady: h.service.VaultReady()})
}

// GetProxy gets a redacted profile.
// @Summary Get proxy profile
// @Tags proxies
// @Produce json
// @Security SessionCookie
// @Param id path string true "Proxy UUID"
// @Success 200 {object} ProxyResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /proxies/{id} [get]
func (h *Handler) GetProxy(ctx context.Context, c *app.RequestContext) {
	value, err := h.service.Proxy(ctx, actor(ctx), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	httpx.Success(c, 200, value)
}
func (h *Handler) ProxyForm(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	model := proxyForm{New: id == "", Proxy: contract.Proxy{Active: true, Port: 1080}, VaultReady: h.service.VaultReady(), CSRF: auth.CSRF(ctx), Action: "/proxies"}
	if id != "" {
		value, err := h.service.Proxy(ctx, actor(ctx), id)
		if err != nil {
			fail(c, err)
			return
		}
		model.Proxy = value
		model.Action += "/" + id
	}
	h.render(ctx, c, 200, "代理配置", "proxy-edit", "", "proxies", model)
}

// SaveProxy creates a proxy profile.
// @Summary Create proxy profile
// @Tags proxies
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param request body contract.ProxyRequest true "Proxy"
// @Success 201 {object} ProxyResponse
// @Failure 400,401,403,409,503 {object} httpx.Envelope
// @Router /proxies [post]
func (h *Handler) SaveProxy(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	var req contract.ProxyRequest
	if api(c) {
		if err := decode(c, &req); err != nil {
			fail(c, err)
			return
		}
	} else {
		port, _ := strconv.Atoi(c.PostForm("port"))
		req = contract.ProxyRequest{Name: c.PostForm("name"), Host: c.PostForm("host"), Port: port, Active: active(c), Version: version(c), ClearAuth: c.PostForm("clear_auth") == "true"}
		if user, pass := c.PostForm("username"), c.PostForm("password"); user != "" || pass != "" {
			req.Auth = &contract.ProxyAuth{Username: user, Password: pass}
		}
	}
	value, err := h.service.SaveProxy(ctx, actor(ctx), id, req, auth.Meta(ctx))
	if err != nil {
		if api(c) {
			fail(c, err)
			return
		}
		code, fault := status(err)
		action := "/proxies"
		if id != "" {
			action += "/" + id
		}
		h.render(ctx, c, code, "代理配置", "proxy-edit", "proxy-form", "proxies", proxyForm{New: id == "", Proxy: contract.Proxy{ID: id, Name: req.Name, Host: req.Host, Port: req.Port, Version: req.Version, Active: req.Active != nil && *req.Active}, VaultReady: h.service.VaultReady(), CSRF: auth.CSRF(ctx), Action: action, Error: fault.Message})
		return
	}
	if api(c) {
		code := 201
		if id != "" {
			code = 200
		}
		httpx.Success(c, code, value)
		return
	}
	view.Redirect(c, "/proxies")
}

// UpdateProxy updates a profile; omitted auth retains the current secret.
// @Summary Update proxy profile
// @Tags proxies
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Proxy UUID"
// @Param request body contract.ProxyRequest true "Proxy"
// @Success 200 {object} ProxyResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /proxies/{id} [patch]
func (h *Handler) UpdateProxy(ctx context.Context, c *app.RequestContext) { h.SaveProxy(ctx, c) }

// GetConnection gets connection configuration with secret values omitted.
// @Summary Get connection
// @Tags connections
// @Produce json
// @Security SessionCookie
// @Param id path string true "Connection UUID"
// @Success 200 {object} ConnectionResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /connections/{id} [get]
func (h *Handler) GetConnection(ctx context.Context, c *app.RequestContext) {
	value, err := h.service.Connection(ctx, actor(ctx), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	httpx.Success(c, 200, value)
}
func (h *Handler) connectionModel(ctx context.Context, c *app.RequestContext, value contract.Connection) (connectionForm, error) {
	detail, err := h.service.AccountDetail(ctx, actor(ctx), value.AccountID)
	if err != nil {
		return connectionForm{}, err
	}
	proxies, err := h.service.ListProxies(ctx, actor(ctx))
	if err != nil {
		return connectionForm{}, err
	}
	accounts, err := h.service.ListAccounts(ctx, actor(ctx))
	if err != nil {
		return connectionForm{}, err
	}
	sources := []contract.Connection{}
	for _, account := range accounts {
		data, err := h.service.AccountDetail(ctx, actor(ctx), account.ID)
		if err != nil {
			return connectionForm{}, err
		}
		for _, source := range data.Connections {
			if source.ID != value.ID {
				source.Name = account.Name + " · " + source.Name
				sources = append(sources, source)
			}
		}
	}
	action := "/connections/" + value.ID
	if value.ID == "" {
		action = "/accounts/" + value.AccountID + "/connections"
	}
	return connectionForm{Connection: value, Account: detail.Account, Proxies: proxies, Sources: sources, New: value.ID == "", VaultReady: h.service.VaultReady(), CSRF: auth.CSRF(ctx), Action: action}, nil
}
func (h *Handler) ConnectionForm(ctx context.Context, c *app.RequestContext) {
	value := contract.Connection{AccountID: c.Param("account_id"), AuthType: "access_key", Region: "us-east-1", RouteMode: "direct", Active: true}
	if id := c.Param("id"); id != "" {
		var err error
		value, err = h.service.Connection(ctx, actor(ctx), id)
		if err != nil {
			fail(c, err)
			return
		}
	}
	model, err := h.connectionModel(ctx, c, value)
	if err != nil {
		fail(c, err)
		return
	}
	h.render(ctx, c, 200, "访问连接", "connection-edit", "", "accounts", model)
}
func connectionInput(c *app.RequestContext) (contract.ConnectionRequest, error) {
	var req contract.ConnectionRequest
	if api(c) {
		err := decode(c, &req)
		return req, err
	}
	req = contract.ConnectionRequest{Name: c.PostForm("name"), AuthType: c.PostForm("auth_type"), Region: c.PostForm("region"), RouteMode: c.PostForm("route_mode"),
		SourceConnectionID: optional(c.PostForm("source_connection_id")), RoleARN: c.PostForm("role_arn"), Active: active(c), Version: version(c)}
	if req.RouteMode == "socks5" {
		req.ProxyID = optional(c.PostForm("proxy_id"))
	}
	if req.AuthType != "assume_role" {
		req.SourceConnectionID = nil
		req.RoleARN = ""
	}
	credentials := contract.Credentials{}
	if req.AuthType == "assume_role" {
		credentials.ExternalID = c.PostForm("external_id")
	} else {
		credentials.AccessKeyID = c.PostForm("access_key_id")
		credentials.SecretAccessKey = c.PostForm("secret_access_key")
		if req.AuthType == "temporary" {
			credentials.SessionToken = c.PostForm("session_token")
			if value := c.PostForm("expires_at"); value != "" {
				expires, err := time.ParseInLocation("2006-01-02T15:04", value, clock.Location())
				if err != nil {
					return req, contract.Invalid("请填写正确的北京时间到期时间")
				}
				credentials.ExpiresAt = &expires
			}
		}
	}
	if c.Param("id") == "" || c.PostForm("replace_credentials") == "true" || credentials.AccessKeyID != "" || credentials.SecretAccessKey != "" || credentials.SessionToken != "" || credentials.ExternalID != "" || credentials.ExpiresAt != nil {
		req.Credentials = &credentials
	}
	return req, nil
}

// SaveConnection creates an account connection.
// @Summary Create connection
// @Tags connections
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param account_id path string true "Account UUID"
// @Param request body contract.ConnectionRequest true "Connection"
// @Success 201 {object} ConnectionResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /accounts/{account_id}/connections [post]
func (h *Handler) SaveConnection(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	accountID := c.Param("account_id")
	if id != "" {
		existing, err := h.service.Connection(ctx, actor(ctx), id)
		if err != nil {
			fail(c, err)
			return
		}
		accountID = existing.AccountID
	}
	req, err := connectionInput(c)
	var result contract.Connection
	if err == nil {
		result, err = h.service.SaveConnection(ctx, actor(ctx), accountID, id, req, auth.Meta(ctx))
	}
	if err != nil {
		if api(c) {
			fail(c, err)
			return
		}
		model, modelErr := h.connectionModel(ctx, c, contract.Connection{ID: id, AccountID: accountID, Name: req.Name, AuthType: req.AuthType, Region: req.Region, RouteMode: req.RouteMode, ProxyID: req.ProxyID, SourceConnectionID: req.SourceConnectionID, RoleARN: req.RoleARN, Active: req.Active != nil && *req.Active, Version: req.Version})
		if modelErr != nil {
			fail(c, modelErr)
			return
		}
		code, fault := status(err)
		model.Error = fault.Message
		h.render(ctx, c, code, "访问连接", "connection-edit", "connection-form", "accounts", model)
		return
	}
	if api(c) {
		code := 201
		if id != "" {
			code = 200
		}
		httpx.Success(c, code, result)
		return
	}
	view.Redirect(c, "/accounts/"+accountID)
}

// UpdateConnection updates connection settings and optionally replaces secrets.
// @Summary Update connection
// @Tags connections
// @Accept json
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Connection UUID"
// @Param request body contract.ConnectionRequest true "Connection"
// @Success 200 {object} ConnectionResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /connections/{id} [patch]
func (h *Handler) UpdateConnection(ctx context.Context, c *app.RequestContext) {
	h.SaveConnection(ctx, c)
}

// Verify tests identity through the configured connection.
// @Summary Verify AWS identity
// @Tags connections
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Connection UUID"
// @Success 200 {object} VerificationResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /connections/{id}/verify [post]
func (h *Handler) Verify(ctx context.Context, c *app.RequestContext) {
	value, err := h.service.Verify(ctx, actor(ctx), c.Param("id"), auth.Meta(ctx))
	if err != nil {
		fail(c, err)
		return
	}
	if api(c) {
		httpx.Success(c, 200, value)
		return
	}
	h.render(ctx, c, 200, "身份验证", "connection-verification", "connection-verification", "accounts", verificationView{Result: value, ID: c.Param("id"), CSRF: auth.CSRF(ctx)})
}

// ProbeProxy checks a fixed AWS HTTPS endpoint, without sending AWS credentials.
// @Summary Test SOCKS5 connectivity
// @Tags proxies
// @Produce json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Proxy UUID"
// @Success 200 {object} VerificationResponse
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /proxies/{id}/test [post]
func (h *Handler) ProbeProxy(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.ProbeProxy(ctx, actor(ctx), c.Param("id"), auth.Meta(ctx))
	if err != nil {
		fail(c, err)
		return
	}
	if api(c) {
		httpx.Success(c, 200, result)
		return
	}
	h.render(ctx, c, 200, "代理检测", "connection-verification", "connection-verification", "proxies", verificationView{Result: result})
}
func (h *Handler) remove(ctx context.Context, c *app.RequestContext, kind, redirect string) {
	var req DeleteRequest
	if api(c) {
		if err := decode(c, &req); err != nil {
			fail(c, err)
			return
		}
	} else {
		req.Version = version(c)
	}
	if err := h.service.Delete(ctx, actor(ctx), kind, c.Param("id"), req.Version, auth.Meta(ctx)); err != nil {
		fail(c, err)
		return
	}
	if api(c) {
		c.Status(204)
		return
	}
	view.Redirect(c, redirect)
}

// DeleteAccount removes local metadata after its connections are removed.
// @Summary Remove AWS account from panel
// @Tags accounts
// @Accept json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Account UUID"
// @Param request body DeleteRequest true "Current version"
// @Success 204
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /accounts/{id} [delete]
func (h *Handler) DeleteAccount(ctx context.Context, c *app.RequestContext) {
	h.remove(ctx, c, "aws_account", "/accounts")
}

// DeleteProxy removes an unused proxy profile.
// @Summary Delete proxy profile
// @Tags proxies
// @Accept json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Proxy UUID"
// @Param request body DeleteRequest true "Current version"
// @Success 204
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /proxies/{id} [delete]
func (h *Handler) DeleteProxy(ctx context.Context, c *app.RequestContext) {
	h.remove(ctx, c, "proxy", "/proxies")
}

// DeleteConnection removes stored credentials, not AWS resources.
// @Summary Delete connection and its stored credentials
// @Tags connections
// @Accept json
// @Security SessionCookie && CSRFToken
// @Param id path string true "Connection UUID"
// @Param request body DeleteRequest true "Current version"
// @Success 204
// @Failure 400,401,403,404,409,503 {object} httpx.Envelope
// @Router /connections/{id} [delete]
func (h *Handler) DeleteConnection(ctx context.Context, c *app.RequestContext) {
	h.remove(ctx, c, "connection", "/accounts")
}
