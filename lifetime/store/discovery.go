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

// ---------------------------------------------------------------------------
// §8.2 static support
// ---------------------------------------------------------------------------

// RecordStaticSupport 写入静态支持（R-8.11）。
//
// 必须包含生命周期点（lifetime_point）与竞争访问（access），且 chain 中每个
// 节点都有 file:line。citation_check 为 unresolved/mismatch 的节点允许写入——
// S1/S2 由谓词层拒绝（R-8.9），store 不做证据结论判断。
func (s *Store) RecordStaticSupport(ctx context.Context, taskID string, sup lifetime.StaticSupport) (string, error) {
	if taskID == "" {
		return "", errors.New("lifetime/store: RecordStaticSupport: empty task_id")
	}
	if err := validateStaticSupport(sup); err != nil {
		return "", err
	}

	if sup.SupportID == "" {
		sup.SupportID = lifetime.NewID("sup")
	}
	if sup.CreatedAt.IsZero() {
		sup.CreatedAt = nowUTC()
	}
	facts, err := marshalJSON(sup.Facts)
	if err != nil {
		return "", err
	}
	chain, err := marshalJSON(sup.Chain)
	if err != nil {
		return "", err
	}
	checks, err := marshalJSON(sup.CitationChecks)
	if err != nil {
		return "", err
	}

	const q = `
INSERT INTO lifetime_static_support (
    support_id, task_id, unit_id, candidate_id, facts, chain, citation_checks,
    index_hash, created_by, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (support_id) DO UPDATE SET
    unit_id = EXCLUDED.unit_id,
    candidate_id = EXCLUDED.candidate_id,
    facts = EXCLUDED.facts,
    chain = EXCLUDED.chain,
    citation_checks = EXCLUDED.citation_checks,
    index_hash = EXCLUDED.index_hash,
    created_by = EXCLUDED.created_by`
	if _, err := s.db.ExecContext(ctx, q,
		sup.SupportID, taskID, sup.UnitID, sup.CandidateID, facts, chain, checks,
		sup.IndexHash, sup.CreatedBy, sup.CreatedAt,
	); err != nil {
		return "", fmt.Errorf("lifetime/store: RecordStaticSupport: %w", err)
	}
	return sup.SupportID, nil
}

