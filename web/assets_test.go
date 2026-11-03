package web

import (
	dc "github.com/Ember-Moth/aws-panel/internal/module/dashboard/contract"
	"strings"
	"testing"
)

func TestTemplatesEscapeDataAndRenderPartial(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	view := SystemView{Version: "<script>alert(1)</script>", ServerTime: "2026-11-02 12:00:00"}
	for _, name := range []string{"dashboard", "system-status"} {
		var model any = view
		if name == "dashboard" {
			model = struct {
				Overview              dc.Overview
				System                SystemView
				CSRF, Key, PartialURL string
			}{System: view}
		}
		body, err := renderer.Render(name, model)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), view.Version) {
			t.Fatal("unescaped version value")
		}
		if !strings.Contains(string(body), "&lt;script&gt;") {
			t.Fatal("escaped value absent")
		}
	}
	partial, _ := renderer.Render("system-status", view)
	if strings.Contains(string(partial), "<html") {
		t.Fatal("partial contains an entire document")
	}
	if !strings.Contains(string(partial), "Asia/Shanghai") {
		t.Fatal("timezone missing")
	}
}

func TestLayoutEscapesIdentityAndCSRFFields(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	attack := `"><script>alert(1)</script>`
	body, err := renderer.Page("dashboard", Layout{Title: attack, CSRF: attack, User: &UserView{DisplayName: attack, Role: "viewer"}}, struct {
		Overview              dc.Overview
		System                SystemView
		CSRF, Key, PartialURL string
	}{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "<script>alert(1)</script>") {
		t.Fatal("layout rendered unescaped identity data")
	}
	if !strings.Contains(string(body), "&lt;script&gt;") {
		t.Fatal("layout identity data absent")
	}
}
