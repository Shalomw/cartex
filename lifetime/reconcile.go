package lifetime

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// 本文件实现 §6.10 的对账环 C1–C12。RunChecks 只读取 ReconcileSnapshot，
// 返回确定性发现；不写库、不调用 LLM，也不修改 evidence/level/reportable
// （R-6.28）。

const (
	// reconcileStallTTL 是 C3/C4 的停滞阈值。§14.6 的 Q2/Q3 默认 lease TTL
	// 均为 60 min；快照不携带 profile，故使用该常量。
	reconcileStallTTL = 60 * time.Minute
	// reconcileStarvationTTL 是 C7 的饿死阈值（§14.6 cold_after=60 min）。
	reconcileStarvationTTL = 60 * time.Minute
	// reconcileDefaultMaxAttempts 是 R-6.12 默认 attempts 上限。
	reconcileDefaultMaxAttempts = 3
)

// reconcileSeverityRank 返回严重度序（low=1 .. critical=4），用于 ">=" 比较。
func reconcileSeverityRank(s ReconciliationSeverity) int {
	switch s {
	case SeverityLow:
		return 1
	case SeverityMedium:
		return 2
	case SeverityHigh:
		return 3
	case SeverityCritical:
		return 4
	}
	return 0
}

// DedupKey 实现 R-6.29：hash(check_id, subject_ref, expected_state)。
func DedupKey(check CheckID, subjectRef, expected string) string {
	return lifetimeLedgerHash("reconciliation", string(check), subjectRef, expected)
}

// reconcileRun 收集一次 sweep 的发现，并按 dedup_key 去重（同一 sweep 内
// 同一问题只生成一条发现）。
type reconcileRun struct {
	s           ReconcileSnapshot
	now         time.Time
	maxAttempts int
	findings    []ReconciliationFinding
	seen        map[string]bool
}

func (r *reconcileRun) add(check CheckID, sev ReconciliationSeverity, subjectKind, subjectRef, expected, actual string, action ReconciliationAction) {
	key := DedupKey(check, subjectRef, expected)
	if r.seen[key] {
		return
	}
	r.seen[key] = true
	r.findings = append(r.findings, ReconciliationFinding{
		CheckID:     string(check),
		Severity:    sev,
		SubjectKind: subjectKind,
		SubjectRef:  subjectRef,
		Expected:    expected,
		Actual:      actual,
		Action:      action,
		Status:      ReconOpen,
		DedupKey:    key,
		CreatedAt:   r.now,
	})
}

// RunChecks 执行 C1–C12，返回 open 状态的对账发现。CreatedAt 取 s.Now；
// maxAttempts<=0 时使用默认值 3（R-6.12）。
func RunChecks(s ReconcileSnapshot, maxAttempts int) []ReconciliationFinding {
	if maxAttempts <= 0 {
		maxAttempts = reconcileDefaultMaxAttempts
	}
	r := &reconcileRun{
		s:           s,
		now:         s.Now,
		maxAttempts: maxAttempts,
		seen:        make(map[string]bool),
	}
	r.checkC1()
	r.checkC2()
	r.checkC3()
	r.checkC4()
	r.checkC5()
	r.checkC6()
	r.checkC7()
	r.checkC8()
	r.checkC9()
	r.checkC10()
	r.checkC11()
	r.checkC12()
	return r.findings
}

