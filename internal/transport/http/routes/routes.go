package routes

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"mime"
	"path"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/herta-network/aws-panel/internal/module/accounts"
	accountshttp "github.com/herta-network/aws-panel/internal/module/accounts/transport/http"
	"github.com/herta-network/aws-panel/internal/module/identity"
	identitycontract "github.com/herta-network/aws-panel/internal/module/identity/contract"
	identityhttp "github.com/herta-network/aws-panel/internal/module/identity/transport/http"
	"github.com/herta-network/aws-panel/internal/module/inventory"
	inventoryhttp "github.com/herta-network/aws-panel/internal/module/inventory/transport/http"
	"github.com/herta-network/aws-panel/internal/module/lightsail"
	lightsailhttp "github.com/herta-network/aws-panel/internal/module/lightsail/transport/http"
	"github.com/herta-network/aws-panel/internal/module/operations"
	operationshttp "github.com/herta-network/aws-panel/internal/module/operations/transport/http"
	"github.com/herta-network/aws-panel/internal/module/platform"
	platformhttp "github.com/herta-network/aws-panel/internal/module/platform/transport/http"
	"github.com/herta-network/aws-panel/internal/module/regions"
	regionshttp "github.com/herta-network/aws-panel/internal/module/regions/transport/http"
	"github.com/herta-network/aws-panel/internal/transport/http/middleware"
	"github.com/herta-network/aws-panel/internal/transport/http/security"
	"github.com/herta-network/aws-panel/web"
)

type Dependencies struct {
	Platform    platform.Service
	Identity    identity.Service
	Accounts    accounts.Service
	Operations  operations.Service
	Inventory   inventory.Service
	Regions     regions.Service
	Lightsail   lightsail.Service
	Diagnostics bool
	Shutdown    <-chan struct{}
	Policy      *security.Policy
	Renderer    *web.Renderer
	Ready       func(context.Context) bool
}

