package http

import (
	"log/slog"
	"net"
	stdhttp "net/http"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

// refusedLogEvery bounds the warnings: a rebinding page can retry in a loop.
const refusedLogEvery = time.Minute

// AllowHosts refuses (421) a request whose Host isn't localhost (or a name under
// .localhost), an IP literal, or one of allowed (lower case, no port; internal/config
// normalizes them). That defeats DNS rebinding: a site the user visits can point its own
// name at this server's address, but its requests then carry that name (ADR-0005).
func AllowHosts(allowed []string, log *slog.Logger, next stdhttp.Handler) stdhttp.Handler {
	var lastLog atomic.Int64
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if hostAllowed(r.Host, allowed) {
			next.ServeHTTP(w, r)
			return
		}
		now := time.Now().UnixNano()
		if last := lastLog.Load(); now-last >= int64(refusedLogEvery) && lastLog.CompareAndSwap(last, now) {
			log.Warn("refused a request for a host name that isn't allowed; if it is this server's, add it to allowed_hosts (POSTELMD_ALLOWED_HOSTS)",
				"host", safeHost(r.Host))
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(stdhttp.StatusMisdirectedRequest)
		_, _ = w.Write([]byte("PostelMD doesn't answer to this host name. If it is this server's, add it to " +
			"allowed_hosts in config.yaml (or POSTELMD_ALLOWED_HOSTS).\n"))
	})
}

func hostAllowed(hostport string, allowed []string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true // browsers resolve these to loopback themselves (RFC 6761)
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return true // an address can't be rebound
	}
	return slices.Contains(allowed, host)
}

// safeHost is a client-chosen Host fit for a log line: escaped and at most 64 bytes.
func safeHost(h string) string {
	if len(h) > 64 {
		h = h[:64] + "…"
	}
	return oneLine(h)
}
