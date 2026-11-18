package vault

import (
	"crypto/sha256"
	"errors"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"testing"
)

// What intent recovery (ADR-0033) needs from the vault: Open no longer sweeps the temp
// folder; the start sweeps it after recovery, sparing the temps an intent names.

const opTemp = "0123456789abcdef0123456789abcdef"

func TestOpenLeavesTempsForTheSweep(t *testing.T) {
	first, dir := newVault(t)
	_ = first.Close()
	writeFiles(t, dir, map[string]string{TmpDir + "/" + opTemp: "an intent's temp", TmpDir + "/other": "half a save",
		TmpDir + "/repo/HEAD": "x"})
	v, err := Open(dir)
	must(t, err)
	t.Cleanup(func() { _ = v.Close() })
	if n := temps(dir); n != 3 {
		t.Fatalf("Open left %d temps, want all 3: recovery judges them first", n)
	}
	must(t, v.Sweep(func(name string) bool { return name == opTemp }))
	if n := temps(dir); n != 1 || content(t, dir, TmpDir+"/"+opTemp) != "an intent's temp" {
		t.Fatalf("after the sweep, %d temps; want only the spared one", n)
	}
	must(t, v.Sweep(nil))
	assertNoTemps(t, dir)
}

func TestJudge(t *testing.T) {
	v, dir := newVault(t)
	writeFiles(t, dir, map[string]string{TmpDir + "/" + opTemp: "new"})
	must(t, os.Mkdir(filepath.Join(dir, "notes", "sub"), 0o700))
	must(t, os.Symlink("a.md", filepath.Join(dir, "notes", "link.md")))
	a, err := v.Judge(ctx, "notes/a.md")
	must(t, err)
	fi, err := os.Stat(filepath.Join(dir, "notes", "a.md"))
	must(t, err)
	if !a.Present || !a.Regular || a.SHA256 != sha256.Sum256([]byte("A")) || a.Stat.Size != 1 || a.Stat.MtimeNS != fi.ModTime().UnixNano() {
		t.Fatalf("Judge(a.md) = %+v", a)
	}
	for p, want := range map[string]Judged{
		"notes/missing.md": {}, "notes/a.md/below": {}, "nowhere/x.md": {},
		"notes/sub": {Present: true}, "notes/link.md": {Present: true},
	} {
		if got, err := v.Judge(ctx, p); err != nil || got != want {
			t.Errorf("Judge(%q) = %+v, %v; want %+v", p, got, err, want)
		}
	}
	tmp, err := v.JudgeTemp(ctx, opTemp)
	must(t, err)
	if !tmp.Regular || tmp.SHA256 != sha256.Sum256([]byte("new")) {
		t.Fatalf("JudgeTemp = %+v", tmp)
	}
	if _, err := v.Judge(ctx, TmpDir+"/"+opTemp); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Judge of a reserved path = %v, want ErrNotFound", err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", ownerMarker} {
		if _, err := v.JudgeTemp(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("JudgeTemp(%q) = %v, want ErrNotFound", bad, err)
		}
	}
}

func TestTempsReadAndRemoved(t *testing.T) {
	v, dir := newVault(t)
	writeFiles(t, dir, map[string]string{TmpDir + "/" + opTemp: "new"})
	for name, open := range map[string]func() (io.ReadCloser, error){
		"temp": func() (io.ReadCloser, error) { return v.OpenTemp(opTemp) },
		"note": func() (io.ReadCloser, error) { return v.OpenNote("notes/a.md") },
	} {
		f, err := open()
		must(t, err)
		b, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil || (name == "temp" && string(b) != "new") || (name == "note" && string(b) != "A") {
			t.Fatalf("%s read %q, %v", name, b, err)
		}
	}
	must(t, v.RemoveTemp(opTemp))
	if _, err := os.Stat(filepath.Join(dir, TmpDir, opTemp)); !errors.Is(err, iofs.ErrNotExist) {
		t.Fatalf("the temp is still there: %v", err)
	}
	must(t, v.RemoveTemp(opTemp)) // already gone: done
	if _, err := v.OpenTemp(opTemp); !errors.Is(err, ErrNotFound) {
		t.Fatalf("OpenTemp of a removed temp = %v, want ErrNotFound", err)
	}
}
