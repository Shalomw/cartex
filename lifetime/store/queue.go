package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/lifetime"
)

const workItemColumns = `
item_id, queue_kind, item_ref, ready_predicate_version, state, priority,
claimed_by_session, claim_id, lease_expires_at, attempts, dedup_key, reason,
lineage_hash, created_at, updated_at`

func scanWorkItem(sc interface{ Scan(...any) error }) (lifetime.WorkItem, error) {
	var (
		it        lifetime.WorkItem
		queue     string
		state     string
		lease     sql.NullTime
		createdAt sql.NullTime
		updatedAt sql.NullTime
	)
	err := sc.Scan(
		&it.ItemID, &queue, &it.ItemRef, &it.ReadyPredicateVersion, &state, &it.Priority,
		&it.ClaimedBySession, &it.ClaimID, &lease, &it.Attempts, &it.DedupKey, &it.Reason,
		&it.LineageHash, &createdAt, &updatedAt,
	)
	if err != nil {
		return it, err
	}
	it.QueueKind = lifetime.QueueKind(queue)
	it.State = lifetime.WorkItemState(state)
	it.LeaseExpiresAt = scanNullTime(lease)
	it.CreatedAt = scanNullTime(createdAt)
	it.UpdatedAt = scanNullTime(updatedAt)
	return it, nil
}

// ProjectWorkItem 按 dedup_key 幂等投影一个 ready 工作项（§6.3/R-6.9）。
//
// 同一 (task, queue_kind, dedup_key) 已存在活跃项时返回既有 item_id，
// 不会重置其 state/claim。dedup_key 为空返回错误（平台必须提供稳定去重键）。
func (s *Store) ProjectWorkItem(ctx context.Context, taskID string, it lifetime.WorkItem) (string, error) {
	if taskID == "" {
		return "", errors.New("lifetime/store: ProjectWorkItem: empty task_id")
	}
	return projectWorkItem(ctx, s.db, taskID, it)
}

// projectWorkItem 是 ProjectWorkItem 的事务内版本。
func projectWorkItem(ctx context.Context, q querier, taskID string, it lifetime.WorkItem) (string, error) {
	if !it.QueueKind.Valid() {
		return "", fmt.Errorf("lifetime/store: project work item: invalid queue_kind %q", it.QueueKind)
	}
	if it.ItemRef == "" {
		return "", errors.New("lifetime/store: project work item: empty item_ref")
	}
	if it.DedupKey == "" {
		return "", errors.New("lifetime/store: project work item: empty dedup_key")
	}
	if it.ItemID == "" {
		it.ItemID = lifetime.NewID("wi")
	}
	if it.ReadyPredicateVersion == "" {
		it.ReadyPredicateVersion = "v1"
	}
	now := nowUTC()

	// 冲突目标与 lifetime_work_queue_dedup_idx 的谓词一致。
	const insertQ = `
INSERT INTO lifetime_work_queue (
    item_id, task_id, queue_kind, item_ref, ready_predicate_version, state,
    priority, claimed_by_session, claim_id, lease_expires_at, attempts,
    dedup_key, reason, lineage_hash, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,'ready',$6,'','',NULL,$7,$8,$9,$10,$11,$11)
ON CONFLICT (task_id, queue_kind, dedup_key) WHERE state <> 'cancelled'
DO NOTHING
RETURNING item_id`
	var itemID string
	err := q.QueryRowContext(ctx, insertQ,
		it.ItemID, taskID, string(it.QueueKind), it.ItemRef, it.ReadyPredicateVersion,
		it.Priority, it.Attempts, it.DedupKey, it.Reason, it.LineageHash, now,
	).Scan(&itemID)
	if err == nil {
		return itemID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("lifetime/store: project work item: %w", err)
	}

	// 已存在活跃项：返回既有 id，保持其状态不变。
	const selQ = `
SELECT item_id FROM lifetime_work_queue
WHERE task_id=$1 AND queue_kind=$2 AND dedup_key=$3 AND state <> 'cancelled'
ORDER BY created_at ASC, item_id ASC
LIMIT 1`
	if err := q.QueryRowContext(ctx, selQ, taskID, string(it.QueueKind), it.DedupKey).Scan(&itemID); err != nil {
		return "", fmt.Errorf("lifetime/store: project work item: lookup existing: %w", err)
	}
	return itemID, nil
}