// checkC1 覆盖完整性：每个 enumerated unit 必须有可对应的账本行。
// 快照以 CoverageUnit 同时承载枚举项与账本行；Status 为空/非法表示该
// enumerated unit 尚无账本行，UnitID 与字段重算不一致表示行缺失或错键。
func (r *reconcileRun) checkC1() {
	for _, u := range r.s.EnumeratedUnits {
		// C1 compares the persisted ID against the identity recomputed from the
		// enumerated fields; coverageUnitRef would trust a wrong persisted ID.
		ref := UnitID(u.UnitKind, u.FamilyID, u.RelationID, u.DomainPairID, u.Form)
		if u.UnitID == "" {
			r.add(CheckC1, SeverityMedium, "coverage_unit", ref,
				"ledger_row_present", "missing_unit_id", ActionCreateRepairTask)
			continue
		}
		if !u.Status.Valid() {
			r.add(CheckC1, SeverityMedium, "coverage_unit", ref,
				"ledger_row_present", fmt.Sprintf("invalid_status=%q", u.Status), ActionCreateRepairTask)
			continue
		}
		if u.UnitID != ref {
			r.add(CheckC1, SeverityMedium, "coverage_unit", ref,
				"ledger_row_present", "unit_id_mismatch", ActionCreateRepairTask)
		}
	}
}

// checkC2 收尾候选：快照没有显式 closing 标志，只在预算耗尽（收尾语境）
// 且仍存在 candidate_open 时报告，避免每轮误报。
func (r *reconcileRun) checkC2() {
	if !r.s.BudgetExhausted {
		return
	}
	for _, u := range r.s.EnumeratedUnits {
		if u.Status != CoverageCandidateOpen {
			continue
		}
		r.add(CheckC2, SeverityHigh, "coverage_unit", coverageUnitRef(u),
			"candidate_open_resolved_at_close", "candidate_open", ActionNotifyPlanner)
	}
}

// checkC3 candidate 停滞：S2 且超过 TTL 无 verify lease → requeue。
// WorkItem 不携带 Q2 mode，因此任何该 candidate 的 active Q2 claim 都视为
// 已有 verify lease（避免误报）。
func (r *reconcileRun) checkC3() {
	verifyLeased := make(map[string]bool)
	for _, it := range r.s.WorkItems {
		if it.QueueKind != QueueCandidateReadyVerify || it.State != WorkItemClaimed {
			continue
		}
		if !it.LeaseExpiresAt.After(r.now) {
			continue
		}
		verifyLeased[it.ItemRef] = true
	}
	candidates := make(map[string]Candidate, len(r.s.Candidates))
	for _, c := range r.s.Candidates {
		candidates[c.CandidateID] = c
	}
	reported := make(map[string]bool)
	for _, a := range r.s.Assessments {
		if a.Level != LevelS2 || a.Status != AssessmentOpen || a.Stale {
			continue
		}
		c, ok := candidates[a.CandidateID]
		if !ok || verifyLeased[a.CandidateID] || reported[a.CandidateID] {
			continue
		}
		base := c.CreatedAt
		if base.IsZero() {
			base = a.DerivedAt
		}
		if base.IsZero() || r.now.Sub(base) < reconcileStallTTL {
			continue
		}
		reported[a.CandidateID] = true
		r.add(CheckC3, SeverityMedium, "candidate", a.CandidateID,
			"verify_lease_within_60m", "no_verify_lease", ActionRequeue)
	}
}

// checkC4 claim 停滞：S3 claim 超过 TTL 无 review lease → requeue。
func (r *reconcileRun) checkC4() {
	reviewLeased := make(map[string]bool)
	for _, it := range r.s.WorkItems {
		if it.QueueKind != QueueClaimReadyReview || it.State != WorkItemClaimed {
			continue
		}
		if !it.LeaseExpiresAt.After(r.now) {
			continue
		}
		reviewLeased[it.ItemRef] = true
	}
	for _, a := range r.s.Assessments {
		if a.Level != LevelS3 || a.LineageRole != LineageRoleClaim {
			continue
		}
		if a.Stale || a.Status != AssessmentOpen || a.Review.Verdict == VerdictConfirmed {
			continue
		}
		if reviewLeased[a.AssessmentID] {
			continue
		}
		if a.DerivedAt.IsZero() || r.now.Sub(a.DerivedAt) < reconcileStallTTL {
			continue
		}
		r.add(CheckC4, SeverityMedium, "assessment", a.AssessmentID,
			"review_lease_within_60m", "no_review_lease", ActionRequeue)
	}
}

