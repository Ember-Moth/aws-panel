package integration

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/herta-network/aws-panel/internal/auth"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAccountDefaultProxyInheritanceAndBindingFence(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	active := true
	a, err := h.accounts.SaveAccount(ctx, h.op.admin, h.accountA.ID, ac.AccountRequest{Name: h.accountA.Name, Partition: h.accountA.Partition, AWSAccountID: h.accountA.AWSAccountID, Active: &active, Version: h.accountA.Version, DefaultRouteMode: "proxy", DefaultProxyID: &h.proxyA.ID}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := h.accounts.SaveConnection(ctx, h.op.admin, a.ID, "", ac.ConnectionRequest{Name: "Inherited route", AuthType: "access_key", Region: "us-east-1", RouteMode: "inherit", Credentials: &ac.Credentials{AccessKeyID: testKeyA, SecretAccessKey: testSecretA}}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := h.accounts.Verify(ctx, h.op.admin, c.ID, auth.RequestMeta{}); err != nil || !v.OK {
		t.Fatal("inherited proxy not used", v.Code, err)
	}
	cfg, err := h.accounts.ConfigFor(ctx, h.op.admin, c.ID, "us-east-1", false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.accounts.SaveProxy(ctx, h.op.admin, "", ac.ProxyRequest{Name: "Alternate account A route", Host: h.proxyA.Host, Port: h.proxyA.Port, Auth: &ac.ProxyAuth{Username: "inventory-a", Password: "inventory-proxy-secret-a"}}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	a, err = h.accounts.SaveAccount(ctx, h.op.admin, a.ID, ac.AccountRequest{Name: a.Name, Partition: a.Partition, AWSAccountID: a.AWSAccountID, Active: &active, Version: a.Version, DefaultRouteMode: "socks5", DefaultProxyID: &p.ID}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.cloud.Identity(ctx, cfg); err == nil {
		t.Fatal("old guarded client survived account proxy change")
	}
	detail, err := h.accounts.AccountDetail(ctx, h.op.admin, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Account.DefaultProxyName != p.Name {
		t.Fatal("proxy name missing")
	}
	found := false
	for _, v := range detail.Connections {
		if v.ID == h.connectionA.ID {
			found = true
			if v.RouteMode != "socks5" || v.ProxyID == nil || *v.ProxyID != h.proxyA.ID {
				t.Fatal("migration of account default rewrote explicit route")
			}
		}
	}
	if !found {
		t.Fatal("old connection missing")
	}
	if err := h.accounts.Delete(ctx, h.op.admin, "proxy", p.ID, p.Version, auth.RequestMeta{}); err == nil {
		t.Fatal("deleted proxy still bound to account")
	}
	if _, err := h.accounts.SaveProxy(ctx, h.op.admin, p.ID, ac.ProxyRequest{Name: p.Name, Host: p.Host, Port: p.Port, Protocol: "http", Version: p.Version, Active: &active}, auth.RequestMeta{}); err == nil {
		t.Fatal("changed protocol of bound proxy")
	}
	if v, err := h.accounts.Verify(ctx, h.op.admin, c.ID, auth.RequestMeta{}); err != nil || !v.OK {
		t.Fatal("could not verify changed inherited route", v.Code, err)
	}
}
func TestAccountHTTPProxyRealSDKAndAuthFailure(t *testing.T) {
	h := newInventory(t)
	ctx := context.Background()
	active := true
	upstream := httptest.NewServer(h.cloud.handler(h.accountA.AWSAccountID))
	defer upstream.Close()
	var tunnels atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "aws-fixture.invalid:80" {
			t.Error("unexpected CONNECT target")
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("AWS auth escaped into CONNECT")
		}
		if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("http-user:http-proxy-secret")) {
			w.WriteHeader(407)
			return
		}
		tunnels.Add(1)
		target, err := net.Dial("tcp", upstream.Listener.Addr().String())
		if err != nil {
			w.WriteHeader(502)
			return
		}
		conn, reader, err := w.(http.Hijacker).Hijack()
		if err != nil {
			target.Close()
			return
		}
		defer conn.Close()
		defer target.Close()
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		done := make(chan struct{})
		go func() { _, _ = io.Copy(target, reader); target.Close(); close(done) }()
		_, _ = io.Copy(conn, target)
		conn.Close()
		<-done
	}))
	defer proxy.Close()
	host, rawPort, _ := net.SplitHostPort(proxy.Listener.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	p, err := h.accounts.SaveProxy(ctx, h.op.admin, "", ac.ProxyRequest{Protocol: "http", Name: "Account HTTP route", Host: host, Port: port, Auth: &ac.ProxyAuth{Username: "http-user", Password: "http-proxy-secret"}}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := h.accounts.SaveAccount(ctx, h.op.admin, h.accountA.ID, ac.AccountRequest{Name: h.accountA.Name, Partition: "aws", AWSAccountID: h.accountA.AWSAccountID, Active: &active, Version: h.accountA.Version, DefaultRouteMode: "proxy", DefaultProxyID: &p.ID}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := h.accounts.SaveConnection(ctx, h.op.admin, a.ID, "", ac.ConnectionRequest{Name: "HTTP inherited", AuthType: "access_key", Region: "us-east-1", RouteMode: "inherit", Credentials: &ac.Credentials{AccessKeyID: testKeyA, SecretAccessKey: testSecretA}}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := h.accounts.Verify(ctx, h.op.admin, c.ID, auth.RequestMeta{}); err != nil || !v.OK || tunnels.Load() == 0 {
		t.Fatal("HTTP STS verification failed", v.Code, err)
	}
	_, err = h.accounts.SaveProxy(ctx, h.op.admin, p.ID, ac.ProxyRequest{Name: p.Name, Host: p.Host, Port: p.Port, Version: p.Version, Active: &active, Auth: &ac.ProxyAuth{Username: "http-user", Password: "wrong-proxy-secret"}}, auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.accounts.Verify(ctx, h.op.admin, c.ID, auth.RequestMeta{})
	if err != nil || v.OK || v.Code != "PROXY_AUTH_FAILED" || strings.Contains(v.Message, "wrong-proxy-secret") {
		t.Fatal("proxy auth error not safely classified", v.Code, err)
	}
	if _, err := h.accounts.SaveConnection(ctx, h.op.admin, a.ID, "", ac.ConnectionRequest{Name: "Protocol mismatch", AuthType: "access_key", Region: "us-east-1", RouteMode: "socks5", ProxyID: &p.ID, Credentials: &ac.Credentials{AccessKeyID: testKeyA, SecretAccessKey: testSecretA}}, auth.RequestMeta{}); !errors.Is(err, ac.ErrInvalid) {
		t.Fatal("protocol mismatch accepted", err)
	}
}
