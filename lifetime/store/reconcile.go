package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Autumn-27/artex/lifetime"
)

// SnapshotForReconcile 装载对账环的只读快照（§6.10）。
//
// 说明：
//   - LineageID 取该 task 最新 create 的 lineage（当前 lineage）；
//   - IndexAvailable = task 是否有任何 family；
//   - FindingExists 返回空 map：本 schema 不拥有 findings 表（由 report
//     service 维护），C9 的 finding 一致性由 report service 侧补充；
//   - BudgetExhausted 无 task 级预算表，默认 false（预算只存在于
//     candidate.budget 与 planner policy，由 platform 判定）。
func (s *Store) SnapshotForReconcile(ctx context.Context, taskID string) (*lifetime.ReconcileSnapshot, error) {
	if taskID == "" {
		return nil, errors.New("lifetime/store: SnapshotForReconcile: empty task_id")
	}
	snap := &lifetime.ReconcileSnapshot{
		TaskID:        taskID,
		Now:           nowUTC(),
		FindingExists: map[string]bool{},
	}

	// 当前 lineage。
	const lineageQ = `
SELECT lineage_id FROM lifetime_lineages
WHERE task_id = $1 ORDER BY created_at DESC, lineage_id DESC LIMIT 1`
	if err := s.db.QueryRowContext(ctx, lineageQ, taskID).Scan(&snap.LineageID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("lifetime/store: SnapshotForReconcile: lineage: %w", err)
	}

	var err error
	if snap.EnumeratedUnits, err = listCoverageTx(ctx, s.db, taskID); err != nil {
		return nil, err
	}
	if snap.Candidates, err = s.ListCandidates(ctx, taskID); err != nil {
		return nil, err
	}
	if snap.Assessments, err = s.ListAssessments(ctx, taskID); err != nil {
		return nil, err
	}
	if snap.Experiments, err = s.listExperimentsByTask(ctx, taskID); err != nil {
		return nil, err
	}
	if snap.Observations, err = s.listObservationsByTask(ctx, taskID); err != nil {
		return nil, err
	}
	if snap.NoRisks, err = s.listNoRisksByTask(ctx, taskID); err != nil {
		return nil, err
	}
	if snap.WorkItems, err = s.listWorkItemsByTask(ctx, taskID); err != nil {
		return nil, err
	}
	if snap.OwnerJournal, err = s.listJournalByTask(ctx, taskID); err != nil {
		return nil, err
	}
	if snap.ExistingDedupKeys, err = s.listOpenDedupKeys(ctx, taskID); err != nil {
		return nil, err
	}

	const existsQ = `SELECT EXISTS (SELECT 1 FROM lifetime_object_families WHERE task_id = $1)`
	if err := s.db.QueryRowContext(ctx, existsQ, taskID).Scan(&snap.IndexAvailable); err != nil {
		return nil, fmt.Errorf("lifetime/store: SnapshotForReconcile: index availability: %w", err)
	}
	snap.BudgetExhausted = false

	return snap, nil
}

