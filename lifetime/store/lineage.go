package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Autumn-27/artex/lifetime"
)

// CreateLineage 计算 §2.2 的 lineage_id 并幂等写入。
//
// payload 保存完整的 LineageInput，便于 GetLineage / service 层回读。
// 同一 lineage 内容哈希相同，重复创建不会产生新行。
func (s *Store) CreateLineage(ctx context.Context, taskID string, in lifetime.LineageInput) (string, error) {
	if taskID == "" {
		return "", errors.New("lifetime/store: CreateLineage: empty task_id")
	}
	lineageID := lifetime.LineageIDFrom(in)
	payload, err := marshalJSON(in)
	if err != nil {
		return "", err
	}

	const q = `
INSERT INTO lifetime_lineages (
    lineage_id, task_id,
    source_manifest_hash, index_hash, build_id, driver_spec_hash,
    policy_hash, profile_version, harness_version,
    instrumentation_profile_hash, payload, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
ON CONFLICT (lineage_id) DO UPDATE SET
    payload = EXCLUDED.payload`
	if _, err := s.db.ExecContext(ctx, q,
		lineageID, taskID,
		in.SourceManifestHash, in.IndexHash, in.BuildID, in.DriverSpecHash,
		in.PolicyHash, in.ProfileVersion, in.HarnessVersion,
		in.InstrumentationProfileHash, payload,
	); err != nil {
		return "", fmt.Errorf("lifetime/store: CreateLineage: %w", err)
	}
	return lineageID, nil
}

// GetLineage 按 lineage_id 读取 LineageInput 与其所属 task_id。
//
// 第二个返回值是 task_id：调用方通常需要它来 scope 后续状态查询。
func (s *Store) GetLineage(ctx context.Context, lineageID string) (*lifetime.LineageInput, string, error) {
	const q = `
SELECT source_manifest_hash, index_hash, build_id, driver_spec_hash,
       policy_hash, profile_version, harness_version,
       instrumentation_profile_hash, task_id
FROM lifetime_lineages
WHERE lineage_id = $1`
	var (
		in     lifetime.LineageInput
		taskID string
	)
	err := s.db.QueryRowContext(ctx, q, lineageID).Scan(
		&in.SourceManifestHash, &in.IndexHash, &in.BuildID, &in.DriverSpecHash,
		&in.PolicyHash, &in.ProfileVersion, &in.HarnessVersion,
		&in.InstrumentationProfileHash, &taskID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("lifetime/store: GetLineage: %w", err)
	}
	return &in, taskID, nil
}

// RegisterTaskProfile 校验并应用默认值后注册 TaskProfile（§5.6）。
//
// 顺序：ApplyTaskProfileDefaults → ValidateTaskProfile → ComputePolicyHash。
// 返回的 policyHash 同时写回 p.PolicyHash 并落入 payload。
func (s *Store) RegisterTaskProfile(ctx context.Context, taskID string, p *lifetime.TaskProfile) (string, error) {
	if taskID == "" {
		return "", errors.New("lifetime/store: RegisterTaskProfile: empty task_id")
	}
	if p == nil {
		return "", errors.New("lifetime/store: RegisterTaskProfile: nil profile")
	}

	lifetime.ApplyTaskProfileDefaults(p)
	if err := lifetime.ValidateTaskProfile(p); err != nil {
		return "", fmt.Errorf("lifetime/store: RegisterTaskProfile: %w", err)
	}
	policyHash := lifetime.ComputePolicyHash(p)
	p.PolicyHash = policyHash

	payload, err := marshalJSON(p)
	if err != nil {
		return "", err
	}

	const q = `
INSERT INTO lifetime_task_profiles (task_id, name, version, payload, policy_hash, created_at)
VALUES ($1,$2,$3,$4,$5,now())
ON CONFLICT (task_id, name, version) DO UPDATE SET
    payload = EXCLUDED.payload,
    policy_hash = EXCLUDED.policy_hash`
	if _, err := s.db.ExecContext(ctx, q, taskID, p.Name, p.Version, payload, policyHash); err != nil {
		return "", fmt.Errorf("lifetime/store: RegisterTaskProfile: %w", err)
	}
	return policyHash, nil
}
