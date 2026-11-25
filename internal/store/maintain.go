package store

import (
	"context"
	"database/sql"
	"errors"
)

const (
	defaultKeepCommands = 10000
	defaultEffectBatch  = 2000
)

// MaintainOptions bounds a single Store.Maintain pass.
type MaintainOptions struct {
	// KeepCommands is how many newest command idempotency rows to retain.
	// Zero uses defaultKeepCommands; a negative value keeps all rows.
	KeepCommands int
	// EffectBatch is the maximum number of completed outbox rows deleted in
	// one pass. Zero or negative uses defaultEffectBatch.
	EffectBatch int
}

// MaintainResult summarizes what Store.Maintain pruned and checkpointed.
type MaintainResult struct {
	PrunedEffects   int64
	PrunedCommands  int64
	WALBusy         int
	WALLog          int
	WALCheckpointed int
}

// Maintain prunes completed outbox rows and old command idempotency receipts
// and runs a passive WAL checkpoint so long-lived databases stay bounded.
func (s *Store) Maintain(ctx context.Context, opts MaintainOptions) (MaintainResult, error) {
	keep := opts.KeepCommands
	if keep == 0 {
		keep = defaultKeepCommands
	}
	batch := opts.EffectBatch
	if batch <= 0 {
		batch = defaultEffectBatch
	}

	var res MaintainResult
	if err := s.prune(ctx, keep, batch, &res); err != nil {
		return res, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&res.WALBusy, &res.WALLog, &res.WALCheckpointed); err != nil {
		return res, err
	}
	return res, nil
}

func (s *Store) prune(ctx context.Context, keep, batch int, res *MaintainResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	r, err := tx.ExecContext(ctx, `DELETE FROM effects WHERE id IN (SELECT id FROM effects WHERE state = 'done' ORDER BY id LIMIT ?)`, batch)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err == nil {
		res.PrunedEffects = n
	}

	if keep >= 0 {
		var cutoff sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT seq FROM commands ORDER BY seq DESC LIMIT 1 OFFSET ?`, keep).Scan(&cutoff)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case cutoff.Valid:
			r, err := tx.ExecContext(ctx, `DELETE FROM commands WHERE seq <= ?`, cutoff.Int64)
			if err != nil {
				return err
			}
			if n, err := r.RowsAffected(); err == nil {
				res.PrunedCommands = n
			}
		}
	}
	return tx.Commit()
}
