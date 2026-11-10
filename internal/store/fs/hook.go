package fs

// OpKind names one step that changes what is on disk, or makes earlier changes durable.
type OpKind int

// The steps a Root reports to its Hook. Writes and directory changes are durable only after
// the matching sync (ADR-0003 §5): file data after OpSyncFile on that file, directory entries
// after OpSyncDir on that directory (OpRemove too).
const (
	OpCreate          OpKind = iota + 1 // Path created exclusively with Data (not yet synced)
	OpSyncFile                          // fsync of Path's data
	OpRename                            // Path renamed to To, replacing To
	OpRenameNoReplace                   // Path renamed to To, failing if To exists
	OpMkdir                             // directory Path created
	OpSyncDir                           // fsync of directory Path's entries
	OpRemove                            // file or empty directory Path unlinked
)

var opNames = map[OpKind]string{
	OpCreate: "create", OpSyncFile: "sync-file", OpRename: "rename",
	OpRenameNoReplace: "rename-noreplace", OpMkdir: "mkdir", OpSyncDir: "sync-dir", OpRemove: "remove",
}

func (k OpKind) String() string { return opNames[k] }

// Op is one step, with paths relative to the root.
type Op struct {
	Kind OpKind
	Path string
	To   string // OpRename, OpRenameNoReplace
	Data []byte // OpCreate
}

// Hook observes every step a Root takes. It exists for the failure-scenario suite
// (CLAUDE.md, "Testing conventions"): a hook can stop the process at any step (Before
// returns an error, and the step doesn't run) and model what survives a power loss.
// Production code never sets one.
type Hook interface {
	// Before runs before the step. A non-nil error aborts it and is returned to the caller.
	Before(op Op) error
	// After runs once the step has finished; err is the step's own error, if any.
	After(op Op, err error)
}

// Option configures OpenRoot.
type Option func(*options)

type options struct{ hook Hook }

// WithHook makes the root report every step to h (tests only).
func WithHook(h Hook) Option { return func(o *options) { o.hook = h } }

// step runs fn as op, reporting it to the hook if there is one.
func step(h Hook, op Op, fn func() error) error {
	if h == nil {
		return fn()
	}
	if err := h.Before(op); err != nil {
		return err
	}
	err := fn()
	h.After(op, err)
	return err
}