// ListReady 返回可 claim 的 ready 项。
//
// SQL 先按 (priority DESC, created_at ASC, hash(item_id, seed)) 取 limit 条；
// hash 并列打破使用 profile.scheduler_seed（R-6.43/I-10），不再依赖 item_id
// 物理排序。Go 侧按同一 key 再排一次，保证语义与 SQL 一致。
func (s *Store) ListReady(ctx context.Context, taskID string, q lifetime.QueueKind, limit int) ([]lifetime.WorkItem, error) {
	if !q.Valid() {
		return nil, fmt.Errorf("lifetime/store: ListReady: invalid queue_kind %q", q)
	}
	if limit <= 0 {
		limit = 100
	}
	seed, _ := schedulerSeed(ctx, s.db, taskID)
	seedArg := strconv.FormatInt(seed, 10)
	const sqlQ = `SELECT ` + workItemColumns + `
FROM lifetime_work_queue
WHERE task_id = $1 AND queue_kind = $2 AND state = 'ready'
ORDER BY priority DESC, created_at ASC, md5(item_id || $4) ASC
LIMIT $3`
	rows, err := s.db.QueryContext(ctx, sqlQ, taskID, string(q), limit, seedArg)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: ListReady: %w", err)
	}
	defer rows.Close()

	var out []lifetime.WorkItem
	for rows.Next() {
		it, err := scanWorkItem(rows)
		if err != nil {
			return nil, fmt.Errorf("lifetime/store: ListReady: scan: %w", err)
		}
		out = append(out, it)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return lifetime.SortKeyHash(a.ItemID, seed) < lifetime.SortKeyHash(b.ItemID, seed)
	})
	return out, nil
}

// Claim 原子领取一个 ready 项（R-6.4/R-6.10）。
//
// 单条 UPDATE ... WHERE item_id=(SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1)
// 保证并发 claim 只有一个成功；没有可领项时返回 (nil, nil)。
func (s *Store) Claim(ctx context.Context, taskID string, q lifetime.QueueKind, sessionID string, ttl time.Duration) (*lifetime.WorkItem, error) {
	if taskID == "" || sessionID == "" {
		return nil, errors.New("lifetime/store: Claim: empty task_id or session_id")
	}
	if !q.Valid() {
		return nil, fmt.Errorf("lifetime/store: Claim: invalid queue_kind %q", q)
	}
	if ttl <= 0 {
		return nil, errors.New("lifetime/store: Claim: non-positive lease ttl")
	}
	claimID := lifetime.NewID("claim")
	var claimed *lifetime.WorkItem

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// R-6.43/I-10：并列用 hash(item_id, scheduler_seed)，种子来自
		// 该 task 的 TaskProfile；同一次 claim 的日志使用同一种子。
		seed, _ := schedulerSeed(ctx, tx, taskID)
		seedArg := strconv.FormatInt(seed, 10)
		const updateQ = `
WITH candidate AS (
    SELECT item_id
    FROM lifetime_work_queue
    WHERE task_id = $1 AND queue_kind = $2 AND state = 'ready'
    ORDER BY priority DESC, created_at ASC, md5(item_id || $6) ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE lifetime_work_queue AS w
SET state = 'claimed',
    claimed_by_session = $3,
    claim_id = $4,
    lease_expires_at = now() + make_interval(secs => $5::double precision),
    updated_at = now()
FROM candidate c
WHERE w.item_id = c.item_id
RETURNING w.item_id, w.queue_kind, w.item_ref, w.ready_predicate_version, w.state,
          w.priority, w.claimed_by_session, w.claim_id, w.lease_expires_at,
          w.attempts, w.dedup_key, w.reason, w.lineage_hash, w.created_at, w.updated_at`
		it, err := scanWorkItem(tx.QueryRowContext(ctx, updateQ, taskID, string(q), sessionID, claimID, ttl.Seconds(), seedArg))
		if errors.Is(err, sql.ErrNoRows) {
			return nil // 队列为空
		}
		if err != nil {
			return fmt.Errorf("lifetime/store: Claim: %w", err)
		}
		claimed = &it
		return appendClaimLog(ctx, tx, taskID, claimID, it.ItemID, sessionID, "claim", it.Priority, seed, "", "")
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// Renew 续租；claim/session 不匹配或已过期返回 ErrLeaseLost（§6.5/R-6.15）。
func (s *Store) Renew(ctx context.Context, itemID, claimID, sessionID string, ttl time.Duration) (*lifetime.WorkItem, error) {
	if itemID == "" || claimID == "" || sessionID == "" {
		return nil, errors.New("lifetime/store: Renew: empty item_id/claim_id/session_id")
	}
	if ttl <= 0 {
		return nil, errors.New("lifetime/store: Renew: non-positive lease ttl")
	}
	var renewed *lifetime.WorkItem
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		const q = `
UPDATE lifetime_work_queue
SET lease_expires_at = now() + make_interval(secs => $4::double precision),
    updated_at = now()
WHERE item_id = $1 AND claim_id = $2 AND claimed_by_session = $3
  AND state = 'claimed' AND lease_expires_at > now()
RETURNING ` + workItemColumns
		it, err := scanWorkItem(tx.QueryRowContext(ctx, q, itemID, claimID, sessionID, ttl.Seconds()))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLeaseLost
		}
		if err != nil {
			return fmt.Errorf("lifetime/store: Renew: %w", err)
		}
		renewed = &it
		taskID := taskIDOfItem(ctx, tx, itemID)
		seed, _ := schedulerSeed(ctx, tx, taskID)
		return appendClaimLog(ctx, tx, taskID, claimID, itemID, sessionID, "renew", it.Priority, seed, "", "")
	})
	if err != nil {
		return nil, err
	}
	return renewed, nil
}

