//go:build linux

package sql

import (
	"encoding/json"
	"strings"
)

// The intents' payloads (ADR-0033). None holds note content: until the client gets its
// answer, its draft is the copy (ADR-0003 §5). A save's or create's temp is
// .postelmd-tmp/<op-id>, so no payload names it.

// BytePath is a vault-relative path in a payload: its bytes as on disk, which needn't be
// UTF-8, so the JSON holds them in base64 (a JSON string would replace invalid bytes).
type BytePath string

// MarshalJSON writes the path's bytes in base64.
func (p BytePath) MarshalJSON() ([]byte, error) { return json.Marshal([]byte(p)) }

// UnmarshalJSON reads them back.
func (p *BytePath) UnmarshalJSON(b []byte) error {
	var raw []byte
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*p = BytePath(raw)
	return nil
}

// validPath: relative, clean enough for the store to compare (the vault checks the rest).
func (p BytePath) validPath() bool {
	s := string(p)
	return s != "" && s != "." && !strings.HasPrefix(s, "/") && !strings.HasSuffix(s, "/") &&
		!strings.Contains(s, "\x00") && !strings.Contains(s, "//")
}

// Affected is a row an operation moves or ends, as the intent recorded it.
type Affected struct {
	ID         string   `json:"id"`
	Generation int64    `json:"generation"`
	Path       BytePath `json:"path"`
}

func validAffected(rows []Affected) bool {
	for _, r := range rows {
		if !ValidID(r.ID) || r.Generation < 1 || !r.Path.validPath() {
			return false
		}
	}
	return true
}

// SaveIntent: the file's ID and path, its generation and full hash at the check (the base),
// the new content's full hash, and the target's stat at the check.
type SaveIntent struct {
	FileID         string   `json:"file_id"`
	Path           BytePath `json:"path"`
	BaseGeneration int64    `json:"base_generation"`
	BaseSHA256     []byte   `json:"base_sha256"`
	NewSHA256      []byte   `json:"new_sha256"`
	Stat           FileStat `json:"stat"`
}

func (SaveIntent) kind() string { return IntentSave }
func (p SaveIntent) valid() bool {
	return ValidID(p.FileID) && p.Path.validPath() && p.BaseGeneration >= 1 && len(p.BaseSHA256) == 32 && len(p.NewSHA256) == 32
}

// CreateIntent: the destination and the new content's full hash.
type CreateIntent struct {
	Path      BytePath `json:"path"`
	NewSHA256 []byte   `json:"new_sha256"`
}

func (CreateIntent) kind() string  { return IntentCreate }
func (p CreateIntent) valid() bool { return p.Path.validPath() && len(p.NewSHA256) == 32 }

// MoveIntent: a file or folder from From to To, the source's (device, inode), every row under
// it, and the mechanism.
type MoveIntent struct {
	From      BytePath   `json:"from"`
	To        BytePath   `json:"to"`
	Folder    bool       `json:"folder"`
	Device    uint64     `json:"device"`
	Inode     uint64     `json:"inode"`
	Files     []Affected `json:"files"`
	Mechanism string     `json:"mechanism"`
}

func (MoveIntent) kind() string { return IntentMove }
func (p MoveIntent) valid() bool {
	return p.From.validPath() && p.To.validPath() && p.From != p.To && !under(string(p.To), string(p.From)) && !under(string(p.From), string(p.To)) &&
		p.Mechanism == MechanismNoReplace && validAffected(p.Files) && allUnder(p.Files, string(p.From))
}

// DeleteIntent: the item at Path, moved to .postelmd-trash/<op-id>/<name>; its (device,
// inode), every row under it, and the mechanism. Its trash record has the same op ID.
type DeleteIntent struct {
	Path      BytePath   `json:"path"`
	Folder    bool       `json:"folder"`
	Device    uint64     `json:"device"`
	Inode     uint64     `json:"inode"`
	Files     []Affected `json:"files"`
	Mechanism string     `json:"mechanism"`
}

func (DeleteIntent) kind() string { return IntentDelete }
func (p DeleteIntent) valid() bool {
	return p.Path.validPath() && p.Mechanism == MechanismNoReplace && validAffected(p.Files) && allUnder(p.Files, string(p.Path))
}

// RestoreIntent: the trash record's op ID, the entry's path in the vault (in the trash) and
// (device, inode), the destination, and the mechanism.
type RestoreIntent struct {
	TrashOpID string   `json:"trash_op_id"`
	Entry     BytePath `json:"entry"`
	To        BytePath `json:"to"`
	Folder    bool     `json:"folder"`
	Device    uint64   `json:"device"`
	Inode     uint64   `json:"inode"`
	Mechanism string   `json:"mechanism"`
}

func (RestoreIntent) kind() string { return IntentRestore }
func (p RestoreIntent) valid() bool {
	return checkOpID(IntentDelete, p.TrashOpID) == nil && p.Entry.validPath() && p.To.validPath() && p.Mechanism == MechanismNoReplace
}

// under reports whether p is inside the folder dir (not dir itself).
func under(p, dir string) bool { return strings.HasPrefix(p, dir+"/") }

// allUnder reports whether every row is root itself or under it.
func allUnder(rows []Affected, root string) bool {
	for _, r := range rows {
		if string(r.Path) != root && !under(string(r.Path), root) {
			return false
		}
	}
	return true
}
