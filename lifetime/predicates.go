package lifetime

// 本文件实现 §3.6 的 S1–S4 谓词、§3.7 的 confidence/对外门槛、§3.8 的平台与
// cross-seed 规则、§9.6/§12.4 的 limited control，以及 §10.2/§10.5 的复核独立
// 性与对外 finding 谓词。
//
// 共享契约没有 static.level 字段：S1 的 “static.level == complete” 由
// ltS1Static 结构化为“chain 非空、含 lifetime_point 与 access、节点带
// file:line”，再叠加 citation/索引条件。holder/domain/synchronization 的
// file:line 完整性由 S2 的 form_facts_complete 检查（§3.1 的其余部分）。
//
// §3.6 的 S2 还要求 hypothesis.level == testable，但共享契约没有该字段
// （只有 Gap / TriggerHypothesis），故本实现不重复检查；该门禁由平台在
// record_candidate 时执行。

import (
	"fmt"
	"strings"
)

// §14.6 的统计默认值。EvaluateS4 的签名不含 TaskProfile/StatisticalDefaults，
// 因此按文档默认值评估；profile 只能收紧、不能放宽（R-14.12）。
const (
	ltDefaultMinRuns            = 300
	ltDefaultMinReproLowerBound = 0.02
	ltDefaultCrossSeedMin       = 3
	ltDefaultCrossSeedFraction  = 0.10

	ltDeterministicReplayMinRuns = 20
)

// AssessmentEvaluation 是一次评估的派生结果。
type AssessmentEvaluation struct {
	Level              Level
	Provisional        bool
	Confidence         ConfidenceLevel
	ReportableInternal bool
	ReportableExternal bool
	Reasons            []string
}

// ltNodeLabel 返回 chain 节点在诊断信息中的稳定标签。
func ltNodeLabel(n EvidenceNode) string {
	if n.FactID != "" {
		return n.FactID
	}
	return fmt.Sprintf("%s:%d", n.File, n.Line)
}

// ltCitationVerified 报告 chain 节点是否有已验证的 mechanical citation。
//
// 共享契约的 CitationCheck 没有 fact_id 字段，因此无法按 fact_id 直接匹配；
// 本实现按 file + line + role 匹配（role 在节点或检查中为空时放宽）。节点带
// fact_id 时仍回退到该匹配方式。
func ltCitationVerified(n EvidenceNode, checks []CitationCheck) bool {
	for _, ch := range checks {
		if ch.Status != CitationVerified {
			continue
		}
		if ch.File != n.File || ch.Line != n.Line {
			continue
		}
		if n.Role != "" && ch.Role != "" && ch.Role != n.Role {
			continue
		}
		return true
	}
	return false
}

// ltS1Static 检查 §3.6 S1 中不依赖 IndexOutput 的部分：
//   - static.level == complete 的结构化代理：chain 非空、含 lifetime_point
//     与 access、所有节点带 file:line（§3.1/R-3.16）；
//   - chain 上所有节点有 verified citation。
func ltS1Static(c *Candidate, s *StaticSupport) (bool, []string) {
	var reasons []string
	if c == nil {
		return false, []string{"candidate is nil"}
	}
	if s == nil {
		return false, []string{"static support is nil"}
	}
	if len(s.Chain) == 0 {
		return false, []string{"static.level incomplete: evidence chain is empty"}
	}

	hasLifetimePoint, hasAccess := false, false
	for _, n := range s.Chain {
		switch n.Role {
		case NodeLifetimePoint:
			hasLifetimePoint = true
		case NodeAccess:
			hasAccess = true
		}
		if strings.TrimSpace(n.File) == "" || n.Line <= 0 {
			reasons = append(reasons,
				fmt.Sprintf("static.level incomplete: chain node %s lacks file:line", ltNodeLabel(n)))
		}
	}
	if !hasLifetimePoint {
		reasons = append(reasons, "static.level incomplete: chain has no lifetime_point (R-3.16)")
	}
	if !hasAccess {
		reasons = append(reasons, "static.level incomplete: chain has no access (R-3.16)")
	}
	for _, n := range s.Chain {
		if !ltCitationVerified(n, s.CitationChecks) {
			reasons = append(reasons,
				fmt.Sprintf("citation unresolved for chain node %s", ltNodeLabel(n)))
		}
	}
	return len(reasons) == 0, reasons
}

