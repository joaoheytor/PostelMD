//go:build linux

package sql

import (
	"context"
	"database/sql"
	"time"
)

// The trash record's states (ADR-0003 §2, ADR-0033): moving → trashed (a delete), then
// trashed → purging → gone (the purge), or trashed → restoring → gone (a restore). Each
// change is committed before its step.

// NewTrashItem is what a delete's trash record holds besides its op ID and times.
type NewTrashItem struct {
	Name         string // the item's name, its entry in the stamp folder
	OriginalPath string
	Kind         string // "file" or "folder"
}

// BeginDelete commits a delete's intent and its trash record, moving, in one transaction:
// the step before the stamp folder's mkdir. The delete's clock (the 7-day purge) starts now.
func (db *DB) BeginDelete(ctx context.Context, opID string, p DeleteIntent, item NewTrashItem, now time.Time) error {
	return db.intentTx(ctx, "delete-begin", func(tx *sql.Tx) error {
		if err := insertIntent(ctx, tx, opID, p, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO trash_records (op_id, name, original_path, kind, state,
			deleted_at_ms, clock_ms) VALUES (?, ?, ?, ?, 'moving', ?, ?)`,
			opID, []byte(item.Name), []byte(item.OriginalPath), item.Kind, now.UnixMilli(), now.UnixMilli())
		return err
	})
}

// CompleteDelete records an applied delete: every row the intent names is tombstoned
// (trashed), the holds under the item are released, the record goes from moving to trashed,
// and the intent ends, in one transaction.
func (db *DB) CompleteDelete(ctx context.Context, opID string, now time.Time) error {
	return db.intentTx(ctx, "delete-complete", func(tx *sql.Tx) error {
		var p DeleteIntent
		if err := readOpen(ctx, tx, opID, &p); err != nil {
			return err
		}
		for _, f := range p.Files {
			err := TombstoneFile{Row: Row{ID: f.ID, Path: string(f.Path), Generation: f.Generation}, Reason: "trashed"}.apply(ctx, tx, now)
			if err != nil {
				return staleAs(err, ErrIntentStale)
			}
		}
		if err := releaseUnder(ctx, tx, string(p.Path)); err != nil {
			return err
		}
		if err := setTrashState(ctx, tx, opID, "moving", "trashed"); err != nil {
			return staleAs(err, ErrTrashState)
		}
		return deleteIntent(ctx, tx, opID)
	})
}

// BeginRestore moves the record p.TrashOpID from trashed to restoring by compare-and-set and
// commits the restore's intent, in one transaction. A record in any other state (purging,
// restoring, moving) or none is ErrTrashState (409), and nothing changes.
func (db *DB) BeginRestore(ctx context.Context, opID string, p RestoreIntent, now time.Time) error {
	return db.intentTx(ctx, "restore-begin", func(tx *sql.Tx) error {
		if err := setTrashState(ctx, tx, p.TrashOpID, "trashed", "restoring"); err != nil {
			return staleAs(err, ErrTrashState)
		}
		return insertIntent(ctx, tx, opID, p, now)
	})
}

// CompleteRestore records an applied restore: each restored file gets a new ID at
// generation 1 (tombstones never come back, ADR-0003), the record is dropped and the intent
// ends, in one transaction. Rows and holds on the new paths are resolved as for a create.
func (db *DB) CompleteRestore(ctx context.Context, opID string, files []NewFile, now time.Time) error {
	return db.intentTx(ctx, "restore-complete", func(tx *sql.Tx) error {
		var p RestoreIntent
		if err := readOpen(ctx, tx, opID, &p); err != nil {
			return err
		}
		if err := freeTree(ctx, tx, string(p.To), now); err != nil {
			return err
		}
		for _, f := range files {
			if !under(f.Path, string(p.To)) && f.Path != string(p.To) {
				return ErrBadIntent
			}
			if err := insertRow(ctx, tx, NewID(now), f.Path, f.SHA256, f.Stat); err != nil {
				return err
			}
		}
		if err := one(ctx, tx, "drop a restored trash record", "DELETE FROM trash_records WHERE op_id = ? AND state = 'restoring'",
			p.TrashOpID); err != nil {
			return staleAs(err, ErrTrashState)
		}
		return deleteIntent(ctx, tx, opID)
	})
}

// BeginPurge moves a record from trashed to purging by compare-and-set, committed before the
// purge's first removal (ADR-0033). It takes no vault mutex: the record's state excludes a
// restore. Any other state is ErrTrashState.
func (db *DB) BeginPurge(ctx context.Context, opID string) error {
	return db.intentTx(ctx, "purge-begin", func(tx *sql.Tx) error {
		return staleAs(setTrashState(ctx, tx, opID, "trashed", "purging"), ErrTrashState)
	})
}

// DropTrashRecord removes the record opID if it is in state: a finished purge (purging), or
// an item removed outside PostelMD (trashed, ADR-0003's last row). Tombstones stay.
func (db *DB) DropTrashRecord(ctx context.Context, opID, state string) error {
	return db.intentTx(ctx, "trash-drop", func(tx *sql.Tx) error {
		err := one(ctx, tx, "drop a trash record", "DELETE FROM trash_records WHERE op_id = ? AND state = ?", opID, state)
		return staleAs(err, ErrTrashState)
	})
}

// setTrashState moves the record opID from one state to another; errStale if it isn't in
// from.
func setTrashState(ctx context.Context, tx *sql.Tx, opID, from, to string) error {
	return one(ctx, tx, "change a trash record's state", "UPDATE trash_records SET state = ? WHERE op_id = ? AND state = ?", to, opID, from)
}