// checkC5 lineage stale：assessment/experiment/coverage 行依赖旧 lineage →
// mark_stale。Observation 没有 lineage_id 字段，其 freshness 通过
// experiment 关联间接覆盖。
func (r *reconcileRun) checkC5() {
	if r.s.LineageID == "" {
		return
	}
	want := "lineage=" + r.s.LineageID
	for _, a := range r.s.Assessments {
		if a.LineageID == r.s.LineageID {
			continue
		}
		r.add(CheckC5, SeverityHigh, "assessment", a.AssessmentID,
			want, "lineage="+a.LineageID, ActionMarkStale)
	}
	for _, e := range r.s.Experiments {
		if e.LineageID == r.s.LineageID {
			continue
		}
		r.add(CheckC5, SeverityHigh, "experiment", e.ExperimentID,
			want, "lineage="+e.LineageID, ActionMarkStale)
	}
	for _, u := range r.s.EnumeratedUnits {
		if u.LineageID == r.s.LineageID {
			continue
		}
		r.add(CheckC5, SeverityHigh, "coverage_unit", coverageUnitRef(u),
			want, "lineage="+u.LineageID, ActionMarkStale)
	}
}

// checkC6 control 缺失：S3 assessment 对应的 experiments 中必须有
// control 实验且结果为 passed/not_applicable；缺失 → create_repair_task，
// 失败 → mark_stale。
func (r *reconcileRun) checkC6() {
	for _, a := range r.s.Assessments {
		if a.Level != LevelS3 {
			continue
		}
		if a.Status == AssessmentRefuted || a.Status == AssessmentSuperseded {
			continue
		}
		hasControl := false
		controlOK := false
		for _, e := range r.s.Experiments {
			if e.CandidateID != a.CandidateID && (a.ClaimExperimentID == "" || e.ExperimentID != a.ClaimExperimentID) {
				continue
			}
			if e.Kind == ExperimentKindControl {
				hasControl = true
			}
			switch e.ControlResult {
			case ControlResultPassed, ControlResultNotApplicable:
				// 控制结果也可能记录在 claim 实验行上（Experiment.ControlResult）。
				hasControl = true
				controlOK = true
			case ControlResultFailed:
				hasControl = true
			}
		}
		switch {
		case !hasControl:
			r.add(CheckC6, SeverityHigh, "assessment", a.AssessmentID,
				"control_present", "control_missing", ActionCreateRepairTask)
		case !controlOK:
			r.add(CheckC6, SeverityHigh, "assessment", a.AssessmentID,
				"control_passed", "control_failed", ActionMarkStale)
		}
	}
}

// checkC7 队列健康：过期 claim / orphan claim / 重复 ready dedup_key /
// 饿死 / 超 attempts 上限仍 ready。所有遍历按 item_id 排序保证确定性。
func (r *reconcileRun) checkC7() {
	items := append([]WorkItem(nil), r.s.WorkItems...)
	sort.Slice(items, func(i, j int) bool { return items[i].ItemID < items[j].ItemID })

	readyByDedup := make(map[string][]string)
	for _, it := range items {
		switch it.State {
		case WorkItemClaimed:
			if !it.LeaseExpiresAt.After(r.now) {
				r.add(CheckC7, SeverityMedium, "work_item", it.ItemID,
					"lease_active_or_released", "lease_expired", ActionRequeue)
			}
			if it.ClaimedBySession == "" || it.ClaimID == "" {
				r.add(CheckC7, SeverityHigh, "work_item", it.ItemID,
					"claim_owner_present", "orphan_claim", ActionRequeue)
			}
		case WorkItemReady:
			if it.DedupKey != "" {
				readyByDedup[it.DedupKey] = append(readyByDedup[it.DedupKey], it.ItemID)
			}
			if !it.CreatedAt.IsZero() && r.now.Sub(it.CreatedAt) >= reconcileStarvationTTL {
				r.add(CheckC7, SeverityMedium, "work_item", it.ItemID,
					"claimed_within_60m", "starvation_ready_unclaimed", ActionNotifyPlanner)
			}
			if r.maxAttempts > 0 && it.Attempts >= r.maxAttempts {
				r.add(CheckC7, SeverityHigh, "work_item", it.ItemID,
					"attempts_below_max", fmt.Sprintf("attempts=%d", it.Attempts), ActionNotifyPlanner)
			}
		}
	}

	dedupKeys := make([]string, 0, len(readyByDedup))
	for key := range readyByDedup {
		dedupKeys = append(dedupKeys, key)
	}
	sort.Strings(dedupKeys)
	for _, key := range dedupKeys {
		ids := readyByDedup[key]
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		for _, id := range ids[1:] {
			r.add(CheckC7, SeverityMedium, "work_item", id,
				"unique_ready_dedup_key", "duplicate_ready_dedup_key="+key, ActionRequeue)
		}
	}
}

