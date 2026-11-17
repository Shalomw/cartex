package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/lifetime"
)

// GetFamilyDetail 汇总单个对象族的索引与覆盖视图（§8.1/§16.1）。
//
// 语义：
//   - family 取该 task 下该族最新 lineage 的 payload；
//   - lifetime_points 取该族的全部点（跨 lineage 时按自然键保留最新）；
//   - domains / domain_pairs 取 task 最近一次 ingest 的 lineage；
//   - sync_primitives / callbacks 是 task 级索引事实，按自然键保留最新；
//   - relations 取 subject 或 object 命中该族的最新行；
//   - coverage 取 family_id 命中的账本行。
//
// 找不到族返回 ErrNotFound。所有切片在无数据时返回空数组而非 null，
// 便于 agent 直接消费。
func (s *Store) GetFamilyDetail(ctx context.Context, taskID, familyID string) (*lifetime.FamilyDetail, error) {
	if taskID == "" || familyID == "" {
		return nil, errors.New("lifetime/store: GetFamilyDetail: empty task_id or family_id")
	}

	detail := &lifetime.FamilyDetail{
		LifetimePoints: []lifetime.LifetimePoint{},
		SyncPrimitives: []lifetime.SyncPrimitive{},
		Callbacks:      []lifetime.CallbackSpec{},
		Relations:      []lifetime.ObjectRelation{},
		Domains:        []lifetime.Domain{},
		DomainPairs:    []lifetime.DomainPair{},
		Coverage:       []lifetime.CoverageUnit{},
	}

	// 族本身：同一 (task, family) 可能随 lineage 重复 ingest，取最新一行。
	var payload []byte
	err := s.db.QueryRowContext(ctx, `
SELECT payload, lineage_id, index_hash
FROM lifetime_object_families
WHERE task_id = $1 AND family_id = $2
ORDER BY created_at DESC, lineage_id DESC
LIMIT 1`, taskID, familyID).
		Scan(&payload, &detail.LineageID, &detail.IndexHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: GetFamilyDetail: family: %w", err)
	}
	if err := json.Unmarshal(payload, &detail.Family); err != nil {
		return nil, fmt.Errorf("lifetime/store: GetFamilyDetail: family payload: %w", err)
	}
	if detail.Family.FamilyID == "" {
		detail.Family.FamilyID = familyID
	}

	if err := s.familyLifetimePoints(ctx, taskID, familyID, detail); err != nil {
		return nil, err
	}

	// 域/域对是 task 级事实：优先取最近一次 ingest 的 lineage；若尚未写入
	// lifetime_index_runs（例如只落 family 的测试数据），退回该族自身 lineage。
	domainLineage := detail.LineageID
	var latestLineage string
	switch err := s.db.QueryRowContext(ctx, `
SELECT lineage_id
FROM lifetime_index_runs
WHERE task_id = $1
ORDER BY created_at DESC, lineage_id DESC
LIMIT 1`, taskID).Scan(&latestLineage); {
	case err == nil:
		if latestLineage != "" {
			domainLineage = latestLineage
		}
	case errors.Is(err, sql.ErrNoRows):
		// 没有 index run：使用族自身 lineage。
	default:
		return nil, fmt.Errorf("lifetime/store: GetFamilyDetail: latest lineage: %w", err)
	}

	if err := scanJSONRows(ctx, s.db, `
SELECT payload
FROM lifetime_domains
WHERE task_id = $1 AND lineage_id = $2
ORDER BY domain_class_id`, []any{taskID, domainLineage}, func(raw []byte) error {
		var d lifetime.Domain
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		detail.Domains = append(detail.Domains, d)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("lifetime/store: GetFamilyDetail: domains: %w", err)
	}

	if err := scanJSONRows(ctx, s.db, `
SELECT payload
FROM lifetime_domain_pairs
WHERE task_id = $1 AND lineage_id = $2
ORDER BY pair_class_id`, []any{taskID, domainLineage}, func(raw []byte) error {
		var p lifetime.DomainPair
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		detail.DomainPairs = append(detail.DomainPairs, p)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("lifetime/store: GetFamilyDetail: domain_pairs: %w", err)
	}

	if err := s.familySyncPrimitives(ctx, taskID, detail); err != nil {
		return nil, err
	}
	if err := s.familyCallbacks(ctx, taskID, detail); err != nil {
		return nil, err
	}
	if err := s.familyRelations(ctx, taskID, familyID, detail); err != nil {
		return nil, err
	}
	if err := s.familyCoverage(ctx, taskID, familyID, detail); err != nil {
		return nil, err
	}
	return detail, nil
}

// familyLifetimePoints 读取该族的生命周期点；跨 lineage 重复行按
// (site, role, source_ref) 去重并保留最新。
func (s *Store) familyLifetimePoints(ctx context.Context, taskID, familyID string, detail *lifetime.FamilyDetail) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT family_id, site, role, source_ref
FROM (
    SELECT DISTINCT ON (site, role, source_ref)
           family_id, site, role, source_ref, created_at
    FROM lifetime_lifetime_points
    WHERE task_id = $1 AND family_id = $2
    ORDER BY site, role, source_ref, created_at DESC, lineage_id DESC
) latest
ORDER BY site, role, source_ref`, taskID, familyID)
	if err != nil {
		return fmt.Errorf("lifetime/store: GetFamilyDetail: lifetime points: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			p    lifetime.LifetimePoint
			role string
		)
		if err := rows.Scan(&p.FamilyID, &p.Site, &role, &p.SourceRef); err != nil {
			return fmt.Errorf("lifetime/store: GetFamilyDetail: scan lifetime point: %w", err)
		}
		p.Role = lifetime.NodeRole(role)
		detail.LifetimePoints = append(detail.LifetimePoints, p)
	}
	if err := rowsErr(rows); err != nil {
		return err
	}
	return nil
}

// familySyncPrimitives 读取 task 级同步原语事实（自然键去重后取最新）。
func (s *Store) familySyncPrimitives(ctx context.Context, taskID string, detail *lifetime.FamilyDetail) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT primitive, scope, protects, file_line
FROM (
    SELECT DISTINCT ON (primitive, scope, protects, file_line)
           primitive, scope, protects, file_line, created_at
    FROM lifetime_sync_primitives
    WHERE task_id = $1
    ORDER BY primitive, scope, protects, file_line, created_at DESC, lineage_id DESC
) latest
ORDER BY primitive, scope, protects, file_line`, taskID)
	if err != nil {
		return fmt.Errorf("lifetime/store: GetFamilyDetail: sync primitives: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var p lifetime.SyncPrimitive
		if err := rows.Scan(&p.Primitive, &p.Scope, &p.Protects, &p.FileLine); err != nil {
			return fmt.Errorf("lifetime/store: GetFamilyDetail: scan sync primitive: %w", err)
		}
		// sync/callback 是 task 级事实；按族 alloc_site 所在文件过滤，避免把
		// 整个 src/objects 的 168 条同步原语塞给 agent。上限 40 条。
		if !sameSourceFile(detail.Family.AllocSite, p.FileLine) {
			continue
		}
		detail.SyncPrimitives = append(detail.SyncPrimitives, p)
		if len(detail.SyncPrimitives) >= 40 {
			break
		}
	}
	if err := rowsErr(rows); err != nil {
		return err
	}
	return nil
}

// familyCallbacks 读取 task 级回调事实（自然键去重后取最新）。
func (s *Store) familyCallbacks(ctx context.Context, taskID string, detail *lifetime.FamilyDetail) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT register_site, unregister_site, capture, source_ref
FROM (
    SELECT DISTINCT ON (register_site, unregister_site, capture, source_ref)
           register_site, unregister_site, capture, source_ref, created_at
    FROM lifetime_callbacks
    WHERE task_id = $1
    ORDER BY register_site, unregister_site, capture, source_ref, created_at DESC, lineage_id DESC
) latest
ORDER BY register_site, source_ref`, taskID)
	if err != nil {
		return fmt.Errorf("lifetime/store: GetFamilyDetail: callbacks: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var c lifetime.CallbackSpec
		if err := rows.Scan(&c.RegisterSite, &c.UnregisterSite, &c.Capture, &c.SourceRef); err != nil {
			return fmt.Errorf("lifetime/store: GetFamilyDetail: scan callback: %w", err)
		}
		if !sameSourceFile(detail.Family.AllocSite, c.RegisterSite) &&
			!sameSourceFile(detail.Family.AllocSite, c.SourceRef) {
			continue
		}
		detail.Callbacks = append(detail.Callbacks, c)
		if len(detail.Callbacks) >= 40 {
			break
		}
	}
	if err := rowsErr(rows); err != nil {
		return err
	}
	return nil
}

// sameSourceFile 判断两个 file:line [function] 站点是否指向同一源文件。
// 族站点缺失时返回 true（不过滤）。
func sameSourceFile(familySite, factSite string) bool {
	familyFile := siteFile(familySite)
	if familyFile == "" {
		return true
	}
	return siteFile(factSite) == familyFile
}

// siteFile 取 "path:line[:func]" 中的 path。
func siteFile(site string) string {
	site = strings.TrimSpace(site)
	if site == "" {
		return ""
	}
	if i := strings.Index(site, ":"); i > 0 {
		return site[:i]
	}
	return site
}

// familyRelations 读取 subject 或 object 命中该族的关系；relation 的族字段
// 在 payload jsonb 内，用 ->> 过滤后按 relation_id 保留最新。
func (s *Store) familyRelations(ctx context.Context, taskID, familyID string, detail *lifetime.FamilyDetail) error {
	if err := scanJSONRows(ctx, s.db, `
SELECT payload
FROM (
    SELECT DISTINCT ON (relation_id) relation_id, payload, created_at
    FROM lifetime_object_relations
    WHERE task_id = $1
      AND (payload->>'subject_family_id' = $2 OR payload->>'object_family_id' = $2)
    ORDER BY relation_id, created_at DESC, lineage_id DESC
) latest
ORDER BY relation_id`, []any{taskID, familyID}, func(raw []byte) error {
		var rel lifetime.ObjectRelation
		if err := json.Unmarshal(raw, &rel); err != nil {
			return err
		}
		detail.Relations = append(detail.Relations, rel)
		return nil
	}); err != nil {
		return fmt.Errorf("lifetime/store: GetFamilyDetail: relations: %w", err)
	}
	return nil
}

// familyCoverage 读取该族的覆盖账本行。
func (s *Store) familyCoverage(ctx context.Context, taskID, familyID string, detail *lifetime.FamilyDetail) error {
	q := `SELECT ` + coverageUnitColumns + `
FROM lifetime_coverage_units
WHERE task_id = $1 AND family_id = $2
ORDER BY domain_pair_id, form`
	rows, err := s.db.QueryContext(ctx, q, taskID, familyID)
	if err != nil {
		return fmt.Errorf("lifetime/store: GetFamilyDetail: coverage: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		u, err := scanCoverageUnit(rows)
		if err != nil {
			return fmt.Errorf("lifetime/store: GetFamilyDetail: scan coverage: %w", err)
		}
		detail.Coverage = append(detail.Coverage, u)
	}
	if err := rowsErr(rows); err != nil {
		return err
	}
	return nil
}