func (s *Store) listExperimentsByTask(ctx context.Context, taskID string) ([]lifetime.Experiment, error) {
	q := `SELECT ` + experimentColumns + `
FROM lifetime_experiments WHERE task_id = $1
ORDER BY created_at, experiment_id`
	rows, err := s.db.QueryContext(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: listExperimentsByTask: %w", err)
	}
	defer rows.Close()

	var out []lifetime.Experiment
	for rows.Next() {
		e, err := scanExperiment(rows)
		if err != nil {
			return nil, fmt.Errorf("lifetime/store: listExperimentsByTask: scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) listObservationsByTask(ctx context.Context, taskID string) ([]lifetime.Observation, error) {
	const q = `
SELECT observation_id, experiment_id, candidate_id, payload, created_at
FROM lifetime_observations
WHERE task_id = $1
ORDER BY created_at, observation_id`
	rows, err := s.db.QueryContext(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: listObservationsByTask: %w", err)
	}
	defer rows.Close()

	var out []lifetime.Observation
	for rows.Next() {
		var (
			o           lifetime.Observation
			observation string
			experiment  string
			candidate   string
			payload     []byte
			createdAt   sql.NullTime
		)
		if err := rows.Scan(&observation, &experiment, &candidate, &payload, &createdAt); err != nil {
			return nil, fmt.Errorf("lifetime/store: listObservationsByTask: scan: %w", err)
		}
		if err := json.Unmarshal(payload, &o); err != nil {
			return nil, fmt.Errorf("lifetime/store: listObservationsByTask: payload: %w", err)
		}
		o.ObservationID = observation
		o.ExperimentID = experiment
		o.CandidateID = candidate
		o.CreatedAt = scanNullTime(createdAt)
		out = append(out, o)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) listNoRisksByTask(ctx context.Context, taskID string) ([]lifetime.NoRisk, error) {
	const q = `
SELECT no_risk_id, family_id, domain_pair_id, form, arguments,
       covers_current_form, review_status, reviewer_id, created_by, created_at
FROM lifetime_no_risk
WHERE task_id = $1
ORDER BY created_at, no_risk_id`
	rows, err := s.db.QueryContext(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: listNoRisksByTask: %w", err)
	}
	defer rows.Close()

	var out []lifetime.NoRisk
	for rows.Next() {
		var (
			n         lifetime.NoRisk
			form      string
			arguments []byte
			createdAt sql.NullTime
		)
		if err := rows.Scan(&n.NoRiskID, &n.FamilyID, &n.DomainPairID, &form, &arguments,
			&n.CoversCurrentForm, &n.ReviewStatus, &n.ReviewerID, &n.CreatedBy, &createdAt); err != nil {
			return nil, fmt.Errorf("lifetime/store: listNoRisksByTask: scan: %w", err)
		}
		n.Form = lifetime.Form(form)
		n.CreatedAt = scanNullTime(createdAt)
		if len(arguments) > 0 {
			if err := json.Unmarshal(arguments, &n.Arguments); err != nil {
				return nil, fmt.Errorf("lifetime/store: listNoRisksByTask: arguments: %w", err)
			}
		}
		out = append(out, n)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) listWorkItemsByTask(ctx context.Context, taskID string) ([]lifetime.WorkItem, error) {
	q := `SELECT ` + workItemColumns + `
FROM lifetime_work_queue WHERE task_id = $1
ORDER BY created_at, item_id`
	rows, err := s.db.QueryContext(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: listWorkItemsByTask: %w", err)
	}
	defer rows.Close()

	var out []lifetime.WorkItem
	for rows.Next() {
		it, err := scanWorkItem(rows)
		if err != nil {
			return nil, fmt.Errorf("lifetime/store: listWorkItemsByTask: scan: %w", err)
		}
		out = append(out, it)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) listJournalByTask(ctx context.Context, taskID string) ([]lifetime.OwnerJournalEntry, error) {
	const q = `
SELECT entry_id, owner_id, area_id, kind, text, refs, confidence,
       source_hash, index_hash, stale, created_at
FROM lifetime_owner_journal
WHERE task_id = $1
ORDER BY created_at, entry_id`
	rows, err := s.db.QueryContext(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: listJournalByTask: %w", err)
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
			return nil, fmt.Errorf("lifetime/store: listJournalByTask: scan: %w", err)
		}
		e.Kind = lifetime.OwnerJournalKind(kind)
		e.Confidence = lifetime.ConfidenceLevel(confidence)
		e.CreatedAt = scanNullTime(createdAt)
		if len(refs) > 0 {
			if err := json.Unmarshal(refs, &e.Refs); err != nil {
				return nil, fmt.Errorf("lifetime/store: listJournalByTask: refs: %w", err)
			}
		}
		out = append(out, e)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) listOpenDedupKeys(ctx context.Context, taskID string) ([]string, error) {
	const q = `
SELECT dedup_key FROM lifetime_reconciliation_findings
WHERE task_id = $1 AND status = 'open' AND dedup_key <> ''
ORDER BY dedup_key`
	rows, err := s.db.QueryContext(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: listOpenDedupKeys: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("lifetime/store: listOpenDedupKeys: scan: %w", err)
		}
		out = append(out, key)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

// SaveReconciliation 写入一次 sweep 统计与发现（R-6.31/R-6.29）。
//
// runs 按 sweep_id upsert；findings 先检查同 task 的 open dedup_key，
// 已存在则跳过，避免重复 repair task。
func (s *Store) SaveReconciliation(ctx context.Context, taskID string, run lifetime.ReconciliationRun, findings []lifetime.ReconciliationFinding) error {
	if taskID == "" {
		return errors.New("lifetime/store: SaveReconciliation: empty task_id")
	}
	if run.SweepID == "" {
		return errors.New("lifetime/store: SaveReconciliation: empty sweep_id")
	}

	return s.withTx(ctx, func(tx *sql.Tx) error {
		startedAt := run.StartedAt
		if startedAt.IsZero() {
			startedAt = nowUTC()
		}
		const runQ = `
INSERT INTO lifetime_reconciliation_runs
    (sweep_id, task_id, mode, checks, findings, repairs, started_at, finished_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (sweep_id) DO UPDATE SET
    mode = EXCLUDED.mode,
    checks = EXCLUDED.checks,
    findings = EXCLUDED.findings,
    repairs = EXCLUDED.repairs,
    started_at = EXCLUDED.started_at,
    finished_at = EXCLUDED.finished_at`
		if _, err := tx.ExecContext(ctx, runQ,
			run.SweepID, taskID, run.Mode, run.Checks, run.Findings, run.Repairs,
			startedAt, nullTime(run.FinishedAt),
		); err != nil {
			return fmt.Errorf("lifetime/store: SaveReconciliation: run: %w", err)
		}

		for i := range findings {
			f := findings[i]
			if f.Severity != "" && !f.Severity.Valid() {
				return fmt.Errorf("lifetime/store: SaveReconciliation: finding %d invalid severity %q", i, f.Severity)
			}
			if f.Action != "" && !f.Action.Valid() {
				return fmt.Errorf("lifetime/store: SaveReconciliation: finding %d invalid action %q", i, f.Action)
			}
			if f.Status == "" {
				f.Status = lifetime.ReconOpen
			}
			if !f.Status.Valid() {
				return fmt.Errorf("lifetime/store: SaveReconciliation: finding %d invalid status %q", i, f.Status)
			}
			f.ReconID = orNewID(f.ReconID, "recon")
			if f.CreatedAt.IsZero() {
				f.CreatedAt = nowUTC()
			}
			if f.SweepID == "" {
				f.SweepID = run.SweepID
			}

			// R-6.29：同一 open dedup_key 不重复生成。
			if f.DedupKey != "" {
				const dupQ = `
SELECT 1 FROM lifetime_reconciliation_findings
WHERE task_id = $1 AND dedup_key = $2 AND status = 'open'
LIMIT 1`
				var one int
				err := tx.QueryRowContext(ctx, dupQ, taskID, f.DedupKey).Scan(&one)
				if err == nil {
					continue
				}
				if !errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("lifetime/store: SaveReconciliation: dedup: %w", err)
				}
			}

			// 无目标 ON CONFLICT：同时吞掉 recon_id 重复与并发写入的
			// open dedup_key 唯一冲突（R-6.29）。
			const insQ = `
INSERT INTO lifetime_reconciliation_findings (
    recon_id, task_id, sweep_id, check_id, severity, subject_kind, subject_ref,
    expected, actual, action, status, dedup_key, created_at, resolved_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT DO NOTHING`
			if _, err := tx.ExecContext(ctx, insQ,
				f.ReconID, taskID, f.SweepID, f.CheckID, string(f.Severity),
				f.SubjectKind, f.SubjectRef, f.Expected, f.Actual, string(f.Action),
				string(f.Status), f.DedupKey, f.CreatedAt, nullTime(f.ResolvedAt),
			); err != nil {
				return fmt.Errorf("lifetime/store: SaveReconciliation: finding %s: %w", f.ReconID, err)
			}
		}
		return nil
	})
}

// ListReconciliationFindings 返回 task 的对账发现；status 为空表示全部。
func (s *Store) ListReconciliationFindings(ctx context.Context, taskID string, status string) ([]lifetime.ReconciliationFinding, error) {
	if taskID == "" {
		return nil, errors.New("lifetime/store: ListReconciliationFindings: empty task_id")
	}
	if status != "" && !lifetime.ReconciliationStatus(status).Valid() {
		return nil, fmt.Errorf("lifetime/store: ListReconciliationFindings: invalid status %q", status)
	}
	const q = `
SELECT recon_id, sweep_id, check_id, severity, subject_kind, subject_ref,
       expected, actual, action, status, dedup_key, created_at, resolved_at
FROM lifetime_reconciliation_findings
WHERE task_id = $1 AND ($2 = '' OR status = $2)
ORDER BY created_at DESC, recon_id DESC`
	rows, err := s.db.QueryContext(ctx, q, taskID, status)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: ListReconciliationFindings: %w", err)
	}
	defer rows.Close()

	var out []lifetime.ReconciliationFinding
	for rows.Next() {
		var (
			f          lifetime.ReconciliationFinding
			severity   string
			action     string
			state      string
			createdAt  sql.NullTime
			resolvedAt sql.NullTime
		)
		if err := rows.Scan(&f.ReconID, &f.SweepID, &f.CheckID, &severity,
			&f.SubjectKind, &f.SubjectRef, &f.Expected, &f.Actual, &action, &state,
			&f.DedupKey, &createdAt, &resolvedAt); err != nil {
			return nil, fmt.Errorf("lifetime/store: ListReconciliationFindings: scan: %w", err)
		}
		f.Severity = lifetime.ReconciliationSeverity(severity)
		f.Action = lifetime.ReconciliationAction(action)
		f.Status = lifetime.ReconciliationStatus(state)
		f.CreatedAt = scanNullTime(createdAt)
		f.ResolvedAt = scanNullTime(resolvedAt)
		out = append(out, f)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}