// EvaluateS1 实现 §3.6 的严格 S1 谓词。
func EvaluateS1(c *Candidate, s *StaticSupport, idx *IndexOutput) (bool, []string) {
	_, reasons := ltS1Static(c, s)
	if idx == nil {
		reasons = append(reasons, "index output is nil")
		return false, reasons
	}
	if idx.IndexQuality != "full" {
		reasons = append(reasons, fmt.Sprintf("index_quality=%q, want full", idx.IndexQuality))
	}
	if idx.TypeResolution != "exact" {
		reasons = append(reasons, fmt.Sprintf("type_resolution=%q, want exact", idx.TypeResolution))
	}
	return len(reasons) == 0, reasons
}

// EvaluateS1Provisional 实现 §3.6 的 S1_provisional：结构上足够（chain 含
// lifetime_point 与 access）且索引为 heuristic。它只能驱动 probe/发现，不能
// 作为 claim 等级（Assessment.Provisional 单独表达）。
func EvaluateS1Provisional(c *Candidate, s *StaticSupport, idx *IndexOutput) (bool, []string) {
	var reasons []string
	if c == nil {
		return false, []string{"candidate is nil"}
	}
	if s == nil {
		return false, []string{"static support is nil"}
	}
	if idx == nil {
		return false, []string{"index output is nil"}
	}
	if idx.IndexQuality != "heuristic" {
		reasons = append(reasons,
			fmt.Sprintf("index_quality=%q, want heuristic for provisional support", idx.IndexQuality))
	}
	if len(s.Chain) == 0 {
		reasons = append(reasons, "evidence chain is empty")
	}
	hasLifetimePoint, hasAccess := false, false
	for _, n := range s.Chain {
		switch n.Role {
		case NodeLifetimePoint:
			hasLifetimePoint = true
		case NodeAccess:
			hasAccess = true
		}
	}
	if !hasLifetimePoint {
		reasons = append(reasons, "chain has no lifetime_point")
	}
	if !hasAccess {
		reasons = append(reasons, "chain has no access")
	}
	return len(reasons) == 0, reasons
}

// EvaluateS2 实现 §3.6 的 S2 中不依赖 IndexOutput 的部分：
// S1 的静态子集（chain + citation）+ form_facts_complete + 四问规则 +
// contract_status != conflicting。完整的 S2 还必须同时满足 S1 的索引条件；
// EvaluateAssessment 会做该合取，调用方单独使用本函数时需自行保证索引条件。
func EvaluateS2(c *Candidate, s *StaticSupport) (bool, []string) {
	if c == nil {
		return false, []string{"candidate is nil"}
	}
	ok, reasons := ltS1Static(c, s)
	if !ok {
		return false, reasons
	}

	if complete, missing := FormFactsComplete(&c.CandidatePackage); !complete {
		reasons = append(reasons,
			"form_facts_complete=false: missing "+strings.Join(missing, ", "))
		ok = false
	}
	if err := ValidateFourQuestions(&c.CandidatePackage); err != nil {
		reasons = append(reasons, "four questions: "+err.Error())
		ok = false
	}
	if c.ContractStatus == ContractConflicting {
		reasons = append(reasons, "contract_status=conflicting is not allowed for S2 (R-12.4)")
		ok = false
	}
	return ok, reasons
}

// ltExperimentBelongsToCandidate 报告实验是否属于候选。空 ID 视为“未标注”
// 并放行，避免调用方只填一侧 ID 时产生假阴性。
func ltExperimentBelongsToCandidate(c *Candidate, e Experiment) bool {
	if c == nil {
		return false
	}
	if c.CandidateID == "" || e.CandidateID == "" {
		return true
	}
	return e.CandidateID == c.CandidateID
}

// ltS3Match 是一对满足 §3.6 S3 exists 谓词的 (experiment, observation)。
type ltS3Match struct {
	Experiment  Experiment
	Observation Observation
}

// ltObservationBelongsToExperiment 实现 obs 与 exp 的关联：
// ObservationIDs 非空时按 ID 匹配，否则按 obs.ExperimentID == exp.ExperimentID。
func ltObservationBelongsToExperiment(e Experiment, o Observation) bool {
	if len(e.ObservationIDs) == 0 {
		return o.ExperimentID == e.ExperimentID
	}
	for _, id := range e.ObservationIDs {
		if id == o.ObservationID {
			return true
		}
	}
	return false
}

