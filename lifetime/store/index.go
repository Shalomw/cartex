package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Autumn-27/artex/lifetime"
)

// IngestIndex 幂等写入索引器输出（§8.1）。
//
// 事务内插入 object_families / object_relations / domains / domain_pairs /
// lifetime_points / sync_primitives / callbacks / index_unknowns；
// 重复 ingest 按主键 upsert payload（unknowns 按内容哈希去重）。
func (s *Store) IngestIndex(ctx context.Context, taskID, lineageID string, idx lifetime.IndexOutput) error {
	if taskID == "" {
		return errors.New("lifetime/store: IngestIndex: empty task_id")
	}
	if lineageID == "" {
		return errors.New("lifetime/store: IngestIndex: empty lineage_id")
	}

	return s.withTx(ctx, func(tx *sql.Tx) error {
		// 索引质量是 S1 门禁输入，必须与索引产物一起持久化（R-8.4）。
		const runQ = `
INSERT INTO lifetime_index_runs (task_id, lineage_id, index_hash, index_quality, type_resolution)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (task_id, lineage_id) DO UPDATE SET
    index_hash = EXCLUDED.index_hash,
    index_quality = EXCLUDED.index_quality,
    type_resolution = EXCLUDED.type_resolution,
    created_at = now()`
		if _, err := tx.ExecContext(ctx, runQ,
			taskID, lineageID, idx.IndexHash, idx.IndexQuality, idx.TypeResolution); err != nil {
			return fmt.Errorf("lifetime/store: IngestIndex: index run: %w", err)
		}

		for _, fam := range idx.ObjectFamilies {
			payload, err := marshalJSON(fam)
			if err != nil {
				return err
			}
			const q = `
INSERT INTO lifetime_object_families (task_id, family_id, lineage_id, index_hash, payload)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (task_id, family_id, lineage_id) DO UPDATE SET
    index_hash = EXCLUDED.index_hash,
    payload = EXCLUDED.payload`
			if _, err := tx.ExecContext(ctx, q, taskID, fam.FamilyID, lineageID, idx.IndexHash, payload); err != nil {
				return fmt.Errorf("lifetime/store: IngestIndex: families: %w", err)
			}
		}

		for _, rel := range idx.ObjectRelations {
			payload, err := marshalJSON(rel)
			if err != nil {
				return err
			}
			const q = `
INSERT INTO lifetime_object_relations (task_id, relation_id, lineage_id, index_hash, payload)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (task_id, relation_id, lineage_id) DO UPDATE SET
    index_hash = EXCLUDED.index_hash,
    payload = EXCLUDED.payload`
			if _, err := tx.ExecContext(ctx, q, taskID, rel.RelationID, lineageID, idx.IndexHash, payload); err != nil {
				return fmt.Errorf("lifetime/store: IngestIndex: relations: %w", err)
			}
		}

		for _, d := range idx.Domains {
			payload, err := marshalJSON(d)
			if err != nil {
				return err
			}
			const q = `
INSERT INTO lifetime_domains (task_id, domain_class_id, lineage_id, index_hash, payload)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (task_id, domain_class_id, lineage_id) DO UPDATE SET
    index_hash = EXCLUDED.index_hash,
    payload = EXCLUDED.payload`
			if _, err := tx.ExecContext(ctx, q, taskID, d.DomainClassID, lineageID, idx.IndexHash, payload); err != nil {
				return fmt.Errorf("lifetime/store: IngestIndex: domains: %w", err)
			}
		}

		for _, p := range idx.DomainPairs {
			payload, err := marshalJSON(p)
			if err != nil {
				return err
			}
			const q = `
INSERT INTO lifetime_domain_pairs (task_id, pair_class_id, lineage_id, index_hash, payload)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (task_id, pair_class_id, lineage_id) DO UPDATE SET
    index_hash = EXCLUDED.index_hash,
    payload = EXCLUDED.payload`
			if _, err := tx.ExecContext(ctx, q, taskID, p.PairClassID, lineageID, idx.IndexHash, payload); err != nil {
				return fmt.Errorf("lifetime/store: IngestIndex: domain_pairs: %w", err)
			}
		}

		for _, lp := range idx.LifetimePoints {
			const q = `
INSERT INTO lifetime_lifetime_points
    (task_id, family_id, site, role, source_ref, lineage_id, index_hash)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (task_id, family_id, site, role, source_ref, lineage_id) DO UPDATE SET
    index_hash = EXCLUDED.index_hash`
			if _, err := tx.ExecContext(ctx, q, taskID, lp.FamilyID, lp.Site, string(lp.Role), lp.SourceRef, lineageID, idx.IndexHash); err != nil {
				return fmt.Errorf("lifetime/store: IngestIndex: lifetime_points: %w", err)
			}
		}

		for _, sp := range idx.SyncPrimitives {
			const q = `
INSERT INTO lifetime_sync_primitives
    (task_id, primitive, scope, protects, file_line, lineage_id, index_hash)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (task_id, primitive, scope, protects, file_line, lineage_id) DO UPDATE SET
    index_hash = EXCLUDED.index_hash`
			if _, err := tx.ExecContext(ctx, q, taskID, sp.Primitive, sp.Scope, sp.Protects, sp.FileLine, lineageID, idx.IndexHash); err != nil {
				return fmt.Errorf("lifetime/store: IngestIndex: sync_primitives: %w", err)
			}
		}

		for _, cb := range idx.Callbacks {
			const q = `
INSERT INTO lifetime_callbacks
    (task_id, register_site, unregister_site, capture, source_ref, lineage_id, index_hash)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (task_id, register_site, unregister_site, capture, source_ref, lineage_id) DO UPDATE SET
    index_hash = EXCLUDED.index_hash`
			if _, err := tx.ExecContext(ctx, q, taskID, cb.RegisterSite, cb.UnregisterSite, cb.Capture, cb.SourceRef, lineageID, idx.IndexHash); err != nil {
				return fmt.Errorf("lifetime/store: IngestIndex: callbacks: %w", err)
			}
		}

		for _, u := range idx.Unknowns {
			// detail 可能很长，唯一性由 md5(detail) 表达式索引保证；
			// 使用无目标 ON CONFLICT 吞掉重复（含任意唯一约束冲突）。
			const q = `
INSERT INTO lifetime_index_unknowns (task_id, scope, reason, detail, lineage_id)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT DO NOTHING`
			if _, err := tx.ExecContext(ctx, q, taskID, u.Scope, string(u.Reason), u.Detail, lineageID); err != nil {
				return fmt.Errorf("lifetime/store: IngestIndex: unknowns: %w", err)
			}
		}

		return nil
	})
}

