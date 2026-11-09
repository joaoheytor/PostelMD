package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

var (
	// ErrDenied: the destination address isn't allowed for this client.
	ErrDenied = errors.New("egress: destination not allowed")
	// ErrScheme: the request (or a redirect) isn't HTTPS and the client doesn't allow HTTP.
	ErrScheme = errors.New("egress: only https is allowed")
	// ErrTooManyRedirects: more than MaxRedirects redirects.
	ErrTooManyRedirects = errors.New("egress: too many redirects")
	// ErrTooLarge: the response body exceeded the client's cap.
	ErrTooLarge = errors.New("egress: response too large")
)

// MaxRedirects is the most redirects a request follows (architecture §11). Every hop is
// dialled through the same policy.
const MaxRedirects = 3

// Options configure one client. Each purpose (git remote, OIDC, registry, an approved embed)
// gets its own client, so an exception approved for one purpose never applies to another.
type Options struct {
	// Exceptions are exact "host:port" destinations the admin approved for this purpose.
	// They may resolve to private addresses (a LAN Grafana, a tailnet host); there are no
	// CIDR-wide exceptions.
	Exceptions []string
	// AllowHTTP permits plain http. Off by default: credentials never travel in clear text.
	AllowHTTP bool
	// Timeout bounds a whole request, including reading the body. Default 30 s.
	Timeout time.Duration
	// MaxResponseBytes caps a response body. Default 10 MiB.
	MaxResponseBytes int64
	// RootCAs replaces the system roots (a private CA behind an exception, or tests).
	RootCAs *x509.CertPool
}

// New returns an HTTP client that enforces the egress policy. It never reads proxy
// settings from the environment: a proxy would connect on our behalf, out of the policy's
// sight.
func New(o Options) *http.Client {
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	if o.MaxResponseBytes == 0 {
		o.MaxResponseBytes = 10 << 20
	}
	exceptions := map[string]bool{}
	for _, e := range o.Exceptions {
		exceptions[strings.ToLower(e)] = true
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer(exceptions),
		TLSClientConfig:        &tls.Config{RootCAs: o.RootCAs, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  15 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		IdleConnTimeout:        90 * time.Second,
	}
	return &http.Client{
		Transport: &guard{next: transport, allowHTTP: o.AllowHTTP, max: o.MaxResponseBytes},
		Timeout:   o.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return ErrTooManyRedirects
			}
			return nil // the scheme and the address are checked again for this hop
		},
	}
}

// dialer checks the address the socket actually connects to, after DNS resolution, so a
// name that resolves to a private address (DNS rebinding included) is refused. The
// connection is made to the address that was checked.
func dialer(exceptions map[string]bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		excepted := exceptions[strings.ToLower(addr)]
		d := net.Dialer{
			Timeout: 10 * time.Second,
			Control: func(_, address string, _ syscall.RawConn) error {
				ap, err := netip.ParseAddrPort(address)
				if err != nil {
					return fmt.Errorf("%w: %s", ErrDenied, address)
				}
				if Public(ap.Addr()) || (excepted && unicast(ap.Addr())) {
					return nil
				}
				return fmt.Errorf("%w: %s resolves to %s", ErrDenied, addr, ap.Addr())
			},
		}
		return d.DialContext(ctx, network, addr)
	}
}

// guard enforces the scheme on every request (redirect hops included) and caps bodies.
type guard struct {
	next      http.RoundTripper
	allowHTTP bool
	max       int64
}

func (g *guard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" && (!g.allowHTTP || req.URL.Scheme != "http") {
		return nil, fmt.Errorf("%w: %s", ErrScheme, req.URL.Scheme)
	}
	resp, err := g.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > g.max {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, resp.ContentLength)
	}
	resp.Body = &capped{rc: resp.Body, left: g.max}
	return resp, nil
}

// capped fails a read that goes past the cap instead of silently truncating.
type capped struct {
	rc   io.ReadCloser
	left int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		if n, _ := c.rc.Read(one[:]); n > 0 {
			return 0, ErrTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.rc.Read(p)
	c.left -= int64(n)
	return n, err
}

func (c *capped) Close() error { return c.rc.Close() }