// ltPlatformQualityRank 返回平台质量的单调序（low=1, medium=2, high=3）。
func ltPlatformQualityRank(q PlatformQuality) int {
	switch q {
	case PlatformQualityHigh:
		return 3
	case PlatformQualityMedium:
		return 2
	case PlatformQualityLow:
		return 1
	}
	return 0
}

// ltPlatformQualityAtLeast 报告 got 是否达到 want 的最低平台质量。
func ltPlatformQualityAtLeast(got, want PlatformQuality) bool {
	return ltPlatformQualityRank(got) >= ltPlatformQualityRank(want)
}

// ltPlatformInS3 报告平台是否在 policy.s3_allowed 中。
func ltPlatformInS3(p Platform, policy PlatformPolicy) bool {
	for _, allowed := range policy.S3Allowed {
		if allowed == p {
			return true
		}
	}
	return false
}

// ltS3ObservationFailures 返回某个 (exp, obs) 不满足 §3.6 S3 exists 谓词的
// 全部原因；空列表表示满足。
func ltS3ObservationFailures(c *Candidate, e Experiment, o Observation, policy PlatformPolicy) []string {
	var fails []string
	add := func(bad bool, reason string) {
		if bad {
			fails = append(fails, reason)
		}
	}

	add(e.Kind != ExperimentKindCandidate, fmt.Sprintf("experiment kind=%s, want candidate", e.Kind))
	add(e.State != ExperimentCompleted, fmt.Sprintf("experiment state=%s, want completed", e.State))
	add(e.ValidRuns < 1, fmt.Sprintf("valid_runs=%d, want >=1", e.ValidRuns))
	add(o.IsControl, "observation is_control=true")
	add(o.Binding != BindingMatched, fmt.Sprintf("binding=%s, want matched", o.Binding))
	add(o.BindingQuality != BindingQualityExact && o.BindingQuality != BindingQualityHeuristic,
		fmt.Sprintf("binding_quality=%s, want exact|heuristic (R-3.10)", o.BindingQuality))
	add(!o.Platform.Valid(), fmt.Sprintf("platform=%q is invalid", string(o.Platform)))
	add(o.Platform == PlatformForeign, "platform=foreign is analysis_only and cannot enter S3 (R-3.8)")
	add(!ltPlatformInS3(o.Platform, policy),
		fmt.Sprintf("platform=%s is not in policy.s3_allowed", o.Platform))
	add(!o.PlatformQuality.Valid(), fmt.Sprintf("platform_quality=%q is invalid", string(o.PlatformQuality)))
	add(!ltPlatformQualityAtLeast(o.PlatformQuality, policy.MinQuality),
		fmt.Sprintf("platform_quality=%s is below policy.min_quality=%s", o.PlatformQuality, policy.MinQuality))
	add(!o.Fidelity.Valid(), fmt.Sprintf("fidelity=%q is invalid", string(o.Fidelity)))
	add(!o.Fidelity.AtLeast(FormMinFidelity(c.PrimaryForm)),
		fmt.Sprintf("fidelity=%s is below form_min_fidelity(%s)=%s",
			o.Fidelity, c.PrimaryForm, FormMinFidelity(c.PrimaryForm)))
	add(!Eligible(c.PrimaryForm, o.EventType, o.Tool),
		fmt.Sprintf("(%s,%s) is not eligible for form %s (R-12.15)",
			o.EventType, o.Tool, c.PrimaryForm))
	add(o.EventType == EventRaceLead, "event_type=race_lead cannot enter S3 (R-3.9/R-3.15)")
	add(e.ContractCheck == ContractCheckViolated, "experiment contract_check=violated")
	add(c.ContractStatus == ContractConflicting, "candidate contract_status=conflicting (R-12.4)")
	add(!(c.ContractStatus == ContractKnown || e.ContractException),
		fmt.Sprintf("contract_status=%s and contract_exception=false; S3 needs known or exception (R-12.3)",
			c.ContractStatus))
	add(e.ControlResult == ControlResultFailed, "experiment control_result=failed")
	add(o.PairBasis != c.PairBasis,
		fmt.Sprintf("pair_basis=%s differs from candidate pair_basis=%s (R-9.24)",
			o.PairBasis, c.PairBasis))

	// F8 池化对象必须用 (instance_id, generation) 绑定，防止 ABA 混淆
	// （R-4.3/R-9.23）。
	if c.PrimaryForm == FormPoolABA && c.Family.StorageClass == StoragePool &&
		o.RuntimeContext.Generation == 0 {
		fails = append(fails,
			"F8 pool observation has generation=0; (instance_id, generation) binding is required (R-4.3/R-9.23)")
	}

	return fails
}

