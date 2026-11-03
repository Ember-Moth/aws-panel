package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/herta-network/aws-panel/internal/module/accounts"
)

const (
	testKeyA       = "AKIAFIXTUREPRIMARY123"
	testSecretA    = "fixture-primary-secret-never-use-in-aws-123"
	testKeyRole    = "ASIAFIXTUREROLE123456"
	testSecretRole = "fixture-role-secret-never-use-in-aws-12345"
	testExternalID = "fixture-external-id-sensitive"
)

type stsFixture struct {
	t           *testing.T
	assumptions atomic.Int64
	mu          sync.Mutex
	routes      map[string]int
}

func newSTSServer(t *testing.T) *stsFixture {
	return &stsFixture{t: t, routes: map[string]int{}}
}

func (f *stsFixture) Identity(ctx context.Context, cfg aws.Config) (accounts.IdentityResult, error) {
	cfg.BaseEndpoint = aws.String("http://sts-fixture.invalid")
	return accounts.NewSTS().Identity(ctx, cfg)
}
func (f *stsFixture) Assume(ctx context.Context, cfg aws.Config, role, name, external string) (aws.Credentials, error) {
	cfg.BaseEndpoint = aws.String("http://sts-fixture.invalid")
	return accounts.NewSTS().Assume(ctx, cfg, role, name, external)
}
func (f *stsFixture) handler(route string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 32768))
		if err != nil {
			f.t.Error(err)
			w.WriteHeader(400)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err := r.ParseForm(); err != nil {
			f.t.Error(err)
			w.WriteHeader(400)
			return
		}
		authHeader := r.Header.Get("Authorization")
		key, secret, account, token := testKeyA, testSecretA, "111111111111", ""
		if strings.Contains(authHeader, "Credential="+testKeyRole+"/") {
			key, secret, account, token = testKeyRole, testSecretRole, "222222222222", "fixture-role-token"
		}
		clone := r.Clone(r.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = r.Host
		clone.RequestURI = ""
		clone.Header.Del("Authorization")
		// net/http adds headers such as Accept-Encoding after AWS signing.
		// Rebuild only the original canonical signed header set.
		_, signed, ok := strings.Cut(authHeader, "SignedHeaders=")
		if !ok {
			f.t.Error("STS request is unsigned")
			w.WriteHeader(403)
			return
		}
		signed, _, _ = strings.Cut(signed, ",")
		for header := range clone.Header {
			if !strings.Contains(";"+signed+";", ";"+strings.ToLower(header)+";") {
				clone.Header.Del(header)
			}
		}
		signedAt, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
		if err != nil {
			f.t.Error("missing signing date")
			w.WriteHeader(400)
			return
		}
		digest := sha256.Sum256(body)
		err = v4.NewSigner().SignHTTP(r.Context(), aws.Credentials{AccessKeyID: key, SecretAccessKey: secret, SessionToken: token}, clone, hex.EncodeToString(digest[:]), "sts", "us-east-1", signedAt)
		if err != nil || clone.Header.Get("Authorization") != authHeader || r.Header.Get("X-Amz-Security-Token") != token {
			f.t.Errorf("STS fixture signature mismatch: received %s, rebuilt %s", authHeader, clone.Header.Get("Authorization"))
			w.WriteHeader(403)
			return
		}
		action := r.Form.Get("Action")
		if route == "proxy-a" && (key != testKeyA || action != "GetCallerIdentity") || route == "proxy-b" && ((action == "AssumeRole" && key != testKeyA) || (action == "GetCallerIdentity" && key != testKeyRole)) {
			f.t.Error("STS operation crossed proxy route")
			w.WriteHeader(403)
			return
		}
		f.mu.Lock()
		f.routes[route+"/"+action]++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		switch action {
		case "GetCallerIdentity":
			fmt.Fprintf(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Account>%s</Account><Arn>arn:aws:iam::%s:user/Fixture</Arn><UserId>fixture</UserId></GetCallerIdentityResult><ResponseMetadata><RequestId>fixture-identity</RequestId></ResponseMetadata></GetCallerIdentityResponse>`, account, account)
		case "AssumeRole":
			if r.Form.Get("ExternalId") != testExternalID || r.Form.Get("RoleArn") != "arn:aws:iam::222222222222:role/Test" {
				f.t.Error("role input changed")
				w.WriteHeader(400)
				return
			}
			f.assumptions.Add(1)
			fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>fixture-role-token</SessionToken><Expiration>%s</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::222222222222:assumed-role/Test/Fixture</Arn><AssumedRoleId>fixture</AssumedRoleId></AssumedRoleUser></AssumeRoleResult><ResponseMetadata><RequestId>fixture-assume</RequestId></ResponseMetadata></AssumeRoleResponse>`, testKeyRole, testSecretRole, time.Now().Add(61*time.Second).UTC().Format(time.RFC3339Nano))
		default:
			f.t.Error("unexpected STS operation")
			w.WriteHeader(400)
		}
	})
}
func (f *stsFixture) assertRoutes(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, route := range []string{"proxy-a/GetCallerIdentity", "proxy-b/AssumeRole", "proxy-b/GetCallerIdentity"} {
		if f.routes[route] == 0 {
			t.Errorf("missing STS route %s", route)
		}
	}
}

type proxyFixture struct {
	host string
	port int
	stop func()
}

func newSOCKSProxy(t *testing.T, route, user, password string, fixture *stsFixture) proxyFixture {
	t.Helper()
	target := httptest.NewServer(fixture.handler(route))
	t.Cleanup(target.Close)
	return newSOCKSForwarder(t, user, password, "sts-fixture.invalid", strings.TrimPrefix(target.URL, "http://"))
}
func newSOCKSForwarder(t *testing.T, user, password, hostname, address string) proxyFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	t.Cleanup(func() { listener.Close(); workers.Wait() })
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(30 * time.Second))
				read := func(n int) []byte {
					value := make([]byte, n)
					if _, err := io.ReadFull(conn, value); err != nil {
						return nil
					}
					return value
				}
				greeting := read(2)
				if len(greeting) != 2 || greeting[0] != 5 {
					return
				}
				if read(int(greeting[1])) == nil {
					return
				}
				conn.Write([]byte{5, 2})
				auth := read(2)
				if len(auth) != 2 {
					return
				}
				username := read(int(auth[1]))
				size := read(1)
				if len(size) != 1 {
					return
				}
				pass := read(int(size[0]))
				if string(username) != user || string(pass) != password {
					conn.Write([]byte{1, 1})
					return
				}
				conn.Write([]byte{1, 0})
				header := read(4)
				if len(header) != 4 || header[3] != 3 {
					return
				}
				size = read(1)
				if len(size) != 1 {
					return
				}
				host := read(int(size[0]))
				port := read(2)
				if string(host) != hostname || len(port) != 2 || int(port[0])*256+int(port[1]) != 80 {
					t.Error("proxy did not preserve the configured test hostname")
					return
				}
				upstream, err := net.DialTimeout("tcp", address, time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
				done := make(chan struct{})
				go func() { io.Copy(upstream, conn); upstream.(*net.TCPConn).CloseWrite(); close(done) }()
				io.Copy(conn, upstream)
				conn.Close()
				<-done
			}()
		}
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	return proxyFixture{host: host, port: port, stop: func() { listener.Close() }}
}
