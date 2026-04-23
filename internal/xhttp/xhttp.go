package xhttp

import (
	"codexswitch/internal/conf"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

type lazyTransport struct{}

var (
	transportOnce sync.Once
	transportRT   http.RoundTripper
	transportErr  error
)

func Transport() http.RoundTripper {
	return lazyTransport{}
}

func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: Transport(),
	}
}

func (lazyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport, err := roundTripper()
	if err != nil {
		return nil, err
	}
	return transport.RoundTrip(request)
}

func roundTripper() (http.RoundTripper, error) {
	transportOnce.Do(func() {
		transportRT, transportErr = buildTransport()
	})
	return transportRT, transportErr
}

func buildTransport() (http.RoundTripper, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default http transport is unavailable")
	}

	transport := base.Clone()
	if !conf.AppConfig.Proxy.Enabled {
		return transport, nil
	}

	host := strings.TrimSpace(conf.AppConfig.Proxy.Host)
	if host == "" || conf.AppConfig.Proxy.Port <= 0 {
		return nil, errors.New("proxy host or port is invalid")
	}

	address := net.JoinHostPort(host, strconv.Itoa(conf.AppConfig.Proxy.Port))
	switch strings.ToLower(strings.TrimSpace(conf.AppConfig.Proxy.Type)) {
	case "http":
		proxyURL := &url.URL{
			Scheme: "http",
			Host:   address,
		}
		if strings.TrimSpace(conf.AppConfig.Proxy.Username) != "" {
			if strings.TrimSpace(conf.AppConfig.Proxy.Password) == "" {
				proxyURL.User = url.User(strings.TrimSpace(conf.AppConfig.Proxy.Username))
			} else {
				proxyURL.User = url.UserPassword(strings.TrimSpace(conf.AppConfig.Proxy.Username), conf.AppConfig.Proxy.Password)
			}
		}
		transport.Proxy = http.ProxyURL(proxyURL)
		return transport, nil
	case "socks", "socks5":
		var auth *proxy.Auth
		if strings.TrimSpace(conf.AppConfig.Proxy.Username) != "" || strings.TrimSpace(conf.AppConfig.Proxy.Password) != "" {
			auth = &proxy.Auth{
				User:     strings.TrimSpace(conf.AppConfig.Proxy.Username),
				Password: conf.AppConfig.Proxy.Password,
			}
		}
		dialer, err := proxy.SOCKS5("tcp", address, auth, &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		})
		if err != nil {
			return nil, err
		}
		transport.Proxy = nil
		if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
			transport.DialContext = contextDialer.DialContext
			return transport, nil
		}
		transport.DialContext = func(ctx context.Context, network string, addr string) (net.Conn, error) {
			type result struct {
				conn net.Conn
				err  error
			}

			done := make(chan result, 1)
			go func() {
				conn, err := dialer.Dial(network, addr)
				done <- result{conn: conn, err: err}
			}()

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case item := <-done:
				return item.conn, item.err
			}
		}
		return transport, nil
	default:
		return nil, fmt.Errorf("unsupported proxy type: %s", conf.AppConfig.Proxy.Type)
	}
}
