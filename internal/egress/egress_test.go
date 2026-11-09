package egress

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestPublic(t *testing.T) {
	denied := []string{
		"0.0.0.0", "0.1.2.3", "10.1.2.3", "100.64.0.1", "100.100.100.100", "127.0.0.1",
		"169.254.169.254", "172.16.0.1", "172.31.255.255", "192.0.0.8", "192.0.2.1",
		"192.88.99.1", "192.168.1.1", "198.18.0.1", "198.51.100.7", "203.0.113.9",
		"224.0.0.1", "240.0.0.1", "255.255.255.255",
		"::", "::1", "::10.0.0.1", "::ffff:10.0.0.1", "::ffff:127.0.0.1", "::ffff:169.254.169.254",
		"64:ff9b::a00:1", "64:ff9b:1::1", "100::1", "2001::1", "2001:db8::1",
		"2002:a00:1::1", "3fff::1", "5f00::1", "fc00::1", "fd12::1", "fe80::1", "ff02::1",
	}
	allowed := []string{"1.1.1.1", "8.8.8.8", "140.82.112.3", "::ffff:1.1.1.1", "2606:4700::1111", "2a00:1450:4001::64"}
	for _, s := range denied {
		if Public(netip.MustParseAddr(s)) {
			t.Errorf("Public(%s) = true, want false", s)
		}
	}
	for _, s := range allowed {
		if !Public(netip.MustParseAddr(s)) {
			t.Errorf("Public(%s) = false, want true", s)
		}
	}
	if Public(netip.Addr{}) {
		t.Error("the zero address is public")
	}
}

func TestUnicastForExceptions(t *testing.T) {
	for s, want := range map[string]bool{
		"192.168.1.10": true, "100.101.102.103": true, "127.0.0.1": true,
		"0.0.0.0": false, "::": false, "224.0.0.1": false, "ff02::1": false, "255.255.255.255": false,
	} {
		if got := unicast(netip.MustParseAddr(s)); got != want {
			t.Errorf("unicast(%s) = %v, want %v", s, got, want)
		}
	}
}

// The test servers listen on 127.0.0.1, which only an exception can reach.
func tlsServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return srv, u.Host
}

func client(srv *httptest.Server, o Options) *http.Client {
	o.RootCAs = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	return New(o)
}

func TestClientDeniesPrivateUnlessExcepted(t *testing.T) {
	srv, host := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	if _, err := client(srv, Options{}).Get(srv.URL); !errors.Is(err, ErrDenied) {
		t.Fatalf("loopback without an exception: err = %v, want ErrDenied", err)
	}
	if _, err := client(srv, Options{Exceptions: []string{"127.0.0.1:1"}}).Get(srv.URL); !errors.Is(err, ErrDenied) {
		t.Fatalf("an exception for another port applied: err = %v", err)
	}
	resp, err := client(srv, Options{Exceptions: []string{host}}).Get(srv.URL)
	if err != nil {
		t.Fatalf("excepted host: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("body %q", body)
	}
}

func TestClientRefusesPlainHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	if _, err := New(Options{Exceptions: []string{u.Host}}).Get(srv.URL); !errors.Is(err, ErrScheme) {
		t.Fatalf("plain http: err = %v, want ErrScheme", err)
	}
	resp, err := New(Options{Exceptions: []string{u.Host}, AllowHTTP: true}).Get(srv.URL)
	if err != nil {
		t.Fatalf("plain http with AllowHTTP: %v", err)
	}
	_ = resp.Body.Close()
}

func TestClientRedirects(t *testing.T) {
	var srv *httptest.Server
	srv, host := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/to-http":
			http.Redirect(w, r, strings.Replace(srv.URL, "https", "http", 1)+"/x", http.StatusFound)
		case "/to-private":
			http.Redirect(w, r, "https://10.0.0.1/x", http.StatusFound)
		}
	})
	c := client(srv, Options{Exceptions: []string{host}})
	for path, want := range map[string]error{"/loop": ErrTooManyRedirects, "/to-http": ErrScheme, "/to-private": ErrDenied} {
		if _, err := c.Get(srv.URL + path); !errors.Is(err, want) {
			t.Errorf("GET %s: err = %v, want %v", path, err, want)
		}
	}
}

func TestClientCapsResponses(t *testing.T) {
	big := strings.Repeat("x", 100)
	srv, host := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chunked" {
			w.(http.Flusher).Flush() // no Content-Length: the cap applies while reading
		}
		_, _ = io.WriteString(w, big)
	})
	c := client(srv, Options{Exceptions: []string{host}, MaxResponseBytes: 50})
	if _, err := c.Get(srv.URL + "/sized"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("declared length over the cap: err = %v, want ErrTooLarge", err)
	}
	resp, err := c.Get(srv.URL + "/chunked")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.ReadAll(resp.Body); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("streamed body over the cap: err = %v, want ErrTooLarge", err)
	}
	exact, err := New(Options{Exceptions: []string{host}, MaxResponseBytes: 100,
		RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}).Get(srv.URL + "/chunked")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exact.Body.Close() }()
	if b, err := io.ReadAll(exact.Body); err != nil || len(b) != 100 {
		t.Fatalf("a body exactly at the cap: %d bytes, %v", len(b), err)
	}
}

func TestRedirectsNeverCarryCredentialsToAnotherHost(t *testing.T) {
	get := func(rawURL string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, rawURL, nil)
		r.Header.Set("Authorization", "Basic c2VjcmV0")
		r.Header.Set("Cookie", "s=1")
		r.Header.Set("Accept", "*/*")
		return r
	}
	tests := []struct {
		name string
		via  []string
		next string
		keep bool
	}{
		{"same host", []string{"https://git.example.com/a"}, "https://git.example.com/b", true},
		{"same host, other case", []string{"https://git.example.com/a"}, "https://GIT.example.com/b", true},
		{"subdomain", []string{"https://example.com/a"}, "https://evil.example.com/b", false},
		{"other port", []string{"https://example.com/a"}, "https://example.com:8443/b", false},
		{"back to the first host", []string{"https://example.com/a", "https://other.test/b"}, "https://example.com/c", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var via []*http.Request
			for _, u := range tt.via {
				via = append(via, get(u))
			}
			req := get(tt.next)
			if err := checkRedirect(req, via); err != nil {
				t.Fatal(err)
			}
			kept := req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != ""
			if kept != tt.keep {
				t.Fatalf("credentials kept = %v, want %v", kept, tt.keep)
			}
			if req.Header.Get("Accept") == "" {
				t.Fatal("a non-credential header was removed")
			}
		})
	}
}