// InitCoverage 初始化覆盖账本与工作包，并为每个 work package 投影 Q1 工作项。
//
// units 以 unit_id 幂等 upsert：重复初始化不会重置已有覆盖状态；
// packages 以 package_id upsert；Q1 的 dedup_key = hash("Q1", package_id)。
func (s *Store) InitCoverage(ctx context.Context, taskID, lineageID string, units []lifetime.CoverageUnit, packages []lifetime.WorkPackage) error {
	if taskID == "" {
		return errors.New("lifetime/store: InitCoverage: empty task_id")
	}

	return s.withTx(ctx, func(tx *sql.Tx) error {
		for i := range units {
			u := units[i]
			if u.UnitID == "" {
				u.UnitID = lifetime.UnitID(u.UnitKind, u.FamilyID, u.RelationID, u.DomainPairID, u.Form)
			}
			if u.UnitID == "" {
				return fmt.Errorf("lifetime/store: InitCoverage: unit %d has no unit_id", i)
			}
			// unit_id 是内容哈希（family×pair×form），跨 task 会重复；加 task 前缀
			// 保证主键全局唯一的语义仍是“task 内一行”。
			u.UnitID = scopeID(taskID, u.UnitID)
			if u.UnitKind == "" {
				u.UnitKind = lifetime.UnitKindFamily
			}
			if u.LineageID == "" {
				u.LineageID = lineageID
			}
			if u.Status == "" {
				u.Status = lifetime.CoverageUnexamined
			}
			if u.ReviewStatus == "" {
				u.ReviewStatus = "self_asserted"
			}
			updatedAt := u.UpdatedAt
			if updatedAt.IsZero() {
				updatedAt = nowUTC()
			}

			const q = `
INSERT INTO lifetime_coverage_units (
    unit_id, task_id, unit_kind, family_id, relation_id, domain_pair_id, form,
    status, reason, evidence_ref, proof_ref, review_status, lineage_id, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (unit_id) DO UPDATE SET
    task_id = EXCLUDED.task_id,
    lineage_id = EXCLUDED.lineage_id,
    updated_at = EXCLUDED.updated_at`
			if _, err := tx.ExecContext(ctx, q,
				u.UnitID, taskID, string(u.UnitKind), nullIfEmpty(u.FamilyID), nullIfEmpty(u.RelationID),
				u.DomainPairID, string(u.Form), string(u.Status), u.Reason, u.EvidenceRef,
				u.ProofRef, u.ReviewStatus, u.LineageID, updatedAt,
			); err != nil {
				return fmt.Errorf("lifetime/store: InitCoverage: unit %s: %w", u.UnitID, err)
			}
		}

		for i := range packages {
			pkg := packages[i]
			if pkg.PackageID == "" {
				pkg.PackageID = lifetime.CanonicalHash("work_package", pkg.FamilyID, pkg.DomainPairID)
			}
			pkg.PackageID = scopeID(taskID, pkg.PackageID)
			forms, err := marshalJSON(pkg.ApplicableForms)
			if err != nil {
				return err
			}
			const q = `
INSERT INTO lifetime_work_packages (
    package_id, task_id, family_id, domain_pair_id, applicable_forms,
    area_id, lease_owner, leased_at, expires_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
ON CONFLICT (package_id) DO UPDATE SET
    family_id = EXCLUDED.family_id,
    domain_pair_id = EXCLUDED.domain_pair_id,
    applicable_forms = EXCLUDED.applicable_forms,
    area_id = CASE WHEN EXCLUDED.area_id <> '' THEN EXCLUDED.area_id
                   ELSE lifetime_work_packages.area_id END,
    updated_at = now()`
			if _, err := tx.ExecContext(ctx, q,
				pkg.PackageID, taskID, pkg.FamilyID, pkg.DomainPairID, forms,
				pkg.AreaID, pkg.LeaseOwner, nullTime(pkg.LeasedAt), nullTime(pkg.ExpiresAt),
			); err != nil {
				return fmt.Errorf("lifetime/store: InitCoverage: package %s: %w", pkg.PackageID, err)
			}

			// Q1：work package 的可开工投影（R-4.16）。
			item := lifetime.WorkItem{
				QueueKind:             lifetime.QueueUnitReadyDiscover,
				ItemRef:               pkg.PackageID,
				ReadyPredicateVersion: "q1.v1",
				State:                 lifetime.WorkItemReady,
				DedupKey:              lifetime.CanonicalHash("Q1", pkg.PackageID),
				LineageHash:           lineageID,
			}
			if _, err := projectWorkItem(ctx, tx, taskID, item); err != nil {
				return fmt.Errorf("lifetime/store: InitCoverage: project Q1 %s: %w", pkg.PackageID, err)
			}
		}

		return nil
	})
}

