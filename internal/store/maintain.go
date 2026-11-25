package store

import "context"

const defaultBatch = 2000

// MaintainOptions bounds a single Store.Maintain pass.
type MaintainOptions struct {
	// Batch is the most rows one transaction deletes: Maintain lets other
	// writers in between batches. Zero or negative uses defaultBatch.
	Batch int
}

// MaintainResult summarizes what Store.Maintain pruned and checkpointed.
type MaintainResult struct {
	PrunedEffects   int64
	WALBusy         int
	WALLog          int
	WALCheckpointed int
}

// Maintain prunes completed outbox rows and runs a passive WAL checkpoint so
// long-lived databases stay bounded. It keeps every command receipt: one
// costs less than the events its command wrote, which stay, and a client
// may retry under a receipt's ID at any time.
func (s *Store) Maintain(ctx context.Context, opts MaintainOptions) (MaintainResult, error) {
	batch := opts.Batch
	if batch <= 0 {
		batch = defaultBatch
	}

	var res MaintainResult
	for {
		deleted, err := s.pruneEffects(ctx, batch)
		res.PrunedEffects += deleted
		if err != nil {
			return res, err
		}
		if deleted < int64(batch) {
			break
		}
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&res.WALBusy, &res.WALLog, &res.WALCheckpointed); err != nil {
		return res, err
	}
	return res, nil
}

// pruneEffects deletes up to batch done effects in one transaction.
func (s *Store) pruneEffects(ctx context.Context, batch int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.db.ExecContext(ctx, `DELETE FROM effects WHERE id IN (SELECT id FROM effects WHERE state = 'done' ORDER BY id LIMIT ?)`, batch)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
