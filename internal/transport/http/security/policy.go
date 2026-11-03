package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/auth/token"
	"github.com/Ember-Moth/aws-panel/internal/config"
	"github.com/Ember-Moth/aws-panel/pkg/httpx"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/network"
)

type Policy struct {
	Origin        string
	Host          string
	Secure        bool
	SessionCookie string
	LoginCookie   string
	SessionTTL    time.Duration
	LoginWindow   time.Duration
	trusted       []netip.Prefix
}

func New(cfg config.Config) (*Policy, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	origin, _ := url.Parse(cfg.Auth.PublicURL)
	p := &Policy{Origin: canonicalOrigin(origin), Host: canonicalHost(origin.Scheme, origin.Host), Secure: origin.Scheme == "https",
		SessionCookie: "__Host-aws-panel-session", LoginCookie: "__Host-aws-panel-login",
		SessionTTL: cfg.Auth.SessionTTL, LoginWindow: cfg.Auth.LoginWindow}
	if !p.Secure {
		sum := sha256.Sum256([]byte(p.Origin))
		suffix := hex.EncodeToString(sum[:4])
		p.SessionCookie = "aws_panel_session_dev_" + suffix
		p.LoginCookie = "aws_panel_login_dev_" + suffix
	}
	for _, value := range cfg.HTTP.TrustedProxies {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			address, _ := netip.ParseAddr(value)
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		p.trusted = append(p.trusted, prefix.Masked())
	}
	return p, nil
}

func canonicalHost(scheme, host string) string {
	host = strings.ToLower(host)
	u, err := url.Parse(scheme + "://" + host)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	port := u.Port()
	if port == "" || (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return strings.ToLower(u.Hostname())
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}
func canonicalOrigin(u *url.URL) string { return u.Scheme + "://" + canonicalHost(u.Scheme, u.Host) }
func address(value string) (netip.Addr, bool) {
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	return ip.Unmap().WithZone(""), err == nil
}
func (p *Policy) isTrusted(ip netip.Addr) bool {
	for _, prefix := range p.trusted {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}
func (p *Policy) ClientIP(remote, forwarded string) string {
	ip, ok := address(remote)
	if !ok {
		return ""
	}
	if !p.isTrusted(ip) {
		return ip.String()
	}
	peer := ip
	parts := strings.Split(forwarded, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate, valid := address(parts[i])
		if !valid {
			return peer.String()
		}
		ip = candidate
		if !p.isTrusted(ip) {
			break
		}
	}
	return ip.String()
}
func (p *Policy) SecureRequest(c *app.RequestContext) bool {
	if conn, ok := c.GetConn().(network.ConnTLSer); ok && conn.ConnectionState().HandshakeComplete {
		return true
	}
	peer, ok := address(c.RemoteAddr().String())
	return ok && p.isTrusted(peer) && strings.EqualFold(strings.TrimSpace(string(c.GetHeader("X-Forwarded-Proto"))), "https")
}
func (p *Policy) Canonical(c *app.RequestContext) bool {
	scheme := "http"
	if p.Secure {
		scheme = "https"
	}
	return canonicalHost(scheme, string(c.Request.Host())) == p.Host && (!p.Secure || p.SecureRequest(c))
}
func (p *Policy) SameOrigin(c *app.RequestContext) bool {
	value := string(c.GetHeader("Origin"))
	originHeader := value != ""
	if value == "" {
		value = string(c.GetHeader("Referer"))
	}
	u, err := url.Parse(value)
	return err == nil && u.User == nil && u.Host != "" && (!originHeader || (u.Path == "" && u.RawQuery == "" && u.Fragment == "")) && canonicalOrigin(u) == p.Origin
}
func (p *Policy) SessionToken(c *app.RequestContext) string { return string(c.Cookie(p.SessionCookie)) }
func (p *Policy) LoginCSRF(c *app.RequestContext) (string, error) {
	raw := string(c.Cookie(p.LoginCookie))
	if !token.Valid(raw) {
		var err error
		raw, err = token.New()
		if err != nil {
			return "", err
		}
		p.cookie(c, p.LoginCookie, raw, time.Now().Add(time.Hour), http.SameSiteStrictMode)
	}
	return token.CSRF(raw, "login"), nil
}
func (p *Policy) CheckCSRF(c *app.RequestContext, login bool) bool {
	if !p.Canonical(c) || !p.SameOrigin(c) {
		return false
	}
	name, purpose := p.SessionCookie, "session"
	if login {
		name, purpose = p.LoginCookie, "login"
	}
	provided := string(c.GetHeader("X-CSRF-Token"))
	if provided == "" {
		provided = c.PostForm("_csrf")
	}
	return token.VerifyCSRF(string(c.Cookie(name)), purpose, provided)
}
func (p *Policy) SetSession(c *app.RequestContext, raw string, expires time.Time) {
	p.cookie(c, p.SessionCookie, raw, expires, http.SameSiteLaxMode)
	p.clear(c, p.LoginCookie, http.SameSiteStrictMode)
}
func (p *Policy) ClearSession(c *app.RequestContext) {
	p.clear(c, p.SessionCookie, http.SameSiteLaxMode)
}
func (p *Policy) cookie(c *app.RequestContext, name, value string, expires time.Time, sameSite http.SameSite) {
	cookie := http.Cookie{Name: name, Value: value, Path: "/", Secure: p.Secure, HttpOnly: true, SameSite: sameSite,
		Expires: expires, MaxAge: int(time.Until(expires).Seconds())}
	c.Response.Header.Add("Set-Cookie", cookie.String())
}
func (p *Policy) clear(c *app.RequestContext, name string, sameSite http.SameSite) {
	cookie := http.Cookie{Name: name, Value: "", Path: "/", Secure: p.Secure, HttpOnly: true, SameSite: sameSite,
		Expires: time.Unix(1, 0), MaxAge: -1}
	c.Response.Header.Add("Set-Cookie", cookie.String())
}

func Metadata(p *Policy) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		requestID, _ := c.Get(httpx.RequestIDKey)
		id, _ := requestID.(string)
		ip := p.ClientIP(c.RemoteAddr().String(), string(c.GetHeader("X-Forwarded-For")))
		c.Next(auth.WithMeta(ctx, auth.RequestMeta{RequestID: id, ClientIP: ip}))
	}
}
