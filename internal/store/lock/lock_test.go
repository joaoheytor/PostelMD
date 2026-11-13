//go:build linux

package lock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSecondHolderIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	// flock locks belong to the open file description, so a second Acquire in this process
	// conflicts exactly as a second process would.
	_, err = Acquire(path)
	var held *HeldError
	if !errors.As(err, &held) || !strings.Contains(held.Holder, "pid=") {
		t.Fatalf("second Acquire = %v, want a HeldError naming the holder", err)
	}
	// The boot UUID (ADR-0014) is a random version-4 UUID, and the holder record names it.
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(l.BootUUID()) {
		t.Fatalf("BootUUID = %q, want a version-4 UUID", l.BootUUID())
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), " boot="+l.BootUUID()) {
		t.Fatalf("the holder record was damaged or doesn't name the boot UUID: %q", b)
	}
	if out, err := exec.Command("flock", "-n", path, "true").CombinedOutput(); err == nil {
		t.Fatalf("another process got the lock (%s)", out)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	_ = again.Release()
}

func TestNeverFollowsASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "instance.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(filepath.Join(dir, "instance.lock")); err == nil {
		t.Fatal("Acquire followed a symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatal("the symlink target was changed")
	}
}
