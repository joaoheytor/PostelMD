package git

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// ErrRemoteURL: a remote URL isn't allowed (ADR-0016, "Transports"; #40). The vault goes to
// `unsupported` when a stored remote fails the check.
var ErrRemoteURL = errors.New("store/git: remote URL not allowed")

const maxRemoteURL = 2048

// ValidateRemoteURL accepts only https://host[:port]/path with no credentials, query or
// fragment. Local paths, file://, git://, http://, ssh and scp-style remotes are refused:
// a local remote would let a vault read another vault's history, and anything not https
// would bypass the egress client. Callers check a URL before storing it and again before
// every fetch or push (Repo.RemoteURL).
func ValidateRemoteURL(raw string) error {
	deny := func(why string) error { return fmt.Errorf("%w: %s", ErrRemoteURL, why) }
	if len(raw) > maxRemoteURL {
		return deny("too long")
	}
	if strings.IndexFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return deny("contains spaces or control characters")
	}
	// A literal, lowercase prefix: url.Parse lowercases the scheme and go-git doesn't, so
	// the two parsers must not get a chance to disagree.
	if !strings.HasPrefix(raw, "https://") {
		return deny("only https:// remotes are supported")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return deny("unparseable")
	}
	switch {
	case u.User != nil:
		return deny("credentials in the URL; use the vault's stored credentials")
	case u.Hostname() == "":
		return deny("no host")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return deny("query or fragment")
	case u.Port() != "" && !validPort(u.Port()):
		return deny("bad port")
	}
	ep, err := transport.NewEndpoint(raw)
	// go-git keeps the brackets around an IPv6 host; net/url drops them.
	if err != nil || ep.Protocol != "https" || strings.Trim(ep.Host, "[]") != u.Hostname() || ep.User != "" {
		return deny("parsers disagree about the URL")
	}
	return nil
}

func validPort(p string) bool {
	n := 0
	for _, c := range p {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
		if n > 65535 {
			return false
		}
	}
	return n > 0
}

// InstallTransports makes egress the only way go-git reaches the network (architecture §11,
// CLAUDE.md rule 13). go-git picks a transport per scheme process-wide, so this runs once
// at startup, before any vault is opened: https goes through c, and every other scheme
// (file, git, http, ssh) is removed. A URL that somehow skipped ValidateRemoteURL then
// fails with "unsupported scheme" instead of touching the local filesystem.
func InstallTransports(c *http.Client) {
	for scheme := range client.Protocols {
		client.InstallProtocol(scheme, nil)
	}
	client.InstallProtocol("https", githttp.NewClient(c))
}

// RemoteURL returns the remote's URL after checking it again. The check sees the URL after
// any url.<base>.insteadOf rewrite in .git/config (go-git applies them on load), and it
// covers remotes that arrived with an imported repository. A remote must have exactly one
// URL.
func (g *Repo) RemoteURL(name string) (string, error) {
	rem, err := g.r.Remote(name)
	if err != nil {
		return "", fmt.Errorf("store/git: remote %q: %w", name, err)
	}
	urls := rem.Config().URLs
	if len(urls) != 1 {
		return "", fmt.Errorf("%w: remote %q has %d URLs, want 1", ErrRemoteURL, name, len(urls))
	}
	if err := ValidateRemoteURL(urls[0]); err != nil {
		return "", fmt.Errorf("remote %q: %w", name, err)
	}
	return urls[0], nil
}