func Register(h *server.Hertz, deps Dependencies) {
	h.Use(security.Metadata(deps.Policy))
	handlers := identityhttp.New(deps.Identity, deps.Policy, deps.Renderer)
	cloud := accountshttp.New(deps.Accounts, deps.Renderer)
	jobs := operationshttp.New(deps.Operations, deps.Renderer, deps.Identity, deps.Policy, deps.Diagnostics, deps.Shutdown)
	resources := inventoryhttp.New(deps.Inventory, deps.Renderer)
	regionOps := regionshttp.New(deps.Regions, deps.Renderer)
	launch := lightsailhttp.New(deps.Lightsail, deps.Renderer)
	require := func(action string) app.HandlerFunc {
		return middleware.RequireSession(deps.Identity, deps.Policy, action)
	}
	csrf := middleware.CSRF(deps.Policy, false)
	loginCSRF := middleware.CSRF(deps.Policy, true)
	h.GET("/health/live", func(_ context.Context, c *app.RequestContext) {
		c.JSON(200, map[string]string{"status": "alive"})
	})
	h.GET("/health/ready", func(ctx context.Context, c *app.RequestContext) {
		c.Header("Cache-Control", "no-store")
		if !deps.Ready(ctx) {
			c.JSON(503, map[string]string{"status": "not_ready"})
			return
		}
		c.JSON(200, map[string]string{"status": "ready"})
	})
	h.GET("/login", handlers.LoginPage)
	h.POST("/login", loginCSRF, handlers.Login)
	h.POST("/logout", require(""), csrf, handlers.Logout)
	h.GET("/profile", require(""), handlers.Profile)
	h.POST("/profile/password", require(""), csrf, handlers.ChangePassword)
	h.GET("/api/v1/auth/csrf", handlers.CSRFToken)
	h.POST("/api/v1/auth/login", loginCSRF, handlers.Login)
	h.GET("/api/v1/auth/session", require(""), handlers.Session)
	h.POST("/api/v1/auth/logout", require(""), csrf, handlers.Logout)
	h.POST("/api/v1/auth/password", require(""), csrf, handlers.ChangePassword)
	h.GET("/members", require(identitycontract.ActionMembersManage), handlers.ListMembers)
	h.GET("/members/new", require(identitycontract.ActionMembersManage), handlers.NewMemberPage)
	h.GET("/members/:id/edit", require(identitycontract.ActionMembersManage), handlers.EditMemberPage)
	h.POST("/members", require(identitycontract.ActionMembersManage), csrf, handlers.CreateMember)
	h.POST("/members/:id", require(identitycontract.ActionMembersManage), csrf, handlers.UpdateMember)
	h.GET("/api/v1/members", require(identitycontract.ActionMembersManage), handlers.ListMembers)
	h.POST("/api/v1/members", require(identitycontract.ActionMembersManage), csrf, handlers.CreateMember)
	h.PATCH("/api/v1/members/:id", require(identitycontract.ActionMembersManage), csrf, handlers.UpdateMember)
	h.GET("/audit", require(identitycontract.ActionAuditRead), platformhttp.Audit(deps.Platform, deps.Renderer))
	h.GET("/api/v1/audit", require(identitycontract.ActionAuditRead), platformhttp.Audit(deps.Platform, deps.Renderer))
	h.GET("/api/v1/system", require(identitycontract.ActionSystemRead), platformhttp.SystemStatus(deps.Platform))
	h.GET("/", require(identitycontract.ActionSystemRead), platformhttp.Dashboard(deps.Platform, deps.Renderer))
	h.GET("/partials/system-status", require(identitycontract.ActionSystemRead), platformhttp.StatusPartial(deps.Platform, deps.Renderer))
	h.GET("/static/*path", static(web.Static()))
	readAccounts := func() app.HandlerFunc { return require(identitycontract.ActionAccountsRead) }
	manageAccounts := func() app.HandlerFunc { return require(identitycontract.ActionConnectionsManage) }
	h.GET("/accounts", readAccounts(), cloud.ListAccounts)
	h.GET("/accounts/new", manageAccounts(), cloud.AccountForm)
	h.GET("/accounts/:id", readAccounts(), cloud.Detail)
	h.GET("/accounts/:id/edit", manageAccounts(), cloud.AccountForm)
	h.GET("/accounts/:id/access", require(identitycontract.ActionMembersManage), handlers.GrantPage)
	h.POST("/accounts/:id/access", require(identitycontract.ActionMembersManage), csrf, handlers.GrantForm)
	h.POST("/accounts", manageAccounts(), csrf, cloud.SaveAccount)
	h.POST("/accounts/:id", manageAccounts(), csrf, cloud.UpdateAccount)
	h.POST("/accounts/:id/delete", manageAccounts(), csrf, cloud.DeleteAccount)
	h.GET("/proxies", manageAccounts(), cloud.ListProxies)
	h.GET("/proxies/new", manageAccounts(), cloud.ProxyForm)
	h.GET("/proxies/:id/edit", manageAccounts(), cloud.ProxyForm)
	h.POST("/proxies", manageAccounts(), csrf, cloud.SaveProxy)
	h.POST("/proxies/:id", manageAccounts(), csrf, cloud.UpdateProxy)
	h.POST("/proxies/:id/delete", manageAccounts(), csrf, cloud.DeleteProxy)
	h.POST("/proxies/:id/test", manageAccounts(), csrf, cloud.ProbeProxy)
	h.GET("/accounts/:account_id/connections/new", manageAccounts(), cloud.ConnectionForm)
	h.GET("/connections/:id/edit", manageAccounts(), cloud.ConnectionForm)
	h.POST("/accounts/:account_id/connections", manageAccounts(), csrf, cloud.SaveConnection)
	h.POST("/connections/:id", manageAccounts(), csrf, cloud.UpdateConnection)
	h.POST("/connections/:id/verify", manageAccounts(), csrf, cloud.Verify)
	h.POST("/connections/:id/delete", manageAccounts(), csrf, cloud.DeleteConnection)
	h.GET("/api/v1/accounts", readAccounts(), cloud.ListAccounts)
	h.GET("/api/v1/accounts/:id", readAccounts(), cloud.Detail)
	h.POST("/api/v1/accounts", manageAccounts(), csrf, cloud.SaveAccount)
	h.PATCH("/api/v1/accounts/:id", manageAccounts(), csrf, cloud.UpdateAccount)
	h.DELETE("/api/v1/accounts/:id", manageAccounts(), csrf, cloud.DeleteAccount)
	h.GET("/api/v1/proxies", manageAccounts(), cloud.ListProxies)
	h.GET("/api/v1/proxies/:id", manageAccounts(), cloud.GetProxy)
	h.POST("/api/v1/proxies", manageAccounts(), csrf, cloud.SaveProxy)
	h.PATCH("/api/v1/proxies/:id", manageAccounts(), csrf, cloud.UpdateProxy)
	h.DELETE("/api/v1/proxies/:id", manageAccounts(), csrf, cloud.DeleteProxy)
	h.POST("/api/v1/proxies/:id/test", manageAccounts(), csrf, cloud.ProbeProxy)
	h.GET("/api/v1/connections/:id", manageAccounts(), cloud.GetConnection)
	h.POST("/api/v1/accounts/:account_id/connections", manageAccounts(), csrf, cloud.SaveConnection)
	h.PATCH("/api/v1/connections/:id", manageAccounts(), csrf, cloud.UpdateConnection)
	h.DELETE("/api/v1/connections/:id", manageAccounts(), csrf, cloud.DeleteConnection)
	h.POST("/api/v1/connections/:id/verify", manageAccounts(), csrf, cloud.Verify)
	h.GET("/api/v1/members/:id/account-grants", require(identitycontract.ActionMembersManage), handlers.ListGrants)
	h.POST("/api/v1/members/:id/account-grants", require(identitycontract.ActionMembersManage), csrf, handlers.SetGrant)
	readJobs := func() app.HandlerFunc { return require(identitycontract.ActionJobsRead) }
	diagnoseJobs := func() app.HandlerFunc { return require(identitycontract.ActionJobsDiagnose) }
	h.GET("/jobs", readJobs(), jobs.List)
	h.GET("/jobs/diagnostics/new", diagnoseJobs(), jobs.DiagnosticForm)
	h.POST("/jobs/diagnostics/plan", diagnoseJobs(), csrf, jobs.PrepareDiagnostic)
	h.GET("/jobs/plans/:id", readJobs(), jobs.GetPlan)
	h.POST("/jobs", readJobs(), csrf, jobs.Submit)
	h.GET("/jobs/:id", readJobs(), jobs.Detail)
	h.GET("/partials/jobs/:id", readJobs(), jobs.Detail)
	h.POST("/jobs/:id/cancel", readJobs(), csrf, jobs.Cancel)
	h.POST("/jobs/:id/retry", readJobs(), csrf, jobs.Retry)
	h.GET("/api/v1/jobs", readJobs(), jobs.List)
	h.POST("/api/v1/jobs", readJobs(), csrf, jobs.Submit)
	h.GET("/api/v1/jobs/:id", readJobs(), jobs.Detail)
	h.POST("/api/v1/jobs/:id/cancel", readJobs(), csrf, jobs.Cancel)
	h.POST("/api/v1/jobs/:id/retry", readJobs(), csrf, jobs.Retry)
	h.GET("/api/v1/jobs/:id/events", readJobs(), jobs.Events)
	h.GET("/api/v1/jobs/:id/stream", readJobs(), jobs.Stream)
	h.GET("/api/v1/plans/:id", readJobs(), jobs.GetPlan)
	h.POST("/api/v1/operations/diagnostics/plans", diagnoseJobs(), csrf, jobs.PrepareDiagnostic)
	h.GET("/resources", readAccounts(), resources.Resources)
	h.GET("/resources/scopes", readAccounts(), resources.Scopes)
	h.GET("/resources/refresh", readAccounts(), resources.RefreshForm)
	h.POST("/resources/refresh", readAccounts(), csrf, resources.Refresh)
	h.GET("/resources/schedules", readAccounts(), resources.Schedules)
	h.GET("/resources/schedules/new", readAccounts(), resources.ScheduleForm)
	h.GET("/resources/schedules/:id/edit", readAccounts(), resources.ScheduleForm)
	h.POST("/resources/schedules", readAccounts(), csrf, resources.SaveSchedule)
	h.POST("/resources/schedules/:id", readAccounts(), csrf, resources.UpdateSchedule)
	h.POST("/resources/schedules/:id/delete", readAccounts(), csrf, resources.DeleteSchedule)
	h.GET("/resources/:id", readAccounts(), resources.Resource)
	h.GET("/api/v1/resources", readAccounts(), resources.Resources)
	h.GET("/api/v1/resources/options", readAccounts(), resources.Options)
	h.GET("/api/v1/resources/scopes", readAccounts(), resources.Scopes)
	h.POST("/api/v1/resources/refresh", readAccounts(), csrf, resources.Refresh)
	h.GET("/api/v1/resources/:id", readAccounts(), resources.Resource)
	h.GET("/api/v1/inventory/schedules", readAccounts(), resources.Schedules)
	h.POST("/api/v1/inventory/schedules", readAccounts(), csrf, resources.SaveSchedule)
	h.GET("/api/v1/inventory/schedules/:id", readAccounts(), resources.GetSchedule)
	h.PATCH("/api/v1/inventory/schedules/:id", readAccounts(), csrf, resources.UpdateSchedule)
	h.DELETE("/api/v1/inventory/schedules/:id", readAccounts(), csrf, resources.DeleteSchedule)
	operateCloud := func() app.HandlerFunc { return require(identitycontract.ActionCloudOperate) }
	h.GET("/regions/enable", operateCloud(), regionOps.Form)
	h.POST("/regions/plans", operateCloud(), csrf, regionOps.Prepare)
	h.GET("/regions/plans/:id", operateCloud(), regionOps.Plan)
	h.POST("/regions/plans/:id/submit", operateCloud(), csrf, regionOps.Submit)
	h.GET("/api/v1/regions/options", operateCloud(), regionOps.Options)
	h.GET("/api/v1/regions/catalogue", operateCloud(), regionOps.Catalogue)
	h.POST("/api/v1/regions/plans", operateCloud(), csrf, regionOps.Prepare)
	h.GET("/api/v1/regions/plans/:id", operateCloud(), regionOps.Plan)
	h.POST("/api/v1/regions/plans/:id/submit", operateCloud(), csrf, regionOps.Submit)
	h.GET("/lightsail/create", operateCloud(), launch.Form)
	h.POST("/lightsail/plans", operateCloud(), csrf, launch.Prepare)
	h.GET("/lightsail/plans/:id", operateCloud(), launch.Plan)
	h.POST("/lightsail/plans/:id/submit", operateCloud(), csrf, launch.Submit)
	h.GET("/api/v1/lightsail/options", operateCloud(), launch.Options)
	h.GET("/api/v1/lightsail/regions", operateCloud(), launch.Regions)
	h.GET("/api/v1/lightsail/catalogue", operateCloud(), launch.Catalogue)
	h.POST("/api/v1/lightsail/plans", operateCloud(), csrf, launch.Prepare)
	h.GET("/api/v1/lightsail/plans/:id", operateCloud(), launch.Plan)
	h.POST("/api/v1/lightsail/plans/:id/submit", operateCloud(), csrf, launch.Submit)

}

func static(files fs.FS) app.HandlerFunc {
	return func(_ context.Context, c *app.RequestContext) {
		name := strings.TrimPrefix(c.Param("path"), "/")
		if !fs.ValidPath(name) {
			c.Status(404)
			return
		}
		body, err := fs.ReadFile(files, name)
		if err != nil {
			c.Status(404)
			return
		}
		etag := fmt.Sprintf("\"%x\"", sha256.Sum256(body))
		c.Header("ETag", etag)
		c.Header("Cache-Control", "no-cache")
		if string(c.Request.Header.Peek("If-None-Match")) == etag {
			c.Status(304)
			return
		}
		contentType := mime.TypeByExtension(path.Ext(name))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		c.Data(200, contentType, body)
	}
}
