package routes

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/herta-network/aws-panel/internal/auth/token"
	"github.com/herta-network/aws-panel/internal/config"
	"github.com/herta-network/aws-panel/internal/module/identity"
	identitycontract "github.com/herta-network/aws-panel/internal/module/identity/contract"
	"github.com/herta-network/aws-panel/internal/module/platform/contract"
	"github.com/herta-network/aws-panel/internal/transport/http/security"
	"github.com/herta-network/aws-panel/web"
)

type platformStub struct{ fail bool }

func (s *platformStub) LoadRuntime(context.Context) (contract.Runtime, error) {
	return contract.Runtime{Name: "AWS Panel"}, nil
}
func (s *platformStub) RecordHeartbeat(context.Context) error { return nil }
func (s *platformStub) ListAudit(context.Context, int64) (*contract.AuditList, error) {
	return &contract.AuditList{}, nil
}
func (s *platformStub) Status(context.Context) (*contract.SystemStatus, error) {
	if s.fail {
		return nil, errors.New("secret-marker must never reach a response")
	}
	return &contract.SystemStatus{Name: "AWS Panel", Timezone: "Asia/Shanghai", ServerTime: time.Now()}, nil
}

type identityStub struct {
	identity.Service
	raw, role string
}

func (s *identityStub) Authenticate(_ context.Context, raw string) (identitycontract.Session, error) {
	if raw != s.raw {
		return identitycontract.Session{}, identitycontract.ErrUnauthenticated
	}
	return identitycontract.Session{Member: identitycontract.Member{ID: 1, Username: "testadmin", DisplayName: "测试成员", Role: s.role, Active: true}, ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func testDependencies(t *testing.T, fail bool) (Dependencies, *identityStub) {
	t.Helper()
	renderer, err := web.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := security.New(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := token.New()
	authenticator := &identityStub{raw: raw, role: identitycontract.RoleAdmin}
	return Dependencies{Platform: &platformStub{fail: fail}, Identity: authenticator, Policy: policy, Renderer: renderer, Ready: func(context.Context) bool { return !fail }}, authenticator
}
func authenticatedHeaders(deps Dependencies, stub *identityStub) []ut.Header {
	return []ut.Header{
		{Key: "Cookie", Value: deps.Policy.SessionCookie + "=" + stub.raw},
		{Key: "Origin", Value: deps.Policy.Origin},
		{Key: "X-CSRF-Token", Value: token.CSRF(stub.raw, "session")},
	}
}

func TestAPIRoutesMatchSwagger(t *testing.T) {
	deps, _ := testDependencies(t, false)
	h := server.New()
	Register(h, deps)
	var specification struct {
		BasePath string                                `json:"basePath"`
		Paths    map[string]map[string]json.RawMessage `json:"paths"`
	}
	body, err := os.ReadFile("../../../../api/generated/swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &specification); err != nil {
		t.Fatal(err)
	}
	actual := make(map[string]bool)
	for _, route := range h.Routes() {
		if strings.HasPrefix(route.Path, "/api/") {
			parts := strings.Split(route.Path, "/")
			for i, part := range parts {
				if strings.HasPrefix(part, ":") {
					parts[i] = "{" + part[1:] + "}"
				}
			}
			actual[route.Method+" "+strings.Join(parts, "/")] = true
		}
	}
	for path, methods := range specification.Paths {
		for method := range methods {
			key := strings.ToUpper(method) + " " + specification.BasePath + path
			if !actual[key] {
				t.Errorf("documented route is missing: %s", key)
			}
			delete(actual, key)
		}
	}
	for key := range actual {
		t.Errorf("API route lacks a contract: %s", key)
	}
}

func TestFailureResponsesAndReadiness(t *testing.T) {
	deps, stub := testDependencies(t, true)
	h := server.New()
	Register(h, deps)
	for _, path := range []string{"/api/v1/system", "/partials/system-status", "/health/ready"} {
		response := ut.PerformRequest(h.Engine, "GET", deps.Policy.Origin+path, nil, authenticatedHeaders(deps, stub)...)
		if response.Code != 503 {
			t.Errorf("%s: got status %d", path, response.Code)
		}
		if strings.Contains(response.Body.String(), "secret-marker") {
			t.Fatal("internal error leaked")
		}
	}
	if response := ut.PerformRequest(h.Engine, "GET", "/health/live", nil); response.Code != 200 {
		t.Fatal("liveness depends on dependencies")
	}
}

func TestPrivateAPIRoutesRequireAuthentication(t *testing.T) {
	deps, _ := testDependencies(t, false)
	h := server.New()
	Register(h, deps)
	for _, route := range h.Routes() {
		if !strings.HasPrefix(route.Path, "/api/") || route.Path == "/api/v1/auth/csrf" || route.Path == "/api/v1/auth/login" {
			continue
		}
		path := strings.ReplaceAll(route.Path, ":id", "1")
		response := ut.PerformRequest(h.Engine, route.Method, deps.Policy.Origin+path, nil, ut.Header{Key: "HX-Request", Value: "true"}, ut.Header{Key: "X-Role", Value: "admin"})
		if response.Code != 401 {
			t.Errorf("%s %s bypassed session gate: %d", route.Method, path, response.Code)
		}
		if response.Header().Get("HX-Redirect") != "/login" {
			t.Error("expired htmx session must redirect the whole page")
		}
	}
}
func TestViewerCannotManageMembersOrReadAudit(t *testing.T) {
	deps, stub := testDependencies(t, false)
	stub.role = identitycontract.RoleViewer
	h := server.New()
	Register(h, deps)
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/members"}, {"POST", "/api/v1/members"}, {"PATCH", "/api/v1/members/1"}, {"GET", "/api/v1/audit"}} {
		response := ut.PerformRequest(h.Engine, route.method, deps.Policy.Origin+route.path, nil, authenticatedHeaders(deps, stub)...)
		if response.Code != 403 {
			t.Errorf("%s: viewer permission was not denied: %d", route.path, response.Code)
		}
	}
	response := ut.PerformRequest(h.Engine, "GET", deps.Policy.Origin+"/", nil, authenticatedHeaders(deps, stub)...)
	if response.Code != 200 || strings.Contains(response.Body.String(), `href="/members"`) {
		t.Fatal("viewer dashboard must omit member administration")
	}
}