// checkC8 no_risk 不完整：covers_current_form=false，或论证不覆盖当前
// form，或同一 no_risk 的各 argument access_set 不一致（平台完整 access
// set 的比对由 ValidateNoRiskAccessSet 在写入时完成；快照不携带索引
// access set，因此对账只能检查内部一致性）。
func (r *reconcileRun) checkC8() {
	for _, n := range r.s.NoRisks {
		if !n.CoversCurrentForm {
			r.add(CheckC8, SeverityMedium, "no_risk", n.NoRiskID,
				"covers_current_form_true", "covers_current_form_false", ActionCreateRepairTask)
		} else if !NoRiskCoversForm(n, n.Form) {
			r.add(CheckC8, SeverityMedium, "no_risk", n.NoRiskID,
				"arguments_cover_current_form", "arguments_missing_form_coverage", ActionCreateRepairTask)
		}
		if len(n.Arguments) < 2 {
			continue
		}
		want := AccessSetHash(n.Arguments[0].AccessSet)
		for i := 1; i < len(n.Arguments); i++ {
			if AccessSetHash(n.Arguments[i].AccessSet) == want {
				continue
			}
			r.add(CheckC8, SeverityMedium, "no_risk", n.NoRiskID,
				"uniform_access_set", fmt.Sprintf("argument_%d_access_set_mismatch", i), ActionCreateRepairTask)
			break
		}
	}
}

// checkC9 report gate 不一致：confirmed+reportable_external 但无 finding；
// 或 finding 已存在但没有 confirmed assessment。
func (r *reconcileRun) checkC9() {
	confirmed := make(map[string]bool)
	for _, a := range r.s.Assessments {
		if a.Review.Verdict != VerdictConfirmed {
			continue
		}
		confirmed[a.CandidateID] = true
		if a.ReportableExternal && !r.s.FindingExists[a.CandidateID] {
			r.add(CheckC9, SeverityHigh, "candidate", a.CandidateID,
				"finding_exists_for_confirmed_candidate", "finding_missing", ActionCreateRepairTask)
		}
	}
	findingIDs := make([]string, 0, len(r.s.FindingExists))
	for id, exists := range r.s.FindingExists {
		if exists {
			findingIDs = append(findingIDs, id)
		}
	}
	sort.Strings(findingIDs)
	for _, id := range findingIDs {
		if confirmed[id] {
			continue
		}
		r.add(CheckC9, SeverityMedium, "candidate", id,
			"confirmed_assessment_for_finding", "confirmed_assessment_missing", ActionCreateRepairTask)
	}
}

// checkC10 预算收尾：预算耗尽但仍有不在 R-7.11 收尾状态集合中的 unit →
// notify_planner。候选 open 同时会触发 C2，便于 planner 区分。
func (r *reconcileRun) checkC10() {
	if !r.s.BudgetExhausted {
		return
	}
	for _, u := range r.s.EnumeratedUnits {
		if coverageClosingStatus(u.Status) {
			continue
		}
		r.add(CheckC10, SeverityHigh, "coverage_unit", coverageUnitRef(u),
			"deferred_budget_at_budget_exhaustion", "status="+string(u.Status), ActionNotifyPlanner)
	}
}

