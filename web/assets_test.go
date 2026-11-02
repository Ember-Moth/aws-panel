package web

import (
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
		body, err := renderer.Render(name, view)
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