// ListFamilies 返回某 task 每个 family 的最新索引 payload（按 lineage 创建时间）。
func (s *Store) ListFamilies(ctx context.Context, taskID string) ([]lifetime.ObjectFamily, error) {
	const q = `
SELECT family_id, payload
FROM (
    SELECT DISTINCT ON (family_id) family_id, payload, created_at
    FROM lifetime_object_families
    WHERE task_id = $1
    ORDER BY family_id, created_at DESC, lineage_id DESC
) latest
ORDER BY family_id`
	rows, err := s.db.QueryContext(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: ListFamilies: %w", err)
	}
	defer rows.Close()

	var out []lifetime.ObjectFamily
	for rows.Next() {
		var (
			familyID string
			payload  []byte
		)
		if err := rows.Scan(&familyID, &payload); err != nil {
			return nil, fmt.Errorf("lifetime/store: ListFamilies: scan: %w", err)
		}
		var fam lifetime.ObjectFamily
		if err := json.Unmarshal(payload, &fam); err != nil {
			return nil, fmt.Errorf("lifetime/store: ListFamilies: payload: %w", err)
		}
		if fam.FamilyID == "" {
			fam.FamilyID = familyID
		}
		out = append(out, fam)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

const coverageUnitColumns = `
unit_id, unit_kind, family_id, relation_id, domain_pair_id, form,
status, reason, evidence_ref, proof_ref, review_status, lineage_id, updated_at`

// scanCoverageUnit 从一行扫描 CoverageUnit；family_id/relation_id 可为 NULL。
func scanCoverageUnit(sc interface{ Scan(...any) error }) (lifetime.CoverageUnit, error) {
	var (
		u          lifetime.CoverageUnit
		familyID   sql.NullString
		relationID sql.NullString
	)
	err := sc.Scan(
		&u.UnitID, &u.UnitKind, &familyID, &relationID, &u.DomainPairID, &u.Form,
		&u.Status, &u.Reason, &u.EvidenceRef, &u.ProofRef, &u.ReviewStatus,
		&u.LineageID, &u.UpdatedAt,
	)
	if err != nil {
		return u, err
	}
	u.FamilyID = familyID.String
	u.RelationID = relationID.String
	u.UpdatedAt = u.UpdatedAt.UTC()
	return u, nil
}

// ListCoverage 返回 task 的全部覆盖账本行（按 unit_kind/family/relation/pair/form 排序）。
func (s *Store) ListCoverage(ctx context.Context, taskID string) ([]lifetime.CoverageUnit, error) {
	q := `SELECT ` + coverageUnitColumns + `
FROM lifetime_coverage_units
WHERE task_id = $1
ORDER BY unit_kind, COALESCE(family_id,''), COALESCE(relation_id,''), domain_pair_id, form`
	rows, err := s.db.QueryContext(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: ListCoverage: %w", err)
	}
	defer rows.Close()

	var out []lifetime.CoverageUnit
	for rows.Next() {
		u, err := scanCoverageUnit(rows)
		if err != nil {
			return nil, fmt.Errorf("lifetime/store: ListCoverage: scan: %w", err)
		}
		out = append(out, u)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

// listCoverageTx 在事务/查询器内读取覆盖账本，供 InitCoverage 与对账复用。
func listCoverageTx(ctx context.Context, q querier, taskID string) ([]lifetime.CoverageUnit, error) {
	sqlQ := `SELECT ` + coverageUnitColumns + `
FROM lifetime_coverage_units
WHERE task_id = $1
ORDER BY unit_kind, COALESCE(family_id,''), COALESCE(relation_id,''), domain_pair_id, form`
	rows, err := q.QueryContext(ctx, sqlQ, taskID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: listCoverage: %w", err)
	}
	defer rows.Close()
	var out []lifetime.CoverageUnit
	for rows.Next() {
		u, err := scanCoverageUnit(rows)
		if err != nil {
			return nil, fmt.Errorf("lifetime/store: listCoverage: scan: %w", err)
		}
		out = append(out, u)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

// nullIfEmpty 把空串映射为 SQL NULL（coverage 的 family_id/relation_id）。
func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// GetIndexOutput 从 lifetime_* 表重建 IndexOutput，供评估 S1/S2 使用。
// 索引质量/类型解析来自 lifetime_index_runs（R-8.4）。
func (s *Store) GetIndexOutput(ctx context.Context, taskID, lineageID string) (*lifetime.IndexOutput, error) {
	if taskID == "" || lineageID == "" {
		return nil, errors.New("lifetime/store: GetIndexOutput: empty task_id or lineage_id")
	}
	idx := &lifetime.IndexOutput{}
	err := s.db.QueryRowContext(ctx, `
SELECT index_hash, index_quality, type_resolution
FROM lifetime_index_runs
WHERE task_id=$1 AND lineage_id=$2`, taskID, lineageID).
		Scan(&idx.IndexHash, &idx.IndexQuality, &idx.TypeResolution)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: GetIndexOutput: run: %w", err)
	}

	if err := scanJSONRows(ctx, s.db, `
SELECT payload FROM lifetime_object_families
WHERE task_id=$1 AND lineage_id=$2 ORDER BY family_id`, []any{taskID, lineageID}, func(raw []byte) error {
		var v lifetime.ObjectFamily
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		idx.ObjectFamilies = append(idx.ObjectFamilies, v)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("lifetime/store: GetIndexOutput: families: %w", err)
	}
	if err := scanJSONRows(ctx, s.db, `
SELECT payload FROM lifetime_object_relations
WHERE task_id=$1 AND lineage_id=$2 ORDER BY relation_id`, []any{taskID, lineageID}, func(raw []byte) error {
		var v lifetime.ObjectRelation
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		idx.ObjectRelations = append(idx.ObjectRelations, v)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("lifetime/store: GetIndexOutput: relations: %w", err)
	}
	if err := scanJSONRows(ctx, s.db, `
SELECT payload FROM lifetime_domains
WHERE task_id=$1 AND lineage_id=$2 ORDER BY domain_class_id`, []any{taskID, lineageID}, func(raw []byte) error {
		var v lifetime.Domain
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		idx.Domains = append(idx.Domains, v)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("lifetime/store: GetIndexOutput: domains: %w", err)
	}
	if err := scanJSONRows(ctx, s.db, `
SELECT payload FROM lifetime_domain_pairs
WHERE task_id=$1 AND lineage_id=$2 ORDER BY pair_class_id`, []any{taskID, lineageID}, func(raw []byte) error {
		var v lifetime.DomainPair
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		idx.DomainPairs = append(idx.DomainPairs, v)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("lifetime/store: GetIndexOutput: domain pairs: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT family_id, site, role, source_ref
FROM lifetime_lifetime_points
WHERE task_id=$1 AND lineage_id=$2
ORDER BY family_id, site, role`, taskID, lineageID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: GetIndexOutput: lifetime points: %w", err)
	}
	for rows.Next() {
		var p lifetime.LifetimePoint
		var role string
		if err := rows.Scan(&p.FamilyID, &p.Site, &role, &p.SourceRef); err != nil {
			rows.Close()
			return nil, fmt.Errorf("lifetime/store: GetIndexOutput: scan point: %w", err)
		}
		p.Role = lifetime.NodeRole(role)
		idx.LifetimePoints = append(idx.LifetimePoints, p)
	}
	if err := rowsErr(rows); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `
SELECT primitive, scope, protects, file_line
FROM lifetime_sync_primitives
WHERE task_id=$1 AND lineage_id=$2
ORDER BY primitive, scope, protects`, taskID, lineageID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: GetIndexOutput: sync primitives: %w", err)
	}
	for rows.Next() {
		var p lifetime.SyncPrimitive
		if err := rows.Scan(&p.Primitive, &p.Scope, &p.Protects, &p.FileLine); err != nil {
			rows.Close()
			return nil, fmt.Errorf("lifetime/store: GetIndexOutput: scan sync: %w", err)
		}
		idx.SyncPrimitives = append(idx.SyncPrimitives, p)
	}
	if err := rowsErr(rows); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `
SELECT register_site, unregister_site, capture, source_ref
FROM lifetime_callbacks
WHERE task_id=$1 AND lineage_id=$2
ORDER BY register_site, source_ref`, taskID, lineageID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: GetIndexOutput: callbacks: %w", err)
	}
	for rows.Next() {
		var c lifetime.CallbackSpec
		if err := rows.Scan(&c.RegisterSite, &c.UnregisterSite, &c.Capture, &c.SourceRef); err != nil {
			rows.Close()
			return nil, fmt.Errorf("lifetime/store: GetIndexOutput: scan callback: %w", err)
		}
		idx.Callbacks = append(idx.Callbacks, c)
	}
	if err := rowsErr(rows); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `
SELECT scope, reason, detail
FROM lifetime_index_unknowns
WHERE task_id=$1 AND lineage_id=$2
ORDER BY scope, reason, detail`, taskID, lineageID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: GetIndexOutput: unknowns: %w", err)
	}
	for rows.Next() {
		var u lifetime.IndexUnknown
		var reason string
		if err := rows.Scan(&u.Scope, &reason, &u.Detail); err != nil {
			rows.Close()
			return nil, fmt.Errorf("lifetime/store: GetIndexOutput: scan unknown: %w", err)
		}
		u.Reason = lifetime.UnknownReason(reason)
		idx.Unknowns = append(idx.Unknowns, u)
	}
	if err := rowsErr(rows); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return idx, nil
}
