package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"glitched/internal/api"
)

const (
	defaultKeepCommands = 10000
	defaultBatch        = 2000
	// prunedKey is the meta row that holds the highest command ID whose
	// receipt Maintain may have pruned.
	prunedKey = "commands_pruned"
)

// errForgotten refuses a command whose receipt Maintain may have pruned:
// it may have been taken already, so it is not taken again.
var errForgotten = fmt.Errorf("%w: it is older than the receipts this engine keeps, so it may have been taken already", api.ErrExpired)

// MaintainOptions bounds a single Store.Maintain pass.
type MaintainOptions struct {
	// KeepCommands is how many receipts, those of the highest command
	// IDs, to retain. Zero uses defaultKeepCommands; a negative value
	// keeps all rows.
	KeepCommands int
	// Batch is the most rows one transaction deletes: Maintain lets other
	// writers in between batches. Zero or negative uses defaultBatch.
	Batch int
}

// MaintainResult summarizes what Store.Maintain pruned and checkpointed.
type MaintainResult struct {
	PrunedEffects   int64
	PrunedCommands  int64
	WALBusy         int
	WALLog          int
	WALCheckpointed int
}

// Maintain prunes completed outbox rows and old command receipts and runs a
// passive WAL checkpoint so long-lived databases stay bounded. A command
// whose receipt it pruned is refused with api.ErrExpired from then on.
func (s *Store) Maintain(ctx context.Context, opts MaintainOptions) (MaintainResult, error) {
	keep := opts.KeepCommands
	if keep == 0 {
		keep = defaultKeepCommands
	}
	batch := opts.Batch
	if batch <= 0 {
		batch = defaultBatch
	}

	var res MaintainResult
	if err := s.batches(ctx, batch, &res.PrunedEffects, func(tx *sql.Tx) (sql.Result, error) {
		return tx.ExecContext(ctx, `DELETE FROM effects WHERE id IN (SELECT id FROM effects WHERE state = 'done' ORDER BY id LIMIT ?)`, batch)
	}); err != nil {
		return res, err
	}
	if keep >= 0 {
		if err := s.batches(ctx, batch, &res.PrunedCommands, func(tx *sql.Tx) (sql.Result, error) {
			return pruneCommands(ctx, tx, keep, batch)
		}); err != nil {
			return res, err
		}
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&res.WALBusy, &res.WALLog, &res.WALCheckpointed); err != nil {
		return res, err
	}
	return res, nil
}

// batches runs del, one transaction at a time, until it deletes fewer than
// batch rows, adding what it deleted to n.
func (s *Store) batches(ctx context.Context, batch int, n *int64, del func(*sql.Tx) (sql.Result, error)) error {
	for {
		deleted, err := s.deleteOnce(ctx, del)
		*n += deleted
		if err != nil || deleted < int64(batch) {
			return err
		}
	}
}

func (s *Store) deleteOnce(ctx context.Context, del func(*sql.Tx) (sql.Result, error)) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	r, err := del(tx)
	if err != nil || r == nil {
		return 0, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// pruneCommands deletes up to batch of the receipts below the keep highest
// command IDs, and first raises prunedKey to the highest it may delete.
func pruneCommands(ctx context.Context, tx *sql.Tx, keep, batch int) (sql.Result, error) {
	var cutoff []byte
	err := tx.QueryRowContext(ctx, `SELECT id FROM commands ORDER BY id DESC LIMIT 1 OFFSET ?`, keep).Scan(&cutoff)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = max(value, excluded.value)`, prunedKey, cutoff); err != nil {
		return nil, err
	}
	return tx.ExecContext(ctx, `DELETE FROM commands WHERE id IN (SELECT id FROM commands WHERE id <= ? ORDER BY id LIMIT ?)`, cutoff, batch)
}

// forgotten is errForgotten for a command with no receipt whose ID is at or
// below the highest one Maintain may have pruned, and nil for any other.
func forgotten(ctx context.Context, q querier, id api.ID) error {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM meta WHERE key = ? AND value >= ?`, prunedKey, id[:]).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errForgotten
	}
	return nil
}
