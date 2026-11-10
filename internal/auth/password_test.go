package auth

import (
	"runtime"
	"testing"
)

// argon2id takes 64 MiB per hash (ADR-0005). Go would keep that memory after a login, which
// puts an idle server over the PRD's 80 MB RSS budget, so hashing hands it back at once.
func TestPasswordHashingGivesItsMemoryBack(t *testing.T) {
	const pw = "correct horse battery staple"
	held := func() uint64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapSys - m.HeapReleased
	}
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if h := held(); h > 32<<20 {
		t.Fatalf("after HashPassword the heap holds %d MiB from the OS, want under 32", h>>20)
	}
	if ok, err := VerifyPassword(hash, pw); !ok || err != nil {
		t.Fatalf("VerifyPassword = %v, %v", ok, err)
	}
	if h := held(); h > 32<<20 {
		t.Fatalf("after VerifyPassword the heap holds %d MiB from the OS, want under 32", h>>20)
	}
}