// ltS3Matches 返回所有满足 S3 exists 谓词的 (exp, obs) 对；没有时返回诊断原因。
func ltS3Matches(c *Candidate, exps []Experiment, obs []Observation, policy PlatformPolicy) ([]ltS3Match, []string) {
	if c == nil {
		return nil, []string{"candidate is nil"}
	}
	var matches []ltS3Match
	var reasons []string
	for _, e := range exps {
		if !ltExperimentBelongsToCandidate(c, e) {
			continue
		}
		if e.Kind != ExperimentKindCandidate {
			continue
		}
		for _, o := range obs {
			if !ltObservationBelongsToExperiment(e, o) {
				continue
			}
			// 观察若显式标注了别的 candidate，则不得用于本候选。
			if o.CandidateID != "" && c.CandidateID != "" && o.CandidateID != c.CandidateID {
				continue
			}
			fails := ltS3ObservationFailures(c, e, o, policy)
			if len(fails) == 0 {
				matches = append(matches, ltS3Match{Experiment: e, Observation: o})
				continue
			}
			reasons = append(reasons, fmt.Sprintf("experiment %s / observation %s: %s",
				e.ExperimentID, o.ObservationID, strings.Join(fails, "; ")))
		}
	}
	if len(matches) > 0 {
		return matches, nil
	}
	if len(reasons) == 0 {
		reasons = []string{"no candidate experiment with a linked observation to evaluate"}
	}
	return nil, reasons
}

// EvaluateS3 实现 §3.6 的 S3 exists 谓词（不重复 S2；S2 由调用方/评估器合取）。
func EvaluateS3(c *Candidate, exps []Experiment, obs []Observation, policy PlatformPolicy) (bool, []string) {
	matches, reasons := ltS3Matches(c, exps, obs, policy)
	if len(matches) > 0 {
		return true, nil
	}
	return false, reasons
}

// ltReproductionOK 实现 §3.6 S4 的复现条件（§14.5）：
//   - deterministic_replay: n >= 20，hits >= ceil(0.9*n)，replay artifact verified；
//   - stochastic_stable: n >= min_runs，cp_lower_bound_95 >= min_repro_lower_bound。
//
// 用整数比较 hits*10 >= n*9 避免浮点 ceil 误差。
func ltReproductionOK(r Reproduction) bool {
	switch r.Mode {
	case ReproDeterministicReplay:
		return r.N >= ltDeterministicReplayMinRuns &&
			r.Hits*10 >= r.N*9 &&
			r.ReplayArtifactVerified
	case ReproStochasticStable:
		return r.N >= ltDefaultMinRuns &&
			r.CPLowerBound95 >= ltDefaultMinReproLowerBound
	}
	return false
}

// ltControlExperimentOK 检查 S4 的控制条件（§3.6/§9.6）。
func ltControlExperimentOK(e Experiment) bool {
	return e.ControlResult == ControlResultPassed &&
		e.ControlKind == ControlKindTarget &&
		e.ControlPowerAdequate
}

// ltS4ControlExperimentOK 检查是否存在满足 S4 控制 + 复现条件的 candidate 实验。
func ltS4ControlExperimentOK(c *Candidate, exps []Experiment) bool {
	for _, e := range exps {
		if !ltExperimentBelongsToCandidate(c, e) || e.Kind != ExperimentKindCandidate {
			continue
		}
		if !ltControlExperimentOK(e) {
			continue
		}
		if ltReproductionOK(e.Reproduction) {
			return true
		}
	}
	return false
}

// EvaluateS4 实现 §3.6 的 S4 谓词 = S3 + 任一 candidate 实验满足
// control=passed/target/power_adequate 且复现条件成立。
func EvaluateS4(c *Candidate, exps []Experiment, obs []Observation, policy PlatformPolicy) (bool, []string) {
	s3, s3Reasons := EvaluateS3(c, exps, obs, policy)
	if !s3 {
		return false, append([]string{"S3 predicate not satisfied"}, s3Reasons...)
	}
	if ltS4ControlExperimentOK(c, exps) {
		return true, nil
	}
	return false, []string{
		"no candidate experiment with control_result=passed, control_kind=target, " +
			"control_power_adequate and an adequate reproduction mode",
	}
}

