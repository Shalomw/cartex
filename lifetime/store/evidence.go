package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Autumn-27/artex/lifetime"
)

// ---------------------------------------------------------------------------
// §3.2 Observation（append-only，平台写）
// ---------------------------------------------------------------------------

// AppendObservation 追加一条 Observation。主键冲突返回错误（append-only，
// 不覆盖既有 evidence 字段）。
func (s *Store) AppendObservation(ctx context.Context, taskID string, o lifetime.Observation) error {
	if taskID == "" {
		return errors.New("lifetime/store: AppendObservation: empty task_id")
	}
	if o.ObservationID == "" {
		return errors.New("lifetime/store: AppendObservation: empty observation_id")
	}
	payload, err := marshalJSON(o)
	if err != nil {
		return err
	}
	createdAt := o.CreatedAt
	if createdAt.IsZero() {
		createdAt = nowUTC()
	}
	const q = `
INSERT INTO lifetime_observations
    (observation_id, task_id, experiment_id, candidate_id, payload, created_at)
VALUES ($1,$2,$3,$4,$5,$6)`
	if _, err := s.db.ExecContext(ctx, q,
		o.ObservationID, taskID, o.ExperimentID, o.CandidateID, payload, createdAt,
	); err != nil {
		return fmt.Errorf("lifetime/store: AppendObservation %s: %w", o.ObservationID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// §3.3 Experiment（append-only，平台写）
// ---------------------------------------------------------------------------

// AppendExperiment 追加一条 Experiment。主键冲突返回错误。
func (s *Store) AppendExperiment(ctx context.Context, taskID string, e lifetime.Experiment) error {
	if taskID == "" {
		return errors.New("lifetime/store: AppendExperiment: empty task_id")
	}
	if e.ExperimentID == "" {
		return errors.New("lifetime/store: AppendExperiment: empty experiment_id")
	}
	payload, err := marshalJSON(e)
	if err != nil {
		return err
	}
	const q = `
INSERT INTO lifetime_experiments (
    experiment_id, task_id, candidate_id, lineage_id, lineage_role,
    confirmation_of, kind, state, payload, started_at, finished_at, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())`
	if _, err := s.db.ExecContext(ctx, q,
		e.ExperimentID, taskID, e.CandidateID, e.LineageID, string(e.LineageRole),
		e.ConfirmationOf, string(e.Kind), string(e.State), payload,
		nullTime(e.StartedAt), nullTime(e.FinishedAt),
	); err != nil {
		return fmt.Errorf("lifetime/store: AppendExperiment %s: %w", e.ExperimentID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// §3.4 Assessment（append-only；状态列允许平台单独更新）
// ---------------------------------------------------------------------------

// MaterializeAssessment 插入一条新的 Assessment（append-only，不 UPDATE
// evidence 字段）。同一 (candidate_id, lineage_id) 的新判定应插入新行，旧行
// 由调用方通过 MarkAssessmentSuperseded 标 superseded。
func (s *Store) MaterializeAssessment(ctx context.Context, taskID string, a lifetime.Assessment) error {
	if taskID == "" {
		return errors.New("lifetime/store: MaterializeAssessment: empty task_id")
	}
	if a.CandidateID == "" {
		return errors.New("lifetime/store: MaterializeAssessment: empty candidate_id")
	}
	if a.AssessmentID == "" {
		a.AssessmentID = lifetime.NewID("asm")
	}
	if a.LineageRole == "" {
		a.LineageRole = lifetime.LineageRoleClaim
	}
	if a.Status == "" {
		a.Status = lifetime.AssessmentOpen
	}
	if a.DerivedAt.IsZero() {
		a.DerivedAt = nowUTC()
	}
	payload, err := marshalJSON(a)
	if err != nil {
		return err
	}
	const q = `
INSERT INTO lifetime_assessments (
    assessment_id, task_id, candidate_id, candidate_key, lineage_id, lineage_role,
    confirmation_of, level, confidence, reportable_internal, reportable_external,
    status, stale, review_stale, payload, derived_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`
	// assessment 落库与队列投影同一事务：状态变化后由平台增量投影
	// Q2/Q3/Q4（R-6.9）。projectWorkItem 幂等，重复物化不会重复建项。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, q,
			a.AssessmentID, taskID, a.CandidateID, a.CandidateKey, a.LineageID, string(a.LineageRole),
			a.ConfirmationOf, string(a.Level), string(a.Confidence), a.ReportableInternal, a.ReportableExternal,
			string(a.Status), a.Stale, a.ReviewStale, payload, a.DerivedAt,
		); err != nil {
			return fmt.Errorf("lifetime/store: MaterializeAssessment %s: %w", a.AssessmentID, err)
		}
		return projectAssessmentQueue(ctx, tx, taskID, a)
	})
}

// projectAssessmentQueue 把 assessment 状态投影为下游队列项（§6.3/R-6.9）：
//   - claim lineage、open、S3/S4 → Q3（review）；
//   - claim lineage、open、S2 → Q2 mode=verify；
//   - claim lineage、open、S1/S1_provisional → Q2 mode=probe；
//   - review confirmed 且 reportable_external → Q4（report）。
//
// 队列项由平台派生，agent 不能直接写（R-6.2）。
func projectAssessmentQueue(ctx context.Context, q querier, taskID string, a lifetime.Assessment) error {
	var (
		kind    lifetime.QueueKind
		dedup   string
		reason  string
		itemRef = a.CandidateID
	)
	switch {
	case a.LineageRole == lifetime.LineageRoleClaim && !a.Stale && a.Status == lifetime.AssessmentOpen:
		switch {
		case a.Level == lifetime.LevelS3 || a.Level == lifetime.LevelS4:
			kind, dedup, reason = lifetime.QueueClaimReadyReview, "Q3:"+a.AssessmentID, "review"
			itemRef = a.AssessmentID
		case a.Level == lifetime.LevelS2:
			kind, dedup, reason = lifetime.QueueCandidateReadyVerify, "Q2:verify:"+a.CandidateID, "verify"
		case a.Level == lifetime.LevelS1 || a.Provisional:
			kind, dedup, reason = lifetime.QueueCandidateReadyVerify, "Q2:probe:"+a.CandidateID, "probe"
		}
	case a.Review.Verdict == lifetime.VerdictConfirmed && a.ReportableExternal:
		kind, dedup, reason = lifetime.QueueConfirmedReadyReport, "Q4:"+a.CandidateID, "report"
	}
	if kind == "" {
		return nil
	}
	_, err := projectWorkItem(ctx, q, taskID, lifetime.WorkItem{
		QueueKind: kind, ItemRef: itemRef, DedupKey: dedup, Reason: reason,
		LineageHash: a.LineageID,
	})
	if err != nil {
		return fmt.Errorf("lifetime/store: MaterializeAssessment %s: project %s: %w", a.AssessmentID, kind, err)
	}
	return nil
}

// MarkAssessmentSuperseded 把 assessment 标为 superseded（平台状态列）。
func (s *Store) MarkAssessmentSuperseded(ctx context.Context, assessmentID string) error {
	if assessmentID == "" {
		return errors.New("lifetime/store: MarkAssessmentSuperseded: empty assessment_id")
	}
	const q = `UPDATE lifetime_assessments SET status = 'superseded' WHERE assessment_id = $1`
	res, err := s.db.ExecContext(ctx, q, assessmentID)
	if err != nil {
		return fmt.Errorf("lifetime/store: MarkAssessmentSuperseded: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("lifetime/store: MarkAssessmentSuperseded: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkAssessmentStale 设置/清除 assessment 的 stale 状态列（平台状态）。
func (s *Store) MarkAssessmentStale(ctx context.Context, assessmentID string, stale bool) error {
	if assessmentID == "" {
		return errors.New("lifetime/store: MarkAssessmentStale: empty assessment_id")
	}
	const q = `UPDATE lifetime_assessments SET stale = $2 WHERE assessment_id = $1`
	res, err := s.db.ExecContext(ctx, q, assessmentID, stale)
	if err != nil {
		return fmt.Errorf("lifetime/store: MarkAssessmentStale: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("lifetime/store: MarkAssessmentStale: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const assessmentColumns = `
assessment_id, candidate_id, candidate_key, lineage_id, lineage_role,
confirmation_of, level, confidence, reportable_internal, reportable_external,
status, stale, review_stale, payload, derived_at`

func scanAssessment(sc interface{ Scan(...any) error }) (lifetime.Assessment, error) {
	var (
		a           lifetime.Assessment
		assessment  string
		candidate   string
		candidateK  string
		lineage     string
		role        string
		confirmOf   string
		level       string
		confidence  string
		internal    bool
		external    bool
		status      string
		stale       bool
		reviewStale bool
		payload     []byte
		derivedAt   sql.NullTime
	)
	err := sc.Scan(&assessment, &candidate, &candidateK, &lineage, &role,
		&confirmOf, &level, &confidence, &internal, &external,
		&status, &stale, &reviewStale, &payload, &derivedAt)
	if err != nil {
		return a, err
	}
	if err := json.Unmarshal(payload, &a); err != nil {
		return a, fmt.Errorf("lifetime/store: assessment payload: %w", err)
	}
	a.AssessmentID = assessment
	a.CandidateID = candidate
	a.CandidateKey = candidateK
	a.LineageID = lineage
	a.LineageRole = lifetime.LineageRole(role)
	a.ConfirmationOf = confirmOf
	a.Level = lifetime.Level(level)
	a.Confidence = lifetime.ConfidenceLevel(confidence)
	a.ReportableInternal = internal
	a.ReportableExternal = external
	a.Status = lifetime.AssessmentStatus(status)
	a.Stale = stale
	a.ReviewStale = reviewStale
	a.DerivedAt = scanNullTime(derivedAt)
	return a, nil
}

// ListAssessments 返回 task 的全部 assessment（append-only 历史，按 derived_at 升序）。
func (s *Store) ListAssessments(ctx context.Context, taskID string) ([]lifetime.Assessment, error) {
	q := `SELECT ` + assessmentColumns + `
FROM lifetime_assessments WHERE task_id = $1
ORDER BY derived_at, assessment_id`
	rows, err := s.db.QueryContext(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: ListAssessments: %w", err)
	}
	defer rows.Close()

	var out []lifetime.Assessment
	for rows.Next() {
		a, err := scanAssessment(rows)
		if err != nil {
			return nil, fmt.Errorf("lifetime/store: ListAssessments: scan: %w", err)
		}
		out = append(out, a)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

// ListObservations 返回某 candidate 的全部 observation（append-only，按时间升序）。
func (s *Store) ListObservations(ctx context.Context, candidateID string) ([]lifetime.Observation, error) {
	const q = `
SELECT observation_id, experiment_id, candidate_id, payload, created_at
FROM lifetime_observations
WHERE candidate_id = $1
ORDER BY created_at, observation_id`
	rows, err := s.db.QueryContext(ctx, q, candidateID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: ListObservations: %w", err)
	}
	defer rows.Close()

	var out []lifetime.Observation
	for rows.Next() {
		var (
			o           lifetime.Observation
			observation string
			experiment  string
			cand        string
			payload     []byte
			createdAt   sql.NullTime
		)
		if err := rows.Scan(&observation, &experiment, &cand, &payload, &createdAt); err != nil {
			return nil, fmt.Errorf("lifetime/store: ListObservations: scan: %w", err)
		}
		if err := json.Unmarshal(payload, &o); err != nil {
			return nil, fmt.Errorf("lifetime/store: ListObservations: payload: %w", err)
		}
		o.ObservationID = observation
		o.ExperimentID = experiment
		o.CandidateID = cand
		o.CreatedAt = scanNullTime(createdAt)
		out = append(out, o)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

// ListExperiments 返回某 candidate 的全部 experiment（append-only，按时间升序）。
func (s *Store) ListExperiments(ctx context.Context, candidateID string) ([]lifetime.Experiment, error) {
	const q = `
SELECT experiment_id, candidate_id, lineage_id, lineage_role, confirmation_of,
       kind, state, payload, started_at, finished_at, created_at
FROM lifetime_experiments
WHERE candidate_id = $1
ORDER BY created_at, experiment_id`
	rows, err := s.db.QueryContext(ctx, q, candidateID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: ListExperiments: %w", err)
	}
	defer rows.Close()

	var out []lifetime.Experiment
	for rows.Next() {
		e, err := scanExperiment(rows)
		if err != nil {
			return nil, fmt.Errorf("lifetime/store: ListExperiments: scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

const experimentColumns = `
experiment_id, candidate_id, lineage_id, lineage_role, confirmation_of,
kind, state, payload, started_at, finished_at, created_at`

func scanExperiment(sc interface{ Scan(...any) error }) (lifetime.Experiment, error) {
	var (
		e          lifetime.Experiment
		experiment string
		candidate  string
		lineage    string
		role       string
		confirmOf  string
		kind       string
		state      string
		payload    []byte
		startedAt  sql.NullTime
		finishedAt sql.NullTime
		createdAt  sql.NullTime
	)
	err := sc.Scan(&experiment, &candidate, &lineage, &role, &confirmOf,
		&kind, &state, &payload, &startedAt, &finishedAt, &createdAt)
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(payload, &e); err != nil {
		return e, fmt.Errorf("lifetime/store: experiment payload: %w", err)
	}
	e.ExperimentID = experiment
	e.CandidateID = candidate
	e.LineageID = lineage
	e.LineageRole = lifetime.LineageRole(role)
	e.ConfirmationOf = confirmOf
	e.Kind = lifetime.ExperimentKind(kind)
	e.State = lifetime.ExperimentState(state)
	e.StartedAt = scanNullTime(startedAt)
	e.FinishedAt = scanNullTime(finishedAt)
	return e, nil
}
