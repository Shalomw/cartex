package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Autumn-27/artex/lifetime"
)

// AcquireArea 原子获取/续租一个 area（R-6.33/R-6.34）。
//
// 已有未过期 owner 且 session 不同 → ErrAreaOwned；同一 session 可续租；
// 旧 lease 已过期时允许接管。
func (s *Store) AcquireArea(ctx context.Context, areaID, sessionID string, ttl time.Duration) error {
	if areaID == "" || sessionID == "" {
		return errors.New("lifetime/store: AcquireArea: empty area_id or session_id")
	}
	if ttl <= 0 {
		return errors.New("lifetime/store: AcquireArea: non-positive ttl")
	}
	const q = `
INSERT INTO lifetime_area_leases (area_id, owner_session, leased_at, expires_at)
VALUES ($1, $2, now(), now() + make_interval(secs => $3::double precision))
ON CONFLICT (area_id) DO UPDATE SET
    owner_session = EXCLUDED.owner_session,
    leased_at = EXCLUDED.leased_at,
    expires_at = EXCLUDED.expires_at
WHERE lifetime_area_leases.owner_session = EXCLUDED.owner_session
   OR lifetime_area_leases.expires_at <= now()
RETURNING area_id`
	var got string
	err := s.db.QueryRowContext(ctx, q, areaID, sessionID, ttl.Seconds()).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAreaOwned
	}
	if err != nil {
		return fmt.Errorf("lifetime/store: AcquireArea: %w", err)
	}
	return nil
}

// ReleaseArea 释放本 session 持有的 area。
func (s *Store) ReleaseArea(ctx context.Context, areaID, sessionID string) error {
	if areaID == "" || sessionID == "" {
		return errors.New("lifetime/store: ReleaseArea: empty area_id or session_id")
	}
	const q = `DELETE FROM lifetime_area_leases WHERE area_id = $1 AND owner_session = $2`
	res, err := s.db.ExecContext(ctx, q, areaID, sessionID)
	if err != nil {
		return fmt.Errorf("lifetime/store: ReleaseArea: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("lifetime/store: ReleaseArea: %w", err)
	}
	if n == 0 {
		return ErrLeaseLost
	}
	return nil
}

// AppendJournal 追加一条 owner 记忆（append-only，R-6.37）。
//
// areaID 参数优先于 entry.AreaID；entry_id 为空时生成。
func (s *Store) AppendJournal(ctx context.Context, taskID, areaID string, e lifetime.OwnerJournalEntry) (string, error) {
	if taskID == "" || areaID == "" {
		return "", errors.New("lifetime/store: AppendJournal: empty task_id or area_id")
	}
	if !e.Kind.Valid() {
		return "", fmt.Errorf("lifetime/store: AppendJournal: invalid kind %q", e.Kind)
	}
	if e.Text == "" {
		return "", errors.New("lifetime/store: AppendJournal: empty text")
	}
	if e.Confidence == "" {
		e.Confidence = lifetime.ConfidenceUnknown
	}
	if !e.Confidence.Valid() {
		return "", fmt.Errorf("lifetime/store: AppendJournal: invalid confidence %q", e.Confidence)
	}
	if e.EntryID == "" {
		e.EntryID = lifetime.NewID("jrn")
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = nowUTC()
	}
	refs, err := marshalJSON(e.Refs)
	if err != nil {
		return "", err
	}
	e.AreaID = areaID

	const q = `
INSERT INTO lifetime_owner_journal (
    entry_id, task_id, area_id, owner_id, kind, text, refs, confidence,
    source_hash, index_hash, stale, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`
	if _, err := s.db.ExecContext(ctx, q,
		e.EntryID, taskID, areaID, e.OwnerID, string(e.Kind), e.Text, refs,
		string(e.Confidence), e.SourceHash, e.IndexHash, e.Stale, e.CreatedAt,
	); err != nil {
		return "", fmt.Errorf("lifetime/store: AppendJournal: %w", err)
	}
	return e.EntryID, nil
}

// ListJournal 返回 area 的 journal；includeStale=false 时只返回未 stale 条目
// （R-6.39：默认检索未 stale entry）。
func (s *Store) ListJournal(ctx context.Context, areaID string, includeStale bool) ([]lifetime.OwnerJournalEntry, error) {
	if areaID == "" {
		return nil, errors.New("lifetime/store: ListJournal: empty area_id")
	}
	const q = `
SELECT entry_id, owner_id, area_id, kind, text, refs, confidence,
       source_hash, index_hash, stale, created_at
FROM lifetime_owner_journal
WHERE area_id = $1 AND ($2 OR stale = false)
ORDER BY created_at ASC, entry_id ASC`
	rows, err := s.db.QueryContext(ctx, q, areaID, includeStale)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: ListJournal: %w", err)
	}
	defer rows.Close()

	var out []lifetime.OwnerJournalEntry
	for rows.Next() {
		var (
			e          lifetime.OwnerJournalEntry
			kind       string
			refs       []byte
			confidence string
			createdAt  sql.NullTime
		)
		if err := rows.Scan(&e.EntryID, &e.OwnerID, &e.AreaID, &kind, &e.Text, &refs,
			&confidence, &e.SourceHash, &e.IndexHash, &e.Stale, &createdAt); err != nil {
			return nil, fmt.Errorf("lifetime/store: ListJournal: scan: %w", err)
		}
		e.Kind = lifetime.OwnerJournalKind(kind)
		e.Confidence = lifetime.ConfidenceLevel(confidence)
		e.CreatedAt = scanNullTime(createdAt)
		if len(refs) > 0 {
			if err := json.Unmarshal(refs, &e.Refs); err != nil {
				return nil, fmt.Errorf("lifetime/store: ListJournal: refs: %w", err)
			}
		}
		out = append(out, e)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

// MarkJournalStale 把与给定 source_hash/index_hash 不一致的未 stale 条目标 stale
// （R-6.38：lineage 变化 → 旧推导失效，不删除历史）。
func (s *Store) MarkJournalStale(ctx context.Context, areaID, sourceHash, indexHash string) (int, error) {
	if areaID == "" {
		return 0, errors.New("lifetime/store: MarkJournalStale: empty area_id")
	}
	const q = `
UPDATE lifetime_owner_journal
SET stale = true
WHERE area_id = $1 AND stale = false
  AND (source_hash <> $2 OR index_hash <> $3)`
	res, err := s.db.ExecContext(ctx, q, areaID, sourceHash, indexHash)
	if err != nil {
		return 0, fmt.Errorf("lifetime/store: MarkJournalStale: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("lifetime/store: MarkJournalStale: %w", err)
	}
	return int(n), nil
}
