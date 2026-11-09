package git

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"

	"github.com/joaoheytor/PostelMD/internal/egress"
)

func TestValidateRemoteURL(t *testing.T) {
	ok := []string{
		"https://github.com/acme/runbooks.git",
		"https://git.example.com:8443/team/notes",
		"https://[2001:db8::1]/repo.git",
		"https://[2001:db8::1]:8443/repo.git",
		"https://gitea.example.com/a/b/c",
	}
	bad := []string{
		"", "/data/other-vault", "../other-vault", "./x", "other-vault", "C:\\vault",
		"file:///data/other-vault", "file://localhost/data/x", "FILE:///data/x",
		"git://example.com/repo", "http://example.com/repo", "HTTPS://example.com/repo",
		"ssh://git@example.com/repo", "git@example.com:acme/repo.git", "example.com:acme/repo",
		"https://user:token@example.com/repo", "https://token@example.com/repo", "https://@example.com/repo",
		"https:///repo", "https://", "https://:443/repo", "https://example.com:0/repo",
		"https://example.com:99999/repo", "https://example.com:443x/repo",
		"https://example.com/repo?x=1", "https://example.com/repo?", "https://example.com/repo#frag",
		"https://example.com/re po", "https://example.com/repo\n", "https://example.com/\x00",
		"https://" + strings.Repeat("a", maxRemoteURL),
	}
	for _, u := range ok {
		if err := ValidateRemoteURL(u); err != nil {
			t.Errorf("ValidateRemoteURL(%q) = %v, want nil", u, err)
		}
	}
	for _, u := range bad {
		if err := ValidateRemoteURL(u); !errors.Is(err, ErrRemoteURL) {
			t.Errorf("ValidateRemoteURL(%q) = %v, want ErrRemoteURL", u, err)
		}
	}
}

func addRemote(t *testing.T, dir string, urls ...string) *Repo {
	t.Helper()
	r, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: urls}); err != nil {
		t.Fatal(err)
	}
	g, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

func TestRemoteURLRevalidatesStoredRemotes(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		_, dir := newRepo(t)
		g := addRemote(t, dir, "https://example.com/repo.git")
		if u, err := g.RemoteURL("origin"); err != nil || u != "https://example.com/repo.git" {
			t.Fatalf("RemoteURL = %q, %v", u, err)
		}
	})
	t.Run("an imported local remote", func(t *testing.T) {
		_, dir := newRepo(t)
		g := addRemote(t, dir, "/data/other-vault")
		if _, err := g.RemoteURL("origin"); !errors.Is(err, ErrRemoteURL) {
			t.Fatalf("RemoteURL = %v, want ErrRemoteURL", err)
		}
	})
	t.Run("several URLs", func(t *testing.T) {
		_, dir := newRepo(t)
		g := addRemote(t, dir, "https://example.com/a.git", "https://example.com/b.git")
		if _, err := g.RemoteURL("origin"); !errors.Is(err, ErrRemoteURL) {
			t.Fatalf("RemoteURL = %v, want ErrRemoteURL", err)
		}
	})
	t.Run("an insteadOf rewrite to a local path", func(t *testing.T) {
		_, dir := newRepo(t)
		gitCLI(t, dir, "config", "url./data/other-vault/.insteadOf", "https://example.com/")
		g := addRemote(t, dir, "https://example.com/repo.git")
		if _, err := g.RemoteURL("origin"); !errors.Is(err, ErrRemoteURL) {
			t.Fatalf("RemoteURL after a rewrite = %v, want ErrRemoteURL", err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		g, _ := newRepo(t)
		if _, err := g.RemoteURL("origin"); err == nil {
			t.Fatal("RemoteURL of a missing remote succeeded")
		}
	})
}

// restoreTransports puts go-git's process-wide transports back after the test.
func restoreTransports(t *testing.T) {
	t.Helper()
	saved := make(map[string]transport.Transport, len(client.Protocols))
	for k, v := range client.Protocols {
		saved[k] = v
	}
	t.Cleanup(func() {
		for k := range client.Protocols {
			client.InstallProtocol(k, nil)
		}
		for k, v := range saved {
			client.InstallProtocol(k, v)
		}
	})
}

// fetch adds remote as origin to a fresh repository and fetches from it.
func fetch(t *testing.T, remote string) error {
	t.Helper()
	_, dir := newRepo(t)
	r, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remote}}); err != nil {
		t.Fatal(err)
	}
	return r.Fetch(&gogit.FetchOptions{RemoteName: "origin"})
}

// TestFileRemoteFailsWithoutValidation fetches from a local path, skipping
// ValidateRemoteURL: with the transports installed, go-git refuses the scheme instead of
// reading the other repository.
func TestFileRemoteFailsWithoutValidation(t *testing.T) {
	restoreTransports(t)
	other, otherDir := newRepo(t)
	if err := other.SetBranch("main", Hash{}, commit(t, other, Hash{}, map[string][]byte{"secret.md": []byte("x")})); err != nil {
		t.Fatal(err)
	}
	InstallTransports(&http.Client{})
	for _, scheme := range []string{"file", "git", "http", "ssh"} {
		if _, ok := client.Protocols[scheme]; ok {
			t.Errorf("transport %q still installed", scheme)
		}
	}
	for _, remote := range []string{otherDir, "file://" + otherDir} {
		if err := fetch(t, remote); err == nil || !strings.Contains(err.Error(), "unsupported scheme") {
			t.Errorf("fetch from %s: err = %v, want unsupported scheme", remote, err)
		}
	}
}

// TestHTTPSGoesThroughEgress: once installed, go-git's https transport is the egress
// client, so its address policy applies to fetches (a loopback remote is refused).
func TestHTTPSGoesThroughEgress(t *testing.T) {
	restoreTransports(t)
	InstallTransports(egress.New(egress.Options{}))
	remote := "https://127.0.0.1:9/repo.git"
	if err := ValidateRemoteURL(remote); err != nil {
		t.Fatal(err) // the URL check is about form; the egress policy judges the address
	}
	if err := fetch(t, remote); err == nil || !strings.Contains(err.Error(), egress.ErrDenied.Error()) {
		t.Fatalf("fetch from loopback = %v, want the egress denial", err)
	}
}