// PlatformExternalOK 实现 §3.8 platform_external_ok：
//
//	native   -> true
//	emulated -> allow_emulated_external && platform_quality == high
//	foreign  -> false
func PlatformExternalOK(o Observation, p PlatformPolicy) bool {
	switch o.Platform {
	case PlatformNative:
		return true
	case PlatformEmulated:
		return p.AllowEmulatedExternal && o.PlatformQuality == PlatformQualityHigh
	}
	return false
}

// CrossSeedOK 实现 §3.8 cross_seed_ok：
//
//	distinct_seeds_hit >= cross_seed_min
//	or (distinct_seeds_hit / planned_seeds >= cross_seed_fraction
//	    and valid_runs >= min_runs)
//
// StatisticalDefaults 中未填写的项回落到 §14.6 默认值。
func CrossSeedOK(e Experiment, sd StatisticalDefaults) bool {
	minSeeds := sd.CrossSeedMin
	if minSeeds <= 0 {
		minSeeds = ltDefaultCrossSeedMin
	}
	fraction := sd.CrossSeedFraction
	if fraction <= 0 {
		fraction = ltDefaultCrossSeedFraction
	}
	minRuns := sd.MinRuns
	if minRuns <= 0 {
		minRuns = ltDefaultMinRuns
	}

	if e.DistinctSeedsHit >= minSeeds {
		return true
	}
	if e.PlannedSeeds > 0 {
		rate := float64(e.DistinctSeedsHit) / float64(e.PlannedSeeds)
		if rate >= fraction && e.ValidRuns >= minRuns {
			return true
		}
	}
	return false
}

// LimitedControlAllowed 实现 §3.8 limited_control_allowed(form) = form 在
// profile.limited_control_forms 白名单中（R-3.7：默认空，只能显式白名单）。
func LimitedControlAllowed(f Form, p *TaskProfile) bool {
	if p == nil {
		return false
	}
	return ltFormsContain(f, p.LimitedControlForms)
}

// DeterministicOracleVerified 实现 §3.8 的确定性 oracle 判定，并叠加 R-3.13
// 的 meta_test 要求：
//   - oracle_type ∈ {api_contract, internal_consistency, resource_conservation,
//     state_machine}；
//   - oracle_evaluation_context ∈ {operation_site, locked, atomic_snapshot}；
//   - meta_test.serialized_pass && meta_test.trigger_fail（两边都失败或都不
//     成功 → 拒绝）。
func DeterministicOracleVerified(inv InvariantSpec) bool {
	if !inv.OracleType.Valid() {
		return false
	}
	switch inv.OracleEvaluationContext {
	case OracleAtOperationSite, OracleLocked, OracleAtomicSnapshot:
	default:
		return false
	}
	return inv.MetaTest.SerializedPass && inv.MetaTest.TriggerFail
}

// DeriveConfidence 实现 §3.7 confidence 表：
//   - high:   S4 + review confirmed + 对外门槛；
//   - medium: S3（含 S4）+ review confirmed；
//   - low:    其他。
func DeriveConfidence(level Level, review Review, external bool) ConfidenceLevel {
	if level == LevelS4 && review.Verdict == VerdictConfirmed && external {
		return ConfidenceHigh
	}
	if level.AtLeast(LevelS3) && review.Verdict == VerdictConfirmed {
		return ConfidenceMedium
	}
	return ConfidenceLow
}

// ReviewIndependence 按 §10.2 从 provenance 派生复核独立性：
//   - 独立重跑同一 driver → independent_reproduction；
//   - reviewer 写新驱动（reviewer 不是原 driver 作者/runner）→ new_driver_reviewer；
//   - 原 verifier/runner 写新驱动 → new_driver_same_verifier；
//   - 没有新驱动 → same_driver（只能复核 S2/解释 S3，不能对外）。
func ReviewIndependence(driverAuthor, jobRunner, reviewer string, newDriver bool, independentRepro bool) Independence {
	if independentRepro {
		return IndependenceIndependentRepro
	}
	if newDriver {
		if reviewer == driverAuthor || reviewer == jobRunner {
			return IndependenceNewDriverSameVerifier
		}
		return IndependenceNewDriverReviewer
	}
	return IndependenceSameDriver
}

