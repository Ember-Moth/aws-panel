// Package aws builds explicit, connection-scoped AWS transports. It never
// changes process-wide proxy settings or falls back to environment credentials.
package aws

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	socksproxy "golang.org/x/net/proxy"
)

type Route struct {
	Mode  string
	Proxy *Proxy
}

type Proxy struct {
	Host     string
	Port     int
	Username string
	Password string
}

// SOCKS5 remains an alias for callers using the original proxy transport.
type SOCKS5 = Proxy

var ErrProxyAuthentication = errors.New("proxy authentication failed")

// NewHTTPClient always owns its Transport and connection pool. SOCKS target
// names are resolved by the proxy; no ProxyFromEnvironment is consulted.
func NewHTTPClient(route Route) (*http.Client, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		MaxConnsPerHost:       8,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	switch route.Mode {
	case "direct":
		if route.Proxy != nil {
			return nil, errors.New("direct route must not include a proxy")
		}
	case "socks5":
		proxy := route.Proxy
		if proxy == nil || proxy.Host == "" || proxy.Port < 1 || proxy.Port > 65535 {
			return nil, errors.New("socks5 route requires a valid proxy address")
		}
		if (proxy.Username == "") != (proxy.Password == "") || len(proxy.Username) > 255 || len(proxy.Password) > 255 {
			return nil, errors.New("invalid SOCKS5 authentication configuration")
		}
		var auth *socksproxy.Auth
		if proxy.Username != "" {
			auth = &socksproxy.Auth{User: proxy.Username, Password: proxy.Password}
		}
		proxyDialer, err := socksproxy.SOCKS5("tcp", net.JoinHostPort(proxy.Host, strconv.Itoa(proxy.Port)), auth, dialer)
		if err != nil {
			return nil, errors.New("cannot configure SOCKS5 transport")
		}
		contextDialer, ok := proxyDialer.(socksproxy.ContextDialer)
		if !ok {
			return nil, errors.New("SOCKS5 transport must support cancellation")
		}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			// net/http can detach its connection dial from the request's
			// deadline for pooling. Bound the SOCKS handshake itself too.
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return contextDialer.DialContext(ctx, network, address)
		}
	case "http":
		profile := route.Proxy
		if profile == nil || profile.Host == "" || profile.Port < 1 || profile.Port > 65535 {
			return nil, errors.New("HTTP route requires a valid proxy address")
		}
		if (profile.Username == "") != (profile.Password == "") || len(profile.Username) > 255 || len(profile.Password) > 255 {
			return nil, errors.New("invalid HTTP proxy authentication configuration")
		}
		transport.DialContext = httpProxyDialer(dialer, *profile)

	default:
		return nil, errors.New("explicit direct, socks5 or http route is required")
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		// AWS API redirects should be handled by service endpoint resolution,
		// not by forwarding signed requests to arbitrary HTTP destinations.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func NewConfig(region string, provider awssdk.CredentialsProvider, client *http.Client) (awssdk.Config, error) {
	if region == "" || provider == nil || client == nil {
		return awssdk.Config{}, errors.New("AWS region, explicit credentials and HTTP client are required")
	}
	cached := provider
	if _, ok := provider.(*awssdk.CredentialsCache); !ok {
		cached = awssdk.NewCredentialsCache(provider)
	}
	return awssdk.Config{
		Region:      region,
		Credentials: cached,
		HTTPClient:  client,
		Retryer: func() awssdk.Retryer {
			return retry.NewStandard(func(options *retry.StandardOptions) { options.MaxAttempts = 3 })
		},
	}, nil
}
