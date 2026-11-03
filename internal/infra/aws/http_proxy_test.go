package aws

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func httpTunnelFixture(t *testing.T, target, username, password string, observed chan<- string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Error("request bypassed CONNECT")
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("origin authorization leaked to proxy")
		}
		if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)) {
			w.WriteHeader(407)
			return
		}
		if observed != nil {
			observed <- r.Host
		}
		upstream, err := net.Dial("tcp", target)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		conn, reader, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		defer conn.Close()
		defer upstream.Close()
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		done := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, reader); upstream.Close(); close(done) }()
		_, _ = io.Copy(conn, upstream)
		conn.Close()
		<-done
	}))
	t.Cleanup(server.Close)
	return server
}
func httpProxyConfig(t *testing.T, server *httptest.Server, user, password string) Proxy {
	t.Helper()
	host, rawPort, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	return Proxy{Host: host, Port: port, Username: user, Password: password}
}
func TestHTTPProxyConnectTLSAndCredentialIsolation(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("proxy authentication leaked to AWS origin")
		}
		if r.Header.Get("Authorization") != "AWS4-HMAC-SHA256 fixture" {
			t.Error("origin auth missing")
		}
		_, _ = io.WriteString(w, r.Header.Get("X-Fixture-Route"))
	}))
	defer origin.Close()
	if err := origin.Certificate().VerifyHostname("example.com"); err != nil {
		t.Fatal(err)
	}
	observed := make(chan string, 16)
	a := httpTunnelFixture(t, origin.Listener.Addr().String(), "alice", "secret-a", observed)
	b := httpTunnelFixture(t, origin.Listener.Addr().String(), "bob", "secret-b", observed)
	var group sync.WaitGroup
	for index, profile := range []Proxy{httpProxyConfig(t, a, "alice", "secret-a"), httpProxyConfig(t, b, "bob", "secret-b")} {
		client, err := NewHTTPClient(Route{Mode: "http", Proxy: &profile})
		if err != nil {
			t.Fatal(err)
		}
		defer client.CloseIdleConnections()
		roots := x509.NewCertPool()
		roots.AddCert(origin.Certificate())
		client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
		group.Go(func() {
			for range 3 {
				request, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
				request.Header.Set("X-Fixture-Route", strconv.Itoa(index))
				request.Header.Set("Authorization", "AWS4-HMAC-SHA256 fixture")
				response, err := client.Do(request)
				if err != nil {
					t.Error(err)
					return
				}
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if string(body) != strconv.Itoa(index) {
					t.Error("routes crossed")
				}
			}
		})
	}
	group.Wait()
	close(observed)
	count := 0
	for target := range observed {
		if target != "example.com:443" {
			t.Fatal("proxy did not receive target hostname", target)
		}
		count++
	}
	if count < 2 {
		t.Fatal("missing independent tunnels")
	}
}
func TestHTTPProxyAuthFailureNoDirectFallback(t *testing.T) {
	var calls atomic.Int32
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer origin.Close()
	proxy := httpTunnelFixture(t, origin.Listener.Addr().String(), "alice", "correct", nil)
	profile := httpProxyConfig(t, proxy, "alice", "wrong")
	client, err := NewHTTPClient(Route{Mode: "http", Proxy: &profile})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	_, err = client.Get(origin.URL)
	if !errors.Is(err, ErrProxyAuthentication) || calls.Load() != 0 {
		t.Fatal("proxy failure bypassed or lost classification", err, calls.Load())
	}
}
func TestHTTPProxyDialCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
		close(closed)
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	dial := httpProxyDialer(&net.Dialer{}, Proxy{Host: host, Port: port})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if conn, err := dial(ctx, "tcp", "example.com:443"); err == nil {
		conn.Close()
		t.Fatal("stalled CONNECT accepted")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancelled CONNECT connection leaked")
	}
}