// validateStaticSupport 实现 R-8.11 的机械检查。
func validateStaticSupport(sup lifetime.StaticSupport) error {
	if len(sup.Chain) == 0 {
		return errors.New("lifetime/store: static support: empty evidence_chain")
	}
	hasLifetimePoint := false
	hasAccess := false
	for i, n := range sup.Chain {
		if !n.Role.Valid() {
			return fmt.Errorf("lifetime/store: static support: chain[%d] invalid role %q", i, n.Role)
		}
		if n.File == "" || n.Line <= 0 {
			return fmt.Errorf("lifetime/store: static support: chain[%d] missing file:line", i)
		}
		switch n.Role {
		case lifetime.NodeLifetimePoint:
			hasLifetimePoint = true
		case lifetime.NodeAccess:
			hasAccess = true
		}
	}
	if !hasLifetimePoint {
		return errors.New("lifetime/store: static support: chain missing lifetime_point")
	}
	if !hasAccess {
		return errors.New("lifetime/store: static support: chain missing access")
	}
	for i, n := range sup.Facts {
		if !n.Role.Valid() {
			return fmt.Errorf("lifetime/store: static support: facts[%d] invalid role %q", i, n.Role)
		}
		if n.File == "" || n.Line <= 0 {
			return fmt.Errorf("lifetime/store: static support: facts[%d] missing file:line", i)
		}
	}
	for i, c := range sup.CitationChecks {
		if !c.Status.Valid() {
			return fmt.Errorf("lifetime/store: static support: citation_checks[%d] invalid status %q", i, c.Status)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// §8.3 candidate
// ---------------------------------------------------------------------------

// RecordCandidate 校验并写入候选，同时把对应覆盖行推进为 candidate_open。
//
// 校验（R-2.1/R-8.12/R-8.16）：
//   - domain_pair_id 非空（未解析用 unresolved:<family_id>:<access_set_hash>）；
//   - lifetime.ValidateFourQuestions + lifetime.FormFactsComplete；
//   - 至少一个竞争访问、evidence_chain 非空且角色合法、同步原语有 protects。
//
// candidate_key / mechanism_key 为空时按 §2.2 计算。
func (s *Store) RecordCandidate(ctx context.Context, taskID string, c lifetime.Candidate) (string, error) {
	if taskID == "" {
		return "", errors.New("lifetime/store: RecordCandidate: empty task_id")
	}
	if c.DomainPairID == "" {
		return "", errors.New("lifetime/store: RecordCandidate: domain_pair_id is required (R-2.1)")
	}
	if !c.PrimaryForm.Valid() {
		return "", fmt.Errorf("lifetime/store: RecordCandidate: invalid primary_form %q", c.PrimaryForm)
	}
	if err := lifetime.ValidateFourQuestions(&c.CandidatePackage); err != nil {
		return "", fmt.Errorf("lifetime/store: RecordCandidate: four questions: %w", err)
	}
	if ok, missing := lifetime.FormFactsComplete(&c.CandidatePackage); !ok {
		return "", fmt.Errorf("lifetime/store: RecordCandidate: form facts incomplete: %v", missing)
	}
	if err := validateCandidateShape(c.CandidatePackage); err != nil {
		return "", err
	}

	// family_id 缺失时按 §2.2 从 family key 推导，便于 agent 只提交结构字段。
	if c.Family.FamilyID == "" && c.Family.TypeCanonical != "" && c.Family.AllocSite != "" {
		c.Family.FamilyID = lifetime.FamilyID(lifetime.FamilyKey{
			TypeCanonical: c.Family.TypeCanonical,
			AllocSite:     c.Family.AllocSite,
			StorageClass:  c.Family.StorageClass,
			LifetimeForm:  c.Family.LifetimeForm,
			MemberPath:    c.Family.MemberPath,
		})
	}

	c.CandidateID = orNewID(c.CandidateID, "cand")
	if c.UnitID == "" {
		c.UnitID = lifetime.UnitID(lifetime.UnitKindFamily, c.Family.FamilyID, "", c.DomainPairID, c.PrimaryForm)
	}
	// §2.2：lifetime_point_hash 只对 lifetime_point 节点取 site+role。
	lifetimePoints := make([]lifetime.EvidenceNode, 0, len(c.EvidenceChain))
	for _, n := range c.EvidenceChain {
		if n.Role == lifetime.NodeLifetimePoint {
			lifetimePoints = append(lifetimePoints, n)
		}
	}
	lifetimePointHash := lifetime.LifetimePointHash(lifetimePoints)
	accessSetHash := lifetime.AccessSetHash(c.CompetingAccesses)
	holderSetHash := lifetime.HolderSetHash(c.Holders)
	if c.CandidateKey == "" {
		c.CandidateKey = lifetime.CandidateKey(
			c.Family.FamilyID, c.DomainPairID, c.PrimaryForm,
			lifetimePointHash, accessSetHash, holderSetHash,
		)
	}
	if c.MechanismKey == "" {
		familySet := c.FamilySet
		if len(familySet) == 0 && c.Family.FamilyID != "" {
			familySet = []string{c.Family.FamilyID}
		}
		// CandidatePackage 没有 root_cause_kind 字段；用 primary_form 作为
		// mechanism 的 root cause 分类（§2.2 的 root_cause_kind）。
		c.MechanismKey = lifetime.MechanismKey(
			familySet, lifetimePointHash, accessSetHash, string(c.PrimaryForm),
		)
	}
	if c.ContractStatus == "" {
		c.ContractStatus = lifetime.ContractUnknown
	}
	if !c.ContractStatus.Valid() {
		return "", fmt.Errorf("lifetime/store: RecordCandidate: invalid contract_status %q", c.ContractStatus)
	}
	now := nowUTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now

	payload, err := marshalJSON(c)
	if err != nil {
		return "", err
	}

	err = s.withTx(ctx, func(tx *sql.Tx) error {
		const q = `
INSERT INTO lifetime_candidates (
    candidate_id, task_id, unit_id, support_id, candidate_key, mechanism_key,
    primary_form, domain_pair_id, payload, contract_status,
    budget_remaining, budget_used, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (candidate_id) DO UPDATE SET
    unit_id = EXCLUDED.unit_id,
    support_id = EXCLUDED.support_id,
    candidate_key = EXCLUDED.candidate_key,
    mechanism_key = EXCLUDED.mechanism_key,
    primary_form = EXCLUDED.primary_form,
    domain_pair_id = EXCLUDED.domain_pair_id,
    payload = EXCLUDED.payload,
    contract_status = EXCLUDED.contract_status,
    updated_at = EXCLUDED.updated_at`
		if _, err := tx.ExecContext(ctx, q,
			c.CandidateID, taskID, c.UnitID, c.SupportID, c.CandidateKey, c.MechanismKey,
			string(c.PrimaryForm), c.DomainPairID, payload, string(c.ContractStatus),
			c.Budget.Remaining, c.Budget.Used, c.CreatedAt, c.UpdatedAt,
		); err != nil {
			return fmt.Errorf("lifetime/store: RecordCandidate: insert: %w", err)
		}

		unitID := c.UnitID
		if unitID == "" {
			unitID = lifetime.UnitID(lifetime.UnitKindFamily, c.Family.FamilyID, "", c.DomainPairID, c.PrimaryForm)
		}
		unitID = scopeID(taskID, unitID)
		unit := lifetime.CoverageUnit{
			UnitID:       unitID,
			UnitKind:     lifetime.UnitKindFamily,
			FamilyID:     c.Family.FamilyID,
			DomainPairID: c.DomainPairID,
			Form:         c.PrimaryForm,
			Status:       lifetime.CoverageCandidateOpen,
			ReviewStatus: "self_asserted",
		}
		return upsertCoverageStatus(ctx, tx, taskID, unit, now)
	})
	if err != nil {
		return "", err
	}
	return c.CandidateID, nil
}

// validateCandidateShape 实现 R-8.12 中不属于四问/形态事实的结构要求。
func validateCandidateShape(pkg lifetime.CandidatePackage) error {
	if len(pkg.CompetingAccesses) == 0 {
		return errors.New("lifetime/store: candidate: at least one competing access is required (R-8.12)")
	}
	if len(pkg.EvidenceChain) == 0 {
		return errors.New("lifetime/store: candidate: evidence_chain is required (R-8.12)")
	}
	for i, n := range pkg.EvidenceChain {
		if !n.Role.Valid() {
			return fmt.Errorf("lifetime/store: candidate: evidence_chain[%d] invalid role %q", i, n.Role)
		}
	}
	for i, s := range pkg.Synchronization {
		if s.Protects == "" {
			return fmt.Errorf("lifetime/store: candidate: synchronization[%d] missing protects (R-8.12)", i)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// §4.7 no_risk / unknown / new_lead
// ---------------------------------------------------------------------------

// RecordNoRisk 校验并写入 no_risk 论证；covers_current_form 时把对应覆盖行
// 推进为 no_risk（R-4.19/R-4.23）。
func (s *Store) RecordNoRisk(ctx context.Context, taskID string, n lifetime.NoRisk) (string, error) {
	if taskID == "" {
		return "", errors.New("lifetime/store: RecordNoRisk: empty task_id")
	}
	if !n.Form.Valid() {
		return "", fmt.Errorf("lifetime/store: RecordNoRisk: invalid form %q", n.Form)
	}
	// record_no_risk 的权威参数是 unit_id（§16.2）；family_id/domain_pair_id
	// 由平台从覆盖账本解析，避免调用方重复传键。
	if n.UnitID != "" {
		n.UnitID = scopeID(taskID, n.UnitID)
		if err := s.resolveNoRiskUnit(ctx, taskID, &n); err != nil {
			return "", err
		}
	}
	lineageID := s.coverageLineage(ctx, taskID, n.FamilyID, n.DomainPairID, n.Form)
	if err := lifetime.ValidateNoRiskArgument(n, lineageID); err != nil {
		return "", fmt.Errorf("lifetime/store: RecordNoRisk: %w", err)
	}
	if n.ReviewStatus == "" {
		n.ReviewStatus = "self_asserted"
	}
	if n.ReviewStatus != "self_asserted" && n.ReviewStatus != "reviewed" {
		return "", fmt.Errorf("lifetime/store: RecordNoRisk: invalid review_status %q", n.ReviewStatus)
	}
	n.NoRiskID = orNewID(n.NoRiskID, "nr")
	if n.CreatedAt.IsZero() {
		n.CreatedAt = nowUTC()
	}
	arguments, err := marshalJSON(n.Arguments)
	if err != nil {
		return "", err
	}

	err = s.withTx(ctx, func(tx *sql.Tx) error {
		const q = `
INSERT INTO lifetime_no_risk (
    no_risk_id, task_id, family_id, domain_pair_id, form, arguments,
    covers_current_form, review_status, reviewer_id, created_by, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (no_risk_id) DO UPDATE SET
    arguments = EXCLUDED.arguments,
    covers_current_form = EXCLUDED.covers_current_form,
    review_status = EXCLUDED.review_status,
    reviewer_id = EXCLUDED.reviewer_id`
		if _, err := tx.ExecContext(ctx, q,
			n.NoRiskID, taskID, n.FamilyID, n.DomainPairID, string(n.Form), arguments,
			n.CoversCurrentForm, n.ReviewStatus, n.ReviewerID, n.CreatedBy, n.CreatedAt,
		); err != nil {
			return fmt.Errorf("lifetime/store: RecordNoRisk: insert: %w", err)
		}

		if !n.CoversCurrentForm {
			return nil
		}
		unitID := n.UnitID
		if unitID == "" {
			unitID = lifetime.UnitID(lifetime.UnitKindFamily, n.FamilyID, "", n.DomainPairID, n.Form)
		}
		unitID = scopeID(taskID, unitID)
		unit := lifetime.CoverageUnit{
			UnitID:       unitID,
			UnitKind:     lifetime.UnitKindFamily,
			FamilyID:     n.FamilyID,
			DomainPairID: n.DomainPairID,
			Form:         n.Form,
			Status:       lifetime.CoverageNoRisk,
			EvidenceRef:  n.NoRiskID,
			ReviewStatus: n.ReviewStatus,
			LineageID:    lineageID,
		}
		return upsertCoverageStatus(ctx, tx, taskID, unit, nowUTC())
	})
	if err != nil {
		return "", err
	}
	return n.NoRiskID, nil
}

// resolveNoRiskUnit 用覆盖账本把 record_no_risk 的 unit_id 解析为
// family_id / domain_pair_id / form；调用方显式给出的键必须与账本一致。
// 未找到 unit 或字段冲突均拒绝写入（R-4.19/R-8.17）。
func (s *Store) resolveNoRiskUnit(ctx context.Context, taskID string, n *lifetime.NoRisk) error {
	const q = `
SELECT unit_kind, COALESCE(family_id,''), COALESCE(relation_id,''), domain_pair_id, form
FROM lifetime_coverage_units
WHERE task_id = $1 AND unit_id = $2`
	var kind, familyID, relationID, domainPairID, form string
	err := s.db.QueryRowContext(ctx, q, taskID, n.UnitID).
		Scan(&kind, &familyID, &relationID, &domainPairID, &form)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lifetime/store: RecordNoRisk: unit_id %q not found in coverage ledger", n.UnitID)
	}
	if err != nil {
		return fmt.Errorf("lifetime/store: RecordNoRisk: resolve unit: %w", err)
	}
	if kind != string(lifetime.UnitKindFamily) || relationID != "" {
		return fmt.Errorf("lifetime/store: RecordNoRisk: unit_id %q is not a family unit", n.UnitID)
	}
	if n.FamilyID != "" && n.FamilyID != familyID {
		return fmt.Errorf("lifetime/store: RecordNoRisk: family_id %q conflicts with unit %q", n.FamilyID, familyID)
	}
	if n.DomainPairID != "" && n.DomainPairID != domainPairID {
		return fmt.Errorf("lifetime/store: RecordNoRisk: domain_pair_id %q conflicts with unit %q", n.DomainPairID, domainPairID)
	}
	if n.Form != "" && string(n.Form) != form {
		return fmt.Errorf("lifetime/store: RecordNoRisk: form %q conflicts with unit %q", n.Form, form)
	}
	n.FamilyID = familyID
	n.DomainPairID = domainPairID
	n.Form = lifetime.Form(form)
	return nil
}

// coverageLineage 查找覆盖行的 lineage_id；缺失返回空串（不阻塞写入）。
func (s *Store) coverageLineage(ctx context.Context, taskID, familyID, domainPairID string, form lifetime.Form) string {
	const q = `
SELECT lineage_id FROM lifetime_coverage_units
WHERE task_id=$1 AND COALESCE(family_id,'')=$2 AND domain_pair_id=$3 AND form=$4
LIMIT 1`
	var lineageID string
	err := s.db.QueryRowContext(ctx, q, taskID, familyID, domainPairID, string(form)).Scan(&lineageID)
	if err != nil {
		return ""
	}
	return lineageID
}

// RecordUnknown 写入结构化 unknown（R-8.18）。
func (s *Store) RecordUnknown(ctx context.Context, taskID string, u lifetime.UnknownRecord) (string, error) {
	if taskID == "" {
		return "", errors.New("lifetime/store: RecordUnknown: empty task_id")
	}
	if u.Scope == "" {
		return "", errors.New("lifetime/store: RecordUnknown: empty scope")
	}
	if !u.Reason.Valid() {
		return "", fmt.Errorf("lifetime/store: RecordUnknown: invalid reason %q (R-8.18)", u.Reason)
	}
	u.UnknownID = orNewID(u.UnknownID, "unk")
	if u.CreatedAt.IsZero() {
		u.CreatedAt = nowUTC()
	}
	const q = `
INSERT INTO lifetime_unknowns (
    unknown_id, task_id, family_id, scope, reason, detail, lineage_id, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (unknown_id) DO UPDATE SET
    scope = EXCLUDED.scope,
    reason = EXCLUDED.reason,
    detail = EXCLUDED.detail,
    lineage_id = EXCLUDED.lineage_id`
	if _, err := s.db.ExecContext(ctx, q,
		u.UnknownID, taskID, u.FamilyID, u.Scope, string(u.Reason), u.Detail, u.LineageID, u.CreatedAt,
	); err != nil {
		return "", fmt.Errorf("lifetime/store: RecordUnknown: %w", err)
	}
	return u.UnknownID, nil
}

// RecordNewLead 写入新线索；不自动创建 candidate（R-8.19）。
func (s *Store) RecordNewLead(ctx context.Context, taskID string, l lifetime.NewLead) (string, error) {
	if taskID == "" {
		return "", errors.New("lifetime/store: RecordNewLead: empty task_id")
	}
	l.LeadID = orNewID(l.LeadID, "lead")
	if l.Status == "" {
		l.Status = "open"
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt = nowUTC()
	}
	domains, err := marshalJSON(l.Domains)
	if err != nil {
		return "", err
	}
	const q = `
INSERT INTO lifetime_new_leads (
    lead_id, task_id, source_event, family_id, domains, reason, status, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (lead_id) DO UPDATE SET
    source_event = EXCLUDED.source_event,
    family_id = EXCLUDED.family_id,
    domains = EXCLUDED.domains,
    reason = EXCLUDED.reason,
    status = EXCLUDED.status`
	if _, err := s.db.ExecContext(ctx, q,
		l.LeadID, taskID, l.SourceEvent, l.FamilyID, domains, l.Reason, l.Status, l.CreatedAt,
	); err != nil {
		return "", fmt.Errorf("lifetime/store: RecordNewLead: %w", err)
	}
	return l.LeadID, nil
}

// RequestProbe 记录 probe 请求并投影 Q2(mode=probe) 工作项（R-8.22）。
//
// probe 模式编码在 dedup_key 中：Q2:probe:<candidate_id>。
func (s *Store) RequestProbe(ctx context.Context, taskID, candidateID, question, expectedSignal string) (string, error) {
	if taskID == "" || candidateID == "" {
		return "", errors.New("lifetime/store: RequestProbe: empty task_id or candidate_id")
	}
	if question == "" {
		return "", errors.New("lifetime/store: RequestProbe: empty question")
	}
	probeID := lifetime.NewID("probe")
	now := nowUTC()

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		const q = `
INSERT INTO lifetime_probes (probe_request_id, task_id, candidate_id, question, expected_signal, created_at)
VALUES ($1,$2,$3,$4,$5,$6)`
		if _, err := tx.ExecContext(ctx, q, probeID, taskID, candidateID, question, expectedSignal, now); err != nil {
			return fmt.Errorf("lifetime/store: RequestProbe: insert: %w", err)
		}
		item := lifetime.WorkItem{
			QueueKind:             lifetime.QueueCandidateReadyVerify,
			ItemRef:               candidateID,
			ReadyPredicateVersion: "q2.probe.v1",
			State:                 lifetime.WorkItemReady,
			DedupKey:              "Q2:probe:" + candidateID,
			Reason:                "probe",
		}
		if _, err := projectWorkItem(ctx, tx, taskID, item); err != nil {
			return fmt.Errorf("lifetime/store: RequestProbe: project Q2: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return probeID, nil
}

// ---------------------------------------------------------------------------
// candidate 读取 / 预算
// ---------------------------------------------------------------------------

const candidateColumns = `
candidate_id, unit_id, support_id, candidate_key, mechanism_key, primary_form,
domain_pair_id, payload, contract_status, budget_remaining, budget_used,
created_at, updated_at`

func scanCandidate(sc interface{ Scan(...any) error }) (lifetime.Candidate, error) {
	var (
		c         lifetime.Candidate
		candID    string
		unitID    string
		supportID string
		candKey   string
		mechKey   string
		payload   []byte
		form      string
		pairID    string
		status    string
		created   sql.NullTime
		updated   sql.NullTime
		remaining int
		used      int
	)
	err := sc.Scan(
		&candID, &unitID, &supportID, &candKey, &mechKey, &form,
		&pairID, &payload, &status, &remaining, &used, &created, &updated,
	)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, fmt.Errorf("lifetime/store: candidate payload: %w", err)
	}
	// 离散列是权威来源（payload 可能在列更新后变旧）。
	c.CandidateID = candID
	c.UnitID = unitID
	c.SupportID = supportID
	c.CandidateKey = candKey
	c.MechanismKey = mechKey
	c.DomainPairID = pairID
	c.PrimaryForm = lifetime.Form(form)
	c.ContractStatus = lifetime.ContractStatus(status)
	c.Budget = lifetime.CandidateBudget{Remaining: remaining, Used: used}
	c.CreatedAt = scanNullTime(created)
	c.UpdatedAt = scanNullTime(updated)
	return c, nil
}

// GetCandidate 按 candidate_id 读取候选。
func (s *Store) GetCandidate(ctx context.Context, candidateID string) (*lifetime.Candidate, error) {
	q := `SELECT ` + candidateColumns + ` FROM lifetime_candidates WHERE candidate_id = $1`
	c, err := scanCandidate(s.db.QueryRowContext(ctx, q, candidateID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: GetCandidate: %w", err)
	}
	return &c, nil
}

// ListCandidates 返回 task 的全部候选（按创建时间升序）。
func (s *Store) ListCandidates(ctx context.Context, taskID string) ([]lifetime.Candidate, error) {
	q := `SELECT ` + candidateColumns + ` FROM lifetime_candidates WHERE task_id = $1 ORDER BY created_at, candidate_id`
	rows, err := s.db.QueryContext(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: ListCandidates: %w", err)
	}
	defer rows.Close()

	var out []lifetime.Candidate
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, fmt.Errorf("lifetime/store: ListCandidates: scan: %w", err)
		}
		out = append(out, c)
	}
	if err := rowsErr(rows); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateCandidateBudget 由平台维护候选级预算（R-6.9）；agent 不得写。
func (s *Store) UpdateCandidateBudget(ctx context.Context, candidateID string, remaining, used int) error {
	if candidateID == "" {
		return errors.New("lifetime/store: UpdateCandidateBudget: empty candidate_id")
	}
	if remaining < 0 || used < 0 {
		return errors.New("lifetime/store: UpdateCandidateBudget: negative budget")
	}
	const q = `
UPDATE lifetime_candidates
SET budget_remaining = $2, budget_used = $3, updated_at = now()
WHERE candidate_id = $1`
	res, err := s.db.ExecContext(ctx, q, candidateID, remaining, used)
	if err != nil {
		return fmt.Errorf("lifetime/store: UpdateCandidateBudget: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("lifetime/store: UpdateCandidateBudget: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// upsertCoverageStatus 插入或更新一条覆盖行。
//
// 冲突时更新状态与结论引用；lineage 为空时保留原值，review_status 不降级
// （reviewed 保持 reviewed）。用于 candidate_open / no_risk 等平台投影。
func upsertCoverageStatus(ctx context.Context, q querier, taskID string, u lifetime.CoverageUnit, at time.Time) error {
	if u.UnitID == "" {
		u.UnitID = lifetime.UnitID(u.UnitKind, u.FamilyID, u.RelationID, u.DomainPairID, u.Form)
	}
	if u.UnitID == "" {
		return errors.New("lifetime/store: coverage: empty unit_id")
	}
	u.UnitID = scopeID(taskID, u.UnitID)
	if u.UnitKind == "" {
		u.UnitKind = lifetime.UnitKindFamily
	}
	if u.ReviewStatus == "" {
		u.ReviewStatus = "self_asserted"
	}
	if at.IsZero() {
		at = nowUTC()
	}

	const sqlQ = `
INSERT INTO lifetime_coverage_units (
    unit_id, task_id, unit_kind, family_id, relation_id, domain_pair_id, form,
    status, reason, evidence_ref, proof_ref, review_status, lineage_id, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (unit_id) DO UPDATE SET
    task_id = EXCLUDED.task_id,
    status = EXCLUDED.status,
    reason = EXCLUDED.reason,
    evidence_ref = CASE WHEN EXCLUDED.evidence_ref <> ''
                        THEN EXCLUDED.evidence_ref
                        ELSE lifetime_coverage_units.evidence_ref END,
    proof_ref = CASE WHEN EXCLUDED.proof_ref <> ''
                     THEN EXCLUDED.proof_ref
                     ELSE lifetime_coverage_units.proof_ref END,
    review_status = CASE WHEN EXCLUDED.review_status = 'reviewed'
                         THEN 'reviewed'
                         ELSE lifetime_coverage_units.review_status END,
    lineage_id = CASE WHEN EXCLUDED.lineage_id <> ''
                      THEN EXCLUDED.lineage_id
                      ELSE lifetime_coverage_units.lineage_id END,
    updated_at = EXCLUDED.updated_at`
	_, err := q.ExecContext(ctx, sqlQ,
		u.UnitID, taskID, string(u.UnitKind), nullIfEmpty(u.FamilyID), nullIfEmpty(u.RelationID),
		u.DomainPairID, string(u.Form), string(u.Status), u.Reason, u.EvidenceRef,
		u.ProofRef, u.ReviewStatus, u.LineageID, at,
	)
	if err != nil {
		return fmt.Errorf("lifetime/store: coverage upsert %s: %w", u.UnitID, err)
	}
	return nil
}

func orNewID(id, prefix string) string {
	if id != "" {
		return id
	}
	return lifetime.NewID(prefix)
}

// GetStaticSupport 读取静态支持（评估 S1 用）。facts/chain/citation_checks
// 是 jsonb 列，按写入时的结构反序列化。
func (s *Store) GetStaticSupport(ctx context.Context, supportID string) (*lifetime.StaticSupport, error) {
	if supportID == "" {
		return nil, errors.New("lifetime/store: GetStaticSupport: empty support_id")
	}
	var (
		sup                     lifetime.StaticSupport
		facts, chain, citations []byte
	)
	err := s.db.QueryRowContext(ctx, `
SELECT support_id, unit_id, COALESCE(candidate_id,''), facts, chain, citation_checks,
       index_hash, created_by, created_at
FROM lifetime_static_support
WHERE support_id=$1`, supportID).
		Scan(&sup.SupportID, &sup.UnitID, &sup.CandidateID, &facts, &chain, &citations,
			&sup.IndexHash, &sup.CreatedBy, &sup.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lifetime/store: GetStaticSupport: %w", err)
	}
	if err := json.Unmarshal(facts, &sup.Facts); err != nil {
		return nil, fmt.Errorf("lifetime/store: GetStaticSupport: facts: %w", err)
	}
	if err := json.Unmarshal(chain, &sup.Chain); err != nil {
		return nil, fmt.Errorf("lifetime/store: GetStaticSupport: chain: %w", err)
	}
	if err := json.Unmarshal(citations, &sup.CitationChecks); err != nil {
		return nil, fmt.Errorf("lifetime/store: GetStaticSupport: citations: %w", err)
	}
	return &sup, nil
}