// ltHasExternalSupportingMatch 报告是否存在同时满足 binding_quality=exact 与
// platform_external_ok 的 S3 支持观察（§3.7/G-7.6/G-7.7）。
func ltHasExternalSupportingMatch(matches []ltS3Match, policy PlatformPolicy) bool {
	for _, m := range matches {
		if m.Observation.BindingQuality == BindingQualityExact &&
			PlatformExternalOK(m.Observation, policy) {
			return true
		}
	}
	return false
}

// ltAnyExternalControlExperiment 检查 §3.7 的 “S3 + control passed/target/
// power_adequate + cross_seed_ok” 路径。
func ltAnyExternalControlExperiment(c *Candidate, exps []Experiment, profile *TaskProfile) bool {
	sd := StatisticalDefaults{}
	if profile != nil {
		sd = profile.StatisticalDefaults
	}
	for _, e := range exps {
		if !ltExperimentBelongsToCandidate(c, e) || e.Kind != ExperimentKindCandidate {
			continue
		}
		if !ltControlExperimentOK(e) {
			continue
		}
		if CrossSeedOK(e, sd) {
			return true
		}
	}
	return false
}

// ltLimitedControlGate 检查 §3.7 的 limited-control 对外路径：
// S3 + control=not_applicable + primary_form ∈ limited_control_forms +
// 确定性 oracle（R-3.7/R-9.28）。
//
// 共享契约没有把 DriverSpec.invariants 传入评估；本实现用“S3 支持观察是
// invariant 且带预注册 invariant_id”作为确定性 oracle 的结构性证据
// （R-3.11/R-3.14：invariant 编译时嵌入，不能事后补）。
func ltLimitedControlGate(c *Candidate, matches []ltS3Match, exps []Experiment, profile *TaskProfile) (bool, []string) {
	var reasons []string
	if !LimitedControlAllowed(c.PrimaryForm, profile) {
		reasons = append(reasons,
			fmt.Sprintf("form %s is not in profile.limited_control_forms (R-3.7)", c.PrimaryForm))
	}
	oracle := false
	for _, m := range matches {
		if m.Observation.EventType == EventInvariant && m.Observation.InvariantID != "" {
			oracle = true
			break
		}
	}
	if !oracle {
		reasons = append(reasons,
			"no S3 supporting invariant observation with invariant_id for deterministic oracle")
	}
	notApplicable := false
	for _, e := range exps {
		if !ltExperimentBelongsToCandidate(c, e) || e.Kind != ExperimentKindCandidate {
			continue
		}
		if e.ControlResult == ControlResultNotApplicable {
			notApplicable = true
			break
		}
	}
	if !notApplicable {
		reasons = append(reasons, "no candidate experiment with control_result=not_applicable")
	}
	return len(reasons) == 0, reasons
}

// ltExternalGate 实现 §3.7 的对外 finding 门槛。
func ltExternalGate(c *Candidate, matches []ltS3Match, exps []Experiment,
	policy PlatformPolicy, profile *TaskProfile, review Review, s4 bool) (bool, []string) {

	var reasons []string
	if review.Verdict != VerdictConfirmed {
		reasons = append(reasons,
			fmt.Sprintf("review.verdict=%q, want confirmed (G-7.5)", review.Verdict))
	}
	if c.ContractStatus != ContractKnown {
		reasons = append(reasons,
			fmt.Sprintf("contract_status=%s, want known for external finding (R-12.3)", c.ContractStatus))
	}
	// citations verified 已由 S1 保证（S3 ⇒ S1 在 EvaluateAssessment 中合取）。
	if !ltHasExternalSupportingMatch(matches, policy) {
		reasons = append(reasons,
			"no S3 supporting observation with binding_quality=exact and platform_external_ok (G-7.6/G-7.7)")
	}

	allowed := s4
	if !allowed {
		allowed = ltAnyExternalControlExperiment(c, exps, profile)
	}
	if !allowed {
		limited, limitedReasons := ltLimitedControlGate(c, matches, exps, profile)
		allowed = limited
		if !limited {
			reasons = append(reasons, limitedReasons...)
		}
	}
	if !allowed && len(reasons) == 0 {
		reasons = append(reasons,
			"no external path: need S4, S3+target control+cross_seed, or limited-control+deterministic oracle")
	}
	return len(reasons) == 0, reasons
}

