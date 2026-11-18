//go:build linux

package intents

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The save's recovery table (ADR-0033, "Recovery tables"), one case per row and per kind of
// temp the row names. Each case sets the target T and the temp P as the crash or the outside
// writer left them, recovers, and checks the action's outcome: the intent, the row, the
// temp, the copies in recovery/<op-id>/, and that no path reached the logs.
func TestRecoverASave(t *testing.T) {
	type outcome int
	const (
		drop    outcome = iota
		keep            // drop, keeping the temp
		forward         // roll forward
		flag
	)
	cases := []struct {
		name   string
		target string // "" absent, "/" a folder
		temp   string // "" absent, "/" a folder, "=" the same inode as the target
		want   outcome
	}{
		{"base, no temp", "base", "", drop},
		{"base, a partial temp", "base", "ne", drop},
		{"base, a complete temp", "base", "new", drop},
		{"new, no temp", "new", "", forward},
		{"new, the temp the same inode", "new", "=", forward},
		{"new, a temp of another inode", "new", "new", drop},
		{"new, a partial temp of another inode", "new", "ne", drop},
		{"other, no temp", "other", "", drop},
		{"other, a partial temp", "other", "ne", drop},
		{"absent, no temp", "", "", drop},
		{"a folder, a partial temp", "/", "ne", drop},
		{"other, a complete temp", "other", "new", keep},
		{"absent, a complete temp", "", "new", keep},
		{"a folder, a complete temp", "/", "new", keep},
		{"other, the temp the same inode", "other", "=", flag},
		{"base, the temp the same inode", "base", "=", flag},
		{"a temp that is a folder", "base", "/", flag},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			before := e.saveIntent()
			target := filepath.Join(e.dir, "notes", "a.md")
			must(t, os.Remove(target))
			switch c.target {
			case "":
			case "/":
				must(t, os.Mkdir(target, 0o700))
			default:
				e.write("notes/a.md", c.target)
			}
			temp := filepath.Join(e.dir, e.temp())
			switch c.temp {
			case "":
			case "/":
				must(t, os.Mkdir(temp, 0o700))
			case "=":
				must(t, os.Link(target, temp))
			default:
				e.write(e.temp(), c.temp)
			}
			e.recover()
			in, open := e.intent()
			after := e.row()
			switch c.want {
			case forward:
				sum := sha256.Sum256([]byte("new"))
				if open || after.Generation != before.Generation+1 || string(after.SHA256) != string(sum[:]) {
					t.Fatalf("rolled forward: intent %v, row %+v; want the intent ended and generation %d, new hash",
						open, after, before.Generation+1)
				}
				// The stat after the temp's name went: unlinking it changed the ctime.
				var st syscall.Stat_t
				must(t, syscall.Stat(target, &st))
				if after.Stat.Inode != st.Ino || after.Stat.CtimeNS != st.Ctim.Nano() || after.Suspect {
					t.Fatalf("rolled forward with stat %+v; want the target's current one", after.Stat)
				}
			case flag:
				if !open || !in.Flagged {
					t.Fatalf("intent %+v (open %v); want it flagged", in, open)
				}
				if after.Generation != before.Generation {
					t.Fatalf("a flagged save changed the row: %+v", after)
				}
			default:
				if open || after.Generation != before.Generation || string(after.SHA256) != string(before.SHA256) {
					t.Fatalf("dropped: intent %v, row %+v; want the intent ended, the row as it was", open, after)
				}
			}
			copies := e.copies()
			switch c.want {
			case keep:
				if len(copies) != 1 || copies["temp"] != "new" {
					t.Fatalf("recovery copies %v; want the complete temp", copies)
				}
			case flag:
				if c.temp != "/" && copies["temp"] == "" || (c.target != "" && c.target != "/" && copies["target"] != c.target) {
					t.Fatalf("recovery copies %v; want both sides that are files", copies)
				}
			default:
				if len(copies) != 0 {
					t.Fatalf("recovery copies %v; want none", copies)
				}
			}
			if spared := c.want == flag; e.exists(e.temp()) != (spared && c.temp != "") {
				t.Fatalf("the temp is there: %v; want it only for a flagged intent", e.exists(e.temp()))
			}
			if c.temp == "=" && c.want == forward && e.read("notes/a.md") != "new" {
				t.Fatal("unlinking the temp's name lost the saved note")
			}
			if strings.Contains(e.logs.String(), "a.md") {
				t.Fatalf("a path reached the logs: %s", e.logs.String())
			}
		})
	}
}
