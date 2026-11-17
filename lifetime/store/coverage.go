package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Autumn-27/artex/lifetime"
)

// GetCoverage 按 unit_id 读取一条覆盖账本行。
func (s *Store) GetCoverage(ctx context.Context, unitID string) (*lifetime.CoverageUnit, error) {
	if unitID == "" {
		return nil, errors.New("lifetime/store: GetCoverage: empty unit_id")
	}
	q := `SELECT ` + coverageUnitColumns + ` FROM lifetime_coverage_units WHERE unit_id = $1`
	u, err := scanCoverageUnit(s.db.QueryRowContext(ctx, q, unitID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: GetCoverage: %w", err)
	}
	return &u, nil
}

// GetCoverageForTask 按 (task_id, 逻辑 unit_id) 读取覆盖行。调用方通常从
// lifetime_family 拿到已带 task 前缀的 unit_id；本方法对未带前缀的输入也
// 正确解析，便于脚本/测试使用。
func (s *Store) GetCoverageForTask(ctx context.Context, taskID, unitID string) (*lifetime.CoverageUnit, error) {
	if taskID == "" {
		return nil, errors.New("lifetime/store: GetCoverageForTask: empty task_id")
	}
	return s.GetCoverage(ctx, scopeID(taskID, unitID))
}

// UpdateCoverageStatus 更新覆盖状态及平台引用（§4.6）。
//
// lineageID/reviewStatus 传空串时保留原值（lineage 是 provenance，不应被
// 无意的空参数清空）。
func (s *Store) UpdateCoverageStatus(ctx context.Context, unitID string, status lifetime.CoverageStatus, reason, evidenceRef, proofRef, reviewStatus, lineageID string) error {
	if unitID == "" {
		return errors.New("lifetime/store: UpdateCoverageStatus: empty unit_id")
	}
	if !status.Valid() {
		return fmt.Errorf("lifetime/store: UpdateCoverageStatus: invalid status %q", status)
	}
	if reviewStatus != "" && reviewStatus != "self_asserted" && reviewStatus != "reviewed" {
		return fmt.Errorf("lifetime/store: UpdateCoverageStatus: invalid review_status %q", reviewStatus)
	}

	const q = `
UPDATE lifetime_coverage_units
SET status = $2,
    reason = $3,
    evidence_ref = $4,
    proof_ref = $5,
    review_status = CASE WHEN $6 = '' THEN review_status ELSE $6 END,
    lineage_id = CASE WHEN $7 = '' THEN lineage_id ELSE $7 END,
    updated_at = now()
WHERE unit_id = $1`
	res, err := s.db.ExecContext(ctx, q, unitID, string(status), reason, evidenceRef, proofRef, reviewStatus, lineageID)
	if err != nil {
		return fmt.Errorf("lifetime/store: UpdateCoverageStatus: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("lifetime/store: UpdateCoverageStatus: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateCoverageStatusForTask 是 UpdateCoverageStatus 的 task-scoped 版本。
func (s *Store) UpdateCoverageStatusForTask(ctx context.Context, taskID, unitID string, status lifetime.CoverageStatus, reason, evidenceRef, proofRef, reviewStatus, lineageID string) error {
	if taskID == "" {
		return errors.New("lifetime/store: UpdateCoverageStatusForTask: empty task_id")
	}
	return s.UpdateCoverageStatus(ctx, scopeID(taskID, unitID), status, reason, evidenceRef, proofRef, reviewStatus, lineageID)
}

// CoverageStats 读取 task 的全部覆盖行并按 §4.8 公式计算统计。
//
// unknownEstimate 是未枚举范围的估计值（只由平台提供，不与账本 unknown 行重复）。
func (s *Store) CoverageStats(ctx context.Context, taskID string, unknownEstimate int) (lifetime.CoverageStats, error) {
	units, err := listCoverageTx(ctx, s.db, taskID)
	if err != nil {
		return lifetime.CoverageStats{}, err
	}
	return lifetime.ComputeCoverageStats(units, unknownEstimate), nil
}