// EvaluateAssessment 从 S1→S4 逐级评估并派生 assessment 字段。
//
// 合取关系：S2 = S1 ∧ S2_static；S3 = S2 ∧ ∃(exp,obs)；S4 = S3 ∧ 控制/复现。
// ReportableInternal = S3（refuted/stale 由调用方保证未发生）。
// ReportableExternal = S3/S4 ∧ §3.7 门槛。
func EvaluateAssessment(c *Candidate, s *StaticSupport, idx *IndexOutput,
	exps []Experiment, obs []Observation, policy PlatformPolicy,
	profile *TaskProfile, review Review) AssessmentEvaluation {

	ev := AssessmentEvaluation{}
	if c == nil {
		ev.Confidence = DeriveConfidence(ev.Level, review, false)
		ev.Reasons = []string{"candidate is nil"}
		return ev
	}

	s1, s1Reasons := EvaluateS1(c, s, idx)
	provisional, _ := EvaluateS1Provisional(c, s, idx)
	s2Static, s2Reasons := EvaluateS2(c, s)
	s2 := s1 && s2Static

	matches, s3Reasons := ltS3Matches(c, exps, obs, policy)
	s3 := s2 && len(matches) > 0
	s4 := s3 && ltS4ControlExperimentOK(c, exps)

	switch {
	case s4:
		ev.Level = LevelS4
	case s3:
		ev.Level = LevelS3
	case s2:
		ev.Level = LevelS2
	case s1:
		ev.Level = LevelS1
	default:
		ev.Level = Level("")
	}
	ev.Provisional = provisional
	ev.ReportableInternal = s3

	switch {
	case !s1:
		ev.Reasons = append(ev.Reasons, "S1 not satisfied")
		ev.Reasons = append(ev.Reasons, s1Reasons...)
		if provisional {
			ev.Reasons = append(ev.Reasons,
				"S1_provisional satisfied: probe/new-lead only, not a claim level")
		}
	case !s2Static:
		ev.Reasons = append(ev.Reasons, "S2 not satisfied")
		ev.Reasons = append(ev.Reasons, s2Reasons...)
	case !s3:
		ev.Reasons = append(ev.Reasons, "S3 not satisfied")
		ev.Reasons = append(ev.Reasons, s3Reasons...)
	case !s4:
		ev.Reasons = append(ev.Reasons, "S4 not satisfied: control/reproduction condition missing")
	}

	if s3 {
		external, extReasons := ltExternalGate(c, matches, exps, policy, profile, review, s4)
		ev.ReportableExternal = external
		if !external {
			ev.Reasons = append(ev.Reasons, extReasons...)
		}
	}

	ev.Confidence = DeriveConfidence(ev.Level, review, ev.ReportableExternal)
	return ev
}

// ExternalFindingPredicate 实现 §10.5 的完整对外 finding 谓词：
// primary 为 S3/S4 claim、review confirmed，并且满足
//   - confirmation lineage：同 candidate_key、confirmation_of=primary.lineage_id、
//     level ∈ {S3,S4}、review confirmed、independence=new_driver_reviewer；或
//   - independent_reproduction：reviewer session 不是 driver 作者，且
//     claim experiment 的 replay artifact 已验证。
func ExternalFindingPredicate(primary Assessment, all []Assessment, claimExp *Experiment) bool {
	if !primary.Level.AtLeast(LevelS3) {
		return false
	}
	if primary.LineageRole != LineageRoleClaim {
		return false
	}
	if primary.Review.Verdict != VerdictConfirmed {
		return false
	}

	for _, conf := range all {
		if conf.CandidateKey != primary.CandidateKey {
			continue
		}
		if conf.LineageRole != LineageRoleConfirmation {
			continue
		}
		if conf.ConfirmationOf != primary.LineageID {
			continue
		}
		if !conf.Level.AtLeast(LevelS3) {
			continue
		}
		if conf.Review.Verdict != VerdictConfirmed {
			continue
		}
		if conf.Review.Independence != IndependenceNewDriverReviewer {
			continue
		}
		return true
	}

	if primary.Review.Independence == IndependenceIndependentRepro &&
		primary.Review.ReviewerSessionID != primary.Provenance.DriverAuthorSession &&
		claimExp != nil &&
		claimExp.Reproduction.ReplayArtifactVerified {
		return true
	}

	return false
}
