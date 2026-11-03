package security

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/herta-network/aws-panel/internal/auth/token"
	"github.com/herta-network/aws-panel/internal/config"
)

func policy(t *testing.T) *Policy {
	t.Helper()
	result, err := New(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func request() *app.RequestContext {
	c := app.NewContext(0)
	c.Request.SetRequestURI("http://127.0.0.1:8080/login")
	c.Request.Header.SetMethod("POST")
	return c
}
func TestOriginAndCSRF(t *testing.T) {
	p := policy(t)
	raw, _ := token.New()
	c := request()
	c.Request.Header.Set("Cookie", p.SessionCookie+"="+raw)
	c.Request.Header.Set("Origin", p.Origin)
	c.Request.Header.Set("X-CSRF-Token", token.CSRF(raw, "session"))
	if !p.CheckCSRF(c, false) {
		t.Fatal("valid CSRF rejected")
	}
	for _, origin := range []string{"http://127.0.0.1:8080.evil", "http://localhost:8080", "null", "http://127.0.0.1:8080/path"} {
		c.Request.Header.Set("Origin", origin)
		if p.CheckCSRF(c, false) {
			t.Fatalf("foreign or malformed origin accepted: %s", origin)
		}
	}
	c.Request.Header.Del("Origin")
	if p.CheckCSRF(c, false) {
		t.Fatal("missing origin accepted")
	}
	c.Request.Header.Set("Referer", p.Origin+"/profile")
	if !p.CheckCSRF(c, false) {
		t.Fatal("same-origin Referer fallback rejected")
	}
	c.Request.Header.Set("X-CSRF-Token", "wrong")
	if p.CheckCSRF(c, false) {
		t.Fatal("wrong CSRF accepted")
	}
	c.Request.Header.Set("X-CSRF-Token", token.CSRF(raw, "login"))
	if p.CheckCSRF(c, false) {
		t.Fatal("pre-login proof reused for authenticated write")
	}
}
func TestProxyHeadersAreScopedToTrustedPeers(t *testing.T) {
	p := policy(t)
	if got := p.ClientIP("127.0.0.1:1234", "8.8.8.8"); got != "127.0.0.1" {
		t.Fatal("untrusted forwarding header used")
	}
	cfg := config.Defaults()
	cfg.HTTP.TrustedProxies = []string{"10.0.0.0/24"}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ClientIP("10.0.0.1:1234", "8.8.8.8, 10.0.0.2"); got != "8.8.8.8" {
		t.Fatal("trusted chain incorrectly resolved")
	}
	if got := p.ClientIP("10.0.0.1:1234", "invalid, 10.0.0.2"); got != "10.0.0.1" {
		t.Fatal("malformed chain did not fall back to peer")
	}
	cfg.Auth.PublicURL = "https://panel.example.com"
	cfg.HTTP.TrustedProxies = nil
	p, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := request()
	c.Request.SetRequestURI("https://panel.example.com/login")
	c.Request.Header.Set("X-Forwarded-Proto", "https")
	if p.Canonical(c) {
		t.Fatal("spoofed HTTPS header accepted from untrusted peer")
	}
}
func TestCookieAttributes(t *testing.T) {
	cfg := config.Defaults()
	cfg.Auth.PublicURL = "https://panel.example.com"
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := request()
	raw, _ := token.New()
	p.SetSession(c, raw, time.Now().Add(time.Hour))
	headers := http.Header{}
	c.Response.Header.VisitAll(func(key, value []byte) { headers.Add(string(key), string(value)) })
	cookies := (&http.Response{Header: headers}).Cookies()
	var found bool
	for _, cookie := range cookies {
		if cookie.Name != p.SessionCookie {
			continue
		}
		found = true
		if !strings.HasPrefix(cookie.Name, "__Host-") || !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode {
			t.Fatal("session cookie protections missing")
		}
	}
	if !found {
		t.Fatal("session cookie missing")
	}
}