// Release 立即把工作项放回 ready；attempts 不变（R-6.14）。
func (s *Store) Release(ctx context.Context, itemID, claimID, sessionID, reason string) error {
	if itemID == "" || claimID == "" || sessionID == "" {
		return errors.New("lifetime/store: Release: empty item_id/claim_id/session_id")
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		const q = `
UPDATE lifetime_work_queue
SET state = 'ready',
    claimed_by_session = '',
    claim_id = '',
    lease_expires_at = NULL,
    updated_at = now()
WHERE item_id = $1 AND claim_id = $2 AND claimed_by_session = $3 AND state = 'claimed'
RETURNING priority, task_id`
		var (
			priority float64
			taskID   string
		)
		err := tx.QueryRowContext(ctx, q, itemID, claimID, sessionID).Scan(&priority, &taskID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLeaseLost
		}
		if err != nil {
			return fmt.Errorf("lifetime/store: Release: %w", err)
		}
		seed, _ := schedulerSeed(ctx, tx, taskID)
		return appendClaimLog(ctx, tx, taskID, claimID, itemID, sessionID, "release", priority, seed, reason, "")
	})
}

// Complete 把工作项置为 done；同一 claim 重复调用幂等返回 nil（R-6.13）。
func (s *Store) Complete(ctx context.Context, itemID, claimID, sessionID, outcomeRef string) error {
	if itemID == "" || claimID == "" {
		return errors.New("lifetime/store: Complete: empty item_id/claim_id")
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		const selQ = `
SELECT task_id, state, claim_id, claimed_by_session, priority
FROM lifetime_work_queue WHERE item_id = $1 FOR UPDATE`
		var (
			taskID   string
			state    string
			curClaim string
			curSess  string
			priority float64
		)
		err := tx.QueryRowContext(ctx, selQ, itemID).Scan(&taskID, &state, &curClaim, &curSess, &priority)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lifetime/store: Complete: read: %w", err)
		}

		if state == string(lifetime.WorkItemDone) {
			// 幂等：同一 claim 的 complete 已记录则成功返回。
			const doneQ = `
SELECT 1 FROM lifetime_claim_log
WHERE item_id=$1 AND claim_id=$2 AND action='complete' LIMIT 1`
			var one int
			err := tx.QueryRowContext(ctx, doneQ, itemID, claimID).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrLeaseLost
			}
			if err != nil {
				return fmt.Errorf("lifetime/store: Complete: idempotency: %w", err)
			}
			return nil
		}
		if state != string(lifetime.WorkItemClaimed) || curClaim != claimID ||
			(sessionID != "" && curSess != sessionID) {
			return ErrLeaseLost
		}

		const updQ = `
UPDATE lifetime_work_queue
SET state = 'done', claimed_by_session = '', claim_id = '',
    lease_expires_at = NULL, updated_at = now()
WHERE item_id = $1`
		if _, err := tx.ExecContext(ctx, updQ, itemID); err != nil {
			return fmt.Errorf("lifetime/store: Complete: update: %w", err)
		}
		seed, _ := schedulerSeed(ctx, tx, taskID)
		return appendClaimLog(ctx, tx, taskID, claimID, itemID, sessionID, "complete", priority, seed, "", outcomeRef)
	})
}

