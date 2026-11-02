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
	"github.com/herta-network/aws-panel/internal/module/platform/contract"
	"github.com/herta-network/aws-panel/web"
)

type platformStub struct{ fail bool }

func (s *platformStub) LoadRuntime(context.Context) (contract.Runtime, error) {
	return contract.Runtime{Name: "AWS Panel"}, nil
}
func (s *platformStub) RecordHeartbeat(context.Context) error { return nil }
func (s *platformStub) Status(context.Context) (*contract.SystemStatus, error) {
	if s.fail {
		return nil, errors.New("secret-marker must never reach a response")
	}
	return &contract.SystemStatus{Name: "AWS Panel", Timezone: "Asia/Shanghai", ServerTime: time.Now()}, nil
}

func TestAPIRoutesMatchSwagger(t *testing.T) {
	renderer, err := web.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	h := server.New()
	Register(h, Dependencies{Platform: &platformStub{}, Renderer: renderer, Ready: func(context.Context) bool { return true }})
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
			actual[route.Method+" "+route.Path] = true
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
	renderer, err := web.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	h := server.New()
	Register(h, Dependencies{Platform: &platformStub{fail: true}, Renderer: renderer, Ready: func(context.Context) bool { return false }})
	for _, path := range []string{"/api/v1/system", "/partials/system-status", "/health/ready"} {
		response := ut.PerformRequest(h.Engine, "GET", path, nil)
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
