package http

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/module/identity/contract"
	"github.com/Ember-Moth/aws-panel/internal/transport/http/view"
	"github.com/Ember-Moth/aws-panel/pkg/httpx"
	"github.com/cloudwego/hertz/pkg/app"
	"strconv"
)

type grantsView struct {
	Page *contract.GrantPage
	CSRF string
}

type GrantsResponse struct {
	Data      []contract.AccountGrant `json:"data"`
	RequestID string                  `json:"request_id"`
}

// ListGrants lists account scopes for a member.
// @Summary List member account grants
// @Tags members
// @Produce json
// @Security SessionCookie
// @Param id path int true "Member ID"
// @Success 200 {object} GrantsResponse
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /members/{id}/account-grants [get]
func (h *Handler) ListGrants(ctx context.Context, c *app.RequestContext) {
	id, err := memberID(c)
	if err != nil {
		fail(c, err)
		return
	}
	current, _ := auth.Current(ctx)
	values, err := h.service.ListAccountGrants(ctx, current.ID, id)
	if err != nil {
		fail(c, err)
		return
	}
	httpx.Success(c, 200, values)
}

// SetGrant assigns or revokes account scope.
// @Summary Set member account grant
// @Tags members
// @Accept json
// @Security SessionCookie && CSRFToken
// @Param id path int true "Member ID"
// @Param request body contract.GrantRequest true "read, operate or none"
// @Success 204
// @Failure 400,401,403,404,503 {object} httpx.Envelope
// @Router /members/{id}/account-grants [post]
func (h *Handler) SetGrant(ctx context.Context, c *app.RequestContext) {
	id, err := memberID(c)
	if err != nil {
		fail(c, err)
		return
	}
	var req contract.GrantRequest
	if err := decode(c, &req); err != nil {
		fail(c, err)
		return
	}
	current, _ := auth.Current(ctx)
	if err := h.service.SetAccountGrant(ctx, current.ID, id, req, auth.Meta(ctx)); err != nil {
		fail(c, err)
		return
	}
	c.Status(204)
}
func (h *Handler) GrantPage(ctx context.Context, c *app.RequestContext) {
	current, _ := auth.Current(ctx)
	result, err := h.service.AccountGrantPage(ctx, current.ID, c.Param("id"), page(c))
	if err != nil {
		fail(c, err)
		return
	}
	body, err := h.renderer.Page("account-grants", view.Layout(ctx, "账号访问权限", "accounts"), grantsView{Page: result, CSRF: auth.CSRF(ctx)})
	view.HTML(c, 200, body, err)
}
func (h *Handler) GrantForm(ctx context.Context, c *app.RequestContext) {
	id, err := strconv.ParseInt(c.PostForm("member_id"), 10, 64)
	if err != nil || id < 1 {
		fail(c, contract.ErrValidation)
		return
	}
	current, _ := auth.Current(ctx)
	err = h.service.SetAccountGrant(ctx, current.ID, id, contract.GrantRequest{AccountID: c.Param("id"), Access: c.PostForm("access")}, auth.Meta(ctx))
	if err != nil {
		fail(c, err)
		return
	}
	if string(c.GetHeader("HX-Request")) == "true" {
		c.Data(200, "text/html; charset=utf-8", []byte("<span class=\"text-success small\">已保存</span>"))
		return
	}
	view.Redirect(c, "/accounts/"+c.Param("id")+"/access")
}
