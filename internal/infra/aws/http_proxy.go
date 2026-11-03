package aws

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Each transport tunnels only through its selected proxy. Target hostnames are
// sent in CONNECT, so target DNS and AWS TLS remain on the intended route.
func httpProxyDialer(dialer *net.Dialer, profile Proxy) func(context.Context, string, string) (net.Conn, error) {
	return func(parent context.Context, network, address string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(parent, 10*time.Second)
		defer cancel()
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(profile.Host, strconv.Itoa(profile.Port)))
		if err != nil {
			return nil, err
		}
		accepted := false
		defer func() {
			if !accepted {
				conn.Close()
			}
		}()
		done := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { conn.Close(); close(done) })
		defer func() {
			if !stop() {
				<-done
			}
		}()
		deadline, _ := ctx.Deadline()
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
		request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Host: address}, Host: address, Header: make(http.Header)}
		if profile.Username != "" {
			request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(profile.Username+":"+profile.Password)))
		}
		if err := request.Write(conn); err != nil {
			return nil, err
		}
		reader := bufio.NewReader(conn)
		var headers bytes.Buffer
		for {
			line, readErr := reader.ReadSlice('\n')
			headers.Write(line)
			if headers.Len() > 64<<10 {
				return nil, errors.New("HTTP proxy response headers too large")
			}
			if readErr != nil && readErr != bufio.ErrBufferFull {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, readErr
			}
			if readErr == nil && (bytes.Equal(line, []byte("\r\n")) || bytes.Equal(line, []byte("\n"))) {
				break
			}
		}
		response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(headers.Bytes())), request)
		if err != nil {
			return nil, errors.New("invalid HTTP proxy response")
		}
		if response.StatusCode == http.StatusProxyAuthRequired {
			return nil, ErrProxyAuthentication
		}
		if response.StatusCode != http.StatusOK {
			return nil, errors.New("HTTP proxy CONNECT rejected")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := conn.SetDeadline(time.Time{}); err != nil {
			return nil, err
		}
		accepted = true
		return &proxyBufferedConn{Conn: conn, reader: reader}, nil
	}
}

type proxyBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *proxyBufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
