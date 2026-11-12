package vault

// ConflictError is a failed save precondition. It carries the current note for the
// conflict view, unless TooLarge: the file is over the store's read limit, so there is no
// current note (Current nil, Rev empty), and the error is also ErrTooLargeToRead (ADR-0029).
type ConflictError struct {
	Current  []byte
	Rev      Rev
	TooLarge bool
}

func (e *ConflictError) Error() string { return ErrConflict.Error() }

func (e *ConflictError) Unwrap() []error {
	if e.TooLarge {
		return []error{ErrConflict, ErrTooLargeToRead}
	}
	return []error{ErrConflict}
}
