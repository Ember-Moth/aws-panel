package aws

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSeparateSOCKSClientsAndRemoteDNS(t *testing.T) {
	targets := make(chan string, 2)
	a := socksServer(t, "proxy-a", "alice", "secret-a", targets)
	b := socksServer(t, "proxy-b", "bob", "secret-b", targets)
	clients := []*http.Client{proxyClient(t, a), proxyClient(t, b)}
	var wait sync.WaitGroup
	for i, client := range clients {
		wait.Add(1)
		go func(index int, client *http.Client) {
			defer wait.Done()
			response, err := client.Get("http://aws-target.invalid/resource")
			if err != nil {
				t.Error(err)
				return
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			want := []string{"proxy-a", "proxy-b"}[index]
			if err != nil || string(body) != want {
				t.Errorf("wrong connection route: %q, %v", body, err)
			}
		}(i, client)
	}
	wait.Wait()
	for range 2 {
		select {
		case host := <-targets:
			if host != "aws-target.invalid:80" {
				t.Errorf("target hostname was not preserved: %s", host)
			}
		case <-time.After(time.Second):
			t.Fatal("proxy did not receive target")
		}
	}
}

func TestProxyFailureDoesNotFallBackToDirect(t *testing.T) {
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(200)
	}))
	defer target.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	host, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	client := proxyClient(t, SOCKS5{Host: host, Port: port})
	if _, err := client.Get(target.URL); err == nil {
		t.Fatal("unreachable proxy unexpectedly succeeded")
	}
	if calls.Load() != 0 {
		t.Fatal("request leaked to a direct connection")
	}
}

func TestDirectIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1")
	client, err := NewHTTPClient(Route{Mode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	request, _ := http.NewRequest("GET", "http://example.invalid", nil)
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil {
		proxy, err := transport.Proxy(request)
		if err != nil || proxy != nil {
			t.Fatal("environment proxy was inherited")
		}
	}
}

func TestInvalidRoutesFailClosed(t *testing.T) {
	for _, route := range []Route{
		{}, {Mode: "socks5"}, {Mode: "socks5", Proxy: &SOCKS5{Host: "localhost", Port: 0}},
		{Mode: "direct", Proxy: &SOCKS5{Host: "localhost", Port: 1080}},
	} {
		if _, err := NewHTTPClient(route); err == nil {
			t.Fatal("invalid route accepted")
		}
	}
}

func TestCancelledProxyHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
	}()
	host, text, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(text)
	client := proxyClient(t, SOCKS5{Host: host, Port: port})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := client.Transport.(*http.Transport).DialContext(ctx, "tcp", "aws-target.invalid:80"); err == nil {
		t.Fatal("stalled proxy succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("request cancellation did not bound proxy wait")
	}
	_ = listener.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled SOCKS handshake left its connection open")
	}
}

func proxyClient(t *testing.T, proxy SOCKS5) *http.Client {
	t.Helper()
	client, err := NewHTTPClient(Route{Mode: "socks5", Proxy: &proxy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func socksServer(t *testing.T, body, username, password string, targets chan<- string) SOCKS5 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				read := func(size int) []byte {
					data := make([]byte, size)
					if _, err := io.ReadFull(conn, data); err != nil {
						return nil
					}
					return data
				}
				greeting := read(2)
				if len(greeting) != 2 || greeting[0] != 5 {
					return
				}
				if read(int(greeting[1])) == nil {
					return
				}
				_, _ = conn.Write([]byte{5, 2})
				auth := read(2)
				if len(auth) != 2 {
					return
				}
				user := read(int(auth[1]))
				length := read(1)
				if len(length) != 1 {
					return
				}
				pass := read(int(length[0]))
				if string(user) != username || string(pass) != password {
					t.Error("SOCKS credentials crossed routes")
					return
				}
				_, _ = conn.Write([]byte{1, 0})
				header := read(4)
				if len(header) != 4 || header[3] != 3 {
					t.Error("expected remote hostname")
					return
				}
				length = read(1)
				if len(length) != 1 {
					return
				}
				host := read(int(length[0]))
				portBytes := read(2)
				if len(portBytes) != 2 {
					return
				}
				targets <- fmt.Sprintf("%s:%d", host, int(portBytes[0])*256+int(portBytes[1]))
				_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
				// Only read to the end of this test's HTTP request headers.
				buffer := make([]byte, 1)
				var headers strings.Builder
				for !strings.HasSuffix(headers.String(), "\r\n\r\n") {
					if _, err := conn.Read(buffer); err != nil {
						return
					}
					headers.WriteByte(buffer[0])
				}
				_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
			}()
		}
	}()
	host, text, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(text)
	return SOCKS5{Host: host, Port: port, Username: username, Password: password}
}