// ExpireLeases 把过期的 claimed 项按 lifetime.ApplyExpire 处理（R-6.12）：
// attempts++ 后回 ready；attempts>=3 → blocked。写 claim_log(action=expire)。
func (s *Store) ExpireLeases(ctx context.Context, taskID string) (int, error) {
	if taskID == "" {
		return 0, errors.New("lifetime/store: ExpireLeases: empty task_id")
	}
	expiredCount := 0
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		const selQ = `SELECT ` + workItemColumns + `
FROM lifetime_work_queue
WHERE task_id = $1 AND state = 'claimed'
  AND lease_expires_at IS NOT NULL AND lease_expires_at <= now()
ORDER BY item_id
FOR UPDATE SKIP LOCKED`
		rows, err := tx.QueryContext(ctx, selQ, taskID)
		if err != nil {
			return fmt.Errorf("lifetime/store: ExpireLeases: select: %w", err)
		}
		var expiredItems []lifetime.WorkItem
		for rows.Next() {
			it, err := scanWorkItem(rows)
			if err != nil {
				_ = rows.Close()
				return fmt.Errorf("lifetime/store: ExpireLeases: scan: %w", err)
			}
			expiredItems = append(expiredItems, it)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("lifetime/store: ExpireLeases: close: %w", err)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("lifetime/store: ExpireLeases: rows: %w", err)
		}

		now := nowUTC()
		seed, _ := schedulerSeed(ctx, tx, taskID)
		for _, old := range expiredItems {
			next, expired := lifetime.ApplyExpire(old, now)
			if !expired {
				continue
			}
			const updQ = `
UPDATE lifetime_work_queue
SET state = $2, claimed_by_session = '', claim_id = '', lease_expires_at = NULL,
    attempts = $3, updated_at = now()
WHERE item_id = $1`
			if _, err := tx.ExecContext(ctx, updQ, next.ItemID, string(next.State), next.Attempts); err != nil {
				return fmt.Errorf("lifetime/store: ExpireLeases: update %s: %w", next.ItemID, err)
			}
			if err := appendClaimLog(ctx, tx, taskID, old.ClaimID, old.ItemID,
				old.ClaimedBySession, "expire", old.Priority, seed, "", ""); err != nil {
				return err
			}
			expiredCount++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return expiredCount, nil
}

// ClaimLog 返回 task 最近的调度日志（seq 倒序）。
func (s *Store) ClaimLog(ctx context.Context, taskID string, limit int) ([]lifetime.ClaimLog, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `
SELECT seq, claim_id, item_id, session_id, action, priority, scheduler_seed, timestamp
FROM lifetime_claim_log
WHERE task_id = $1
ORDER BY seq DESC
LIMIT $2`
	rows, err := s.db.QueryContext(ctx, q, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: ClaimLog: %w", err)
	}
	defer rows.Close()

	var out []lifetime.ClaimLog
	for rows.Next() {
		var (
			l         lifetime.ClaimLog
			timestamp sql.NullTime
		)
		if err := rows.Scan(&l.Seq, &l.ClaimID, &l.ItemID, &l.SessionID, &l.Action,
			&l.Priority, &l.SchedulerSeed, &timestamp); err != nil {
			return nil, fmt.Errorf("lifetime/store: ClaimLog: scan: %w", err)
		}
		l.Timestamp = scanNullTime(timestamp)
		out = append(out, l)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// schedulerSeed 读取 task 最新 profile 的 scheduler_seed（R-6.6/R-6.45）。
// 无 profile 时返回 0；claim_log 始终记录当时可见的值。
func schedulerSeed(ctx context.Context, q querier, taskID string) (int64, error) {
	const sqlQ = `
SELECT COALESCE((payload->>'scheduler_seed')::bigint, 0)
FROM lifetime_task_profiles
WHERE task_id = $1
ORDER BY created_at DESC
LIMIT 1`
	var seed int64
	err := q.QueryRowContext(ctx, sqlQ, taskID).Scan(&seed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return seed, nil
}

// taskIDOfItem 查询工作项所属 task（Renew 的日志路径需要）。
func taskIDOfItem(ctx context.Context, q querier, itemID string) string {
	const sqlQ = `SELECT task_id FROM lifetime_work_queue WHERE item_id = $1`
	var taskID string
	if err := q.QueryRowContext(ctx, sqlQ, itemID).Scan(&taskID); err != nil {
		return ""
	}
	return taskID
}

// appendClaimLog 写调度审计（R-6.42）。每次 claim/renew/release/complete/expire 必写。
func appendClaimLog(ctx context.Context, q querier, taskID, claimID, itemID, sessionID, action string, priority float64, seed int64, reason, outcomeRef string) error {
	// taskID 理论上不应为空；为空时仍写入（审计优先），但不能用于按 task 清理。
	const sqlQ = `
INSERT INTO lifetime_claim_log
    (task_id, claim_id, item_id, session_id, action, priority, scheduler_seed, reason, outcome_ref)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`
	if _, err := q.ExecContext(ctx, sqlQ, taskID, claimID, itemID, sessionID, action, priority, seed, reason, outcomeRef); err != nil {
		return fmt.Errorf("lifetime/store: claim_log %s: %w", action, err)
	}
	return nil
}
