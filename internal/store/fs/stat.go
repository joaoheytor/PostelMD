package fs

// Stat is what file identity reads of a regular file (ADR-0031): the signals the startup
// scan compares to decide whether to hash a file again, and the rename rules match on.
type Stat struct {
	Size          int64
	Device, Inode uint64
	// MtimeNS and CtimeNS are nanoseconds since 1970: rule 1 compares mtime exactly.
	MtimeNS, CtimeNS int64
}