// checkC11 重复候选：同 candidate_key 或 mechanism_key 出现不同 candidate_id
// 且未合并 → merge。
func (r *reconcileRun) checkC11() {
	byCandidateKey := make(map[string]map[string]bool)
	byMechanismKey := make(map[string]map[string]bool)
	for _, c := range r.s.Candidates {
		if c.CandidateKey != "" {
			if byCandidateKey[c.CandidateKey] == nil {
				byCandidateKey[c.CandidateKey] = make(map[string]bool)
			}
			byCandidateKey[c.CandidateKey][c.CandidateID] = true
		}
		if c.MechanismKey != "" {
			if byMechanismKey[c.MechanismKey] == nil {
				byMechanismKey[c.MechanismKey] = make(map[string]bool)
			}
			byMechanismKey[c.MechanismKey][c.CandidateID] = true
		}
	}
	r.checkC11Group(byCandidateKey, "single_candidate_for_candidate_key", "candidate_key")
	r.checkC11Group(byMechanismKey, "single_candidate_for_mechanism_key", "mechanism_key")
}

func (r *reconcileRun) checkC11Group(groups map[string]map[string]bool, expected, keyName string) {
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ids := make([]string, 0, len(groups[key]))
		for id := range groups[key] {
			ids = append(ids, id)
		}
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		r.add(CheckC11, SeverityMedium, "candidate", strings.Join(ids, ","),
			expected, keyName+"="+key, ActionMerge)
	}
}

// checkC12 owner 记忆 stale：entry 引用旧 lineage 实体，或 entry 未标 stale
// 但缺少 source_hash/index_hash（无法证明其仍对应当前 lineage）。快照不携带
// 预期的 source/index hash，故只能做引用与完整性判断。
func (r *reconcileRun) checkC12() {
	staleRefs := make(map[string]bool)
	if r.s.LineageID != "" {
		for _, a := range r.s.Assessments {
			if a.LineageID != r.s.LineageID {
				staleRefs[a.AssessmentID] = true
			}
		}
		for _, e := range r.s.Experiments {
			if e.LineageID != r.s.LineageID {
				staleRefs[e.ExperimentID] = true
			}
		}
		for _, u := range r.s.EnumeratedUnits {
			if u.LineageID != r.s.LineageID {
				staleRefs[coverageUnitRef(u)] = true
			}
		}
	}
	entries := append([]OwnerJournalEntry(nil), r.s.OwnerJournal...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].EntryID < entries[j].EntryID })
	for _, e := range entries {
		if e.Stale {
			continue
		}
		actual := ""
		for _, ref := range e.Refs {
			if staleRefs[ref] {
				actual = "refs_stale_lineage"
				break
			}
		}
		if actual == "" && (e.SourceHash == "" || e.IndexHash == "") {
			actual = "journal_source_or_index_hash_missing"
		}
		if actual == "" {
			continue
		}
		r.add(CheckC12, SeverityLow, "owner_journal", e.EntryID,
			"entry_current_for_lineage", actual, ActionMarkStale)
	}
}

// DedupAgainstExisting 过滤 dedup_key 已存在的发现（R-6.29）。空 dedup_key
// 视为不可去重，保留原样；返回顺序与输入一致。
func DedupAgainstExisting(findings []ReconciliationFinding, existing []string) []ReconciliationFinding {
	if len(existing) == 0 {
		return findings
	}
	seen := make(map[string]bool, len(existing))
	for _, key := range existing {
		if key != "" {
			seen[key] = true
		}
	}
	out := make([]ReconciliationFinding, 0, len(findings))
	for _, f := range findings {
		if f.DedupKey != "" && seen[f.DedupKey] {
			continue
		}
		out = append(out, f)
	}
	return out
}
