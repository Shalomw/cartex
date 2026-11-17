package lifetime

// 本文件实现 §5.6 TaskProfile 的默认值与校验（R-5.13–R-5.16、R-14.12）。
// TaskProfile 只能参数化 §5.6 列出的项；S1–S4、状态集合、覆盖单位、
// 所有权与角色集合属于 CoreContract，不得由 profile 改变（R-5.13）。

import (
	"fmt"
	"time"
)

// profileDefaultName / profileDefaultVersion 是 DefaultTaskProfile 的身份。
// name/version 是 profile 的身份而非可补零的参数，ApplyTaskProfileDefaults
// 不会为它们填值；ValidateTaskProfile 要求显式非空。
const (
	profileDefaultName    = "lifetime-default"
	profileDefaultVersion = "1.0"
)

// profileAllForms 返回 F1–F8 的新切片（调用方可安全修改）。
func profileAllForms() []Form {
	return []Form{
		FormRefcountRace,
		FormDanglingThis,
		FormRegisterLeak,
		FormContainerInvalid,
		FormStaticDestroy,
		FormSharedPtrRace,
		FormLazyInitRace,
		FormPoolABA,
	}
}

// profileAllQueues 返回 Q1–Q5 的新切片。
func profileAllQueues() []QueueKind {
	return []QueueKind{
		QueueUnitReadyDiscover,
		QueueCandidateReadyVerify,
		QueueClaimReadyReview,
		QueueConfirmedReadyReport,
		QueueRepairTask,
	}
}

// profileDefaultStats 是 §14.6 的统计默认值，同时也是 R-5.15 的下限。
func profileDefaultStats() StatisticalDefaults {
	return StatisticalDefaults{
		MinRuns:            300,
		TargetUpperBound:   0.01,
		ControlUpperBound:  0.01,
		MinControlRuns:     300,
		MinReproLowerBound: 0.02,
		CrossSeedMin:       3,
		CrossSeedFraction:  0.10,
		Alpha:              0.05,
		Beta:               0.20,
		P0:                 0.01,
		P1:                 0.10,
	}
}

// profileDefaultLeaseTTL 是 §14.6 的队列租约默认值。
func profileDefaultLeaseTTL() map[QueueKind]time.Duration {
	return map[QueueKind]time.Duration{
		QueueUnitReadyDiscover:    30 * time.Minute,
		QueueCandidateReadyVerify: 60 * time.Minute,
		QueueClaimReadyReview:     60 * time.Minute,
		QueueConfirmedReadyReport: 15 * time.Minute,
		QueueRepairTask:           30 * time.Minute,
	}
}

// profileDefaultWeights 是 §6.6 优先级公式的权重默认值（全部 1.0）。
func profileDefaultWeights() PriorityWeights {
	return PriorityWeights{
		Risk:        1.0,
		Novelty:     1.0,
		ExpectedHit: 1.0,
		Cost:        1.0,
		Age:         1.0,
		Cold:        1.0,
	}
}

// profileDefaultRoleCapacity 是 §6.7 per_role_capacity 的实现默认值。
// 需求给定 auditor/verifier/reviewer 各 1–4、reporter/owner/reconciler 各 1–4；
// 这里取：auditor=2、verifier=2、reviewer=2、reporter=1、owner=2、reconciler=1。
// planner/mainagent 是控制面角色，不参与工作队列并发配额。
func profileDefaultRoleCapacity() map[Role]int {
	return map[Role]int{
		RoleLifetimeAuditor:  2,
		RoleLifetimeVerifier: 2,
		RoleLifetimeReviewer: 2,
		RoleReporter:         1,
		RoleOwner:            2,
		RoleReconciler:       1,
	}
}

// profileDefaultStatsFor 返回每个字段的默认值，供 ApplyTaskProfileDefaults
// 对部分填写的结构逐字段补零。
var profileStatDefaults = profileDefaultStats()

// profileDefaultRoleCapacities 避免每次调用重建 map 时被调用方修改默认值；
// Apply 总是复制后返回。
var profileDefaultRoleCapacities = profileDefaultRoleCapacity()

// StatisticalFloor 返回 §14.6 的下限（R-5.15）。
// 方向约定：
//   - max_* / upper_bound / alpha / beta / p0 / p1 是“越小越严格”，
//     合法值必须 <= floor；
//   - min_* / cross_seed_* 是“越大越严格”，合法值必须 >= floor。
//
// floor 与默认值数值相同：默认值本身就是允许的最宽松配置。
func StatisticalFloor() StatisticalDefaults {
	return profileDefaultStats()
}

// DefaultTaskProfile 返回可直接通过 ValidateTaskProfile 的 lifetime 默认 profile：
// forms=F1–F8、队列 Q1–Q5、§14.6 统计/租约/对账默认值、hybrid 调度、
// owner=summary、public_api 枚举范围、priority weights 全 1.0、
// scheduler_seed=0、limited_control_forms 为空（R-3.7）。
func DefaultTaskProfile() *TaskProfile {
	p := &TaskProfile{
		Name:    profileDefaultName,
		Version: profileDefaultVersion,
	}
	ApplyTaskProfileDefaults(p)
	return p
}

// ApplyTaskProfileDefaults 为零值字段补默认值，不覆盖显式值。
// 说明：
//   - name/version 是身份，不补默认；
//   - limited_control_forms 默认空（R-3.7）；
//   - form_facts、evidence_eligibility 默认 nil（不增加任何约束）；
//   - ReconciliationCadence.FullDaily 为 bool，无法区分“显式 false”与
//     零值；按 §14.6 默认 daily full，因此 false 会被补成 true。
func ApplyTaskProfileDefaults(p *TaskProfile) {
	if p == nil {
		return
	}

	if len(p.Forms) == 0 {
		p.Forms = profileAllForms()
	}

	sd := &p.StatisticalDefaults
	if sd.MinRuns == 0 {
		sd.MinRuns = profileStatDefaults.MinRuns
	}
	if sd.TargetUpperBound == 0 {
		sd.TargetUpperBound = profileStatDefaults.TargetUpperBound
	}
	if sd.ControlUpperBound == 0 {
		sd.ControlUpperBound = profileStatDefaults.ControlUpperBound
	}
	if sd.MinControlRuns == 0 {
		sd.MinControlRuns = profileStatDefaults.MinControlRuns
	}
	if sd.MinReproLowerBound == 0 {
		sd.MinReproLowerBound = profileStatDefaults.MinReproLowerBound
	}
	if sd.CrossSeedMin == 0 {
		sd.CrossSeedMin = profileStatDefaults.CrossSeedMin
	}
	if sd.CrossSeedFraction == 0 {
		sd.CrossSeedFraction = profileStatDefaults.CrossSeedFraction
	}
	if sd.Alpha == 0 {
		sd.Alpha = profileStatDefaults.Alpha
	}
	if sd.Beta == 0 {
		sd.Beta = profileStatDefaults.Beta
	}
	if sd.P0 == 0 {
		sd.P0 = profileStatDefaults.P0
	}
	if sd.P1 == 0 {
		sd.P1 = profileStatDefaults.P1
	}

	if p.EnumerationScope.Reachability == "" {
		p.EnumerationScope.Reachability = "public_api"
	}

	if p.DispatchMode == "" {
		p.DispatchMode = DispatchHybrid
	}
	if p.OwnerMode == "" {
		p.OwnerMode = OwnerSummary
	}

	if len(p.QueueKinds) == 0 {
		p.QueueKinds = profileAllQueues()
	}

	if p.LeaseTTL == nil {
		p.LeaseTTL = profileDefaultLeaseTTL()
	} else {
		defaults := profileDefaultLeaseTTL()
		for q, ttl := range defaults {
			if p.LeaseTTL[q] == 0 {
				p.LeaseTTL[q] = ttl
			}
		}
	}

	pw := &p.PriorityWeights
	if pw.Risk == 0 {
		pw.Risk = profileDefaultWeights().Risk
	}
	if pw.Novelty == 0 {
		pw.Novelty = profileDefaultWeights().Novelty
	}
	if pw.ExpectedHit == 0 {
		pw.ExpectedHit = profileDefaultWeights().ExpectedHit
	}
	if pw.Cost == 0 {
		pw.Cost = profileDefaultWeights().Cost
	}
	if pw.Age == 0 {
		pw.Age = profileDefaultWeights().Age
	}
	if pw.Cold == 0 {
		pw.Cold = profileDefaultWeights().Cold
	}

	if p.PerRoleCapacity == nil {
		p.PerRoleCapacity = profileDefaultRoleCapacity()
	} else {
		for r, c := range profileDefaultRoleCapacities {
			if p.PerRoleCapacity[r] == 0 {
				p.PerRoleCapacity[r] = c
			}
		}
	}

	if p.ReconciliationCadence.Incremental == 0 {
		p.ReconciliationCadence.Incremental = 15 * time.Minute
	}
	if !p.ReconciliationCadence.FullDaily {
		p.ReconciliationCadence.FullDaily = true
	}

	// scheduler_seed 的默认值就是 0；显式非零值原样保留。
}

// profileFormSet 把 forms 转成集合，用于 limited_control_forms ⊆ forms。
func profileFormSet(forms []Form) map[Form]struct{} {
	set := make(map[Form]struct{}, len(forms))
	for _, f := range forms {
		set[f] = struct{}{}
	}
	return set
}

// ValidateTaskProfile 按 §5.6/R-5.13–R-5.16 校验 profile；不合法时返回
// 带字段名的 error（平台加载时直接拒绝，R-5.16）。
//
// 统计项方向见 StatisticalFloor 的注释：upper_bound/alpha/beta/p0/p1
// 不得调高（放宽），min_* 不得调低。
func ValidateTaskProfile(p *TaskProfile) error {
	if p == nil {
		return fmt.Errorf("task_profile: nil")
	}
	if p.Name == "" {
		return fmt.Errorf("task_profile.name: empty")
	}
	if p.Version == "" {
		return fmt.Errorf("task_profile.version: empty")
	}

	if len(p.Forms) == 0 {
		return fmt.Errorf("task_profile.forms: empty")
	}
	forms := profileFormSet(p.Forms)
	for _, f := range p.Forms {
		if !f.Valid() {
			return fmt.Errorf("task_profile.forms: invalid form %q", string(f))
		}
	}
	for _, f := range p.LimitedControlForms {
		if !f.Valid() {
			return fmt.Errorf("task_profile.limited_control_forms: invalid form %q", string(f))
		}
		if _, ok := forms[f]; !ok {
			return fmt.Errorf("task_profile.limited_control_forms: %q not in forms (R-3.7)", string(f))
		}
	}

	floor := StatisticalFloor()
	sd := p.StatisticalDefaults
	if sd.MinRuns < floor.MinRuns {
		return fmt.Errorf("task_profile.statistical_defaults.min_runs: %d below floor %d (R-5.15)", sd.MinRuns, floor.MinRuns)
	}
	if sd.TargetUpperBound <= 0 || sd.TargetUpperBound > floor.TargetUpperBound {
		return fmt.Errorf("task_profile.statistical_defaults.target_upper_bound: %v must be in (0, %v] (R-5.15)", sd.TargetUpperBound, floor.TargetUpperBound)
	}
	if sd.ControlUpperBound <= 0 || sd.ControlUpperBound > floor.ControlUpperBound {
		return fmt.Errorf("task_profile.statistical_defaults.control_upper_bound: %v must be in (0, %v] (R-5.15)", sd.ControlUpperBound, floor.ControlUpperBound)
	}
	if sd.MinControlRuns < floor.MinControlRuns {
		return fmt.Errorf("task_profile.statistical_defaults.min_control_runs: %d below floor %d (R-5.15)", sd.MinControlRuns, floor.MinControlRuns)
	}
	if sd.MinReproLowerBound <= 0 || sd.MinReproLowerBound >= 1 {
		return fmt.Errorf("task_profile.statistical_defaults.min_repro_lower_bound: %v must be in (0, 1)", sd.MinReproLowerBound)
	}
	if sd.MinReproLowerBound < floor.MinReproLowerBound {
		return fmt.Errorf("task_profile.statistical_defaults.min_repro_lower_bound: %v below floor %v (R-5.15)", sd.MinReproLowerBound, floor.MinReproLowerBound)
	}
	if sd.CrossSeedMin < floor.CrossSeedMin {
		return fmt.Errorf("task_profile.statistical_defaults.cross_seed_min: %d below floor %d (R-5.15)", sd.CrossSeedMin, floor.CrossSeedMin)
	}
	if sd.CrossSeedFraction <= 0 || sd.CrossSeedFraction > 1 {
		return fmt.Errorf("task_profile.statistical_defaults.cross_seed_fraction: %v must be in (0, 1]", sd.CrossSeedFraction)
	}
	if sd.CrossSeedFraction < floor.CrossSeedFraction {
		return fmt.Errorf("task_profile.statistical_defaults.cross_seed_fraction: %v below floor %v (R-5.15)", sd.CrossSeedFraction, floor.CrossSeedFraction)
	}
	if sd.Alpha <= 0 || sd.Alpha >= 1 {
		return fmt.Errorf("task_profile.statistical_defaults.alpha: %v must be in (0, 1)", sd.Alpha)
	}
	if sd.Alpha > floor.Alpha {
		return fmt.Errorf("task_profile.statistical_defaults.alpha: %v looser than floor %v (R-5.15)", sd.Alpha, floor.Alpha)
	}
	if sd.Beta <= 0 || sd.Beta >= 1 {
		return fmt.Errorf("task_profile.statistical_defaults.beta: %v must be in (0, 1)", sd.Beta)
	}
	if sd.Beta > floor.Beta {
		return fmt.Errorf("task_profile.statistical_defaults.beta: %v looser than floor %v (R-5.15)", sd.Beta, floor.Beta)
	}
	if sd.P0 <= 0 || sd.P0 >= 1 {
		return fmt.Errorf("task_profile.statistical_defaults.p0: %v must be in (0, 1)", sd.P0)
	}
	if sd.P1 <= 0 || sd.P1 >= 1 {
		return fmt.Errorf("task_profile.statistical_defaults.p1: %v must be in (0, 1)", sd.P1)
	}
	if sd.P0 >= sd.P1 {
		return fmt.Errorf("task_profile.statistical_defaults.p0/p1: p0 (%v) must be < p1 (%v)", sd.P0, sd.P1)
	}
	if sd.P0 > floor.P0 {
		return fmt.Errorf("task_profile.statistical_defaults.p0: %v looser than floor %v (R-5.15)", sd.P0, floor.P0)
	}
	if sd.P1 > floor.P1 {
		return fmt.Errorf("task_profile.statistical_defaults.p1: %v looser than floor %v (R-5.15)", sd.P1, floor.P1)
	}

	if !p.DispatchMode.Valid() {
		return fmt.Errorf("task_profile.dispatch_mode: invalid %q (must be explicitly set)", string(p.DispatchMode))
	}
	if !p.OwnerMode.Valid() {
		return fmt.Errorf("task_profile.owner_mode: invalid %q", string(p.OwnerMode))
	}

	if len(p.QueueKinds) == 0 {
		return fmt.Errorf("task_profile.queue_kinds: empty")
	}
	for _, q := range p.QueueKinds {
		if !q.Valid() {
			return fmt.Errorf("task_profile.queue_kinds: invalid queue kind %q", string(q))
		}
	}

	for q, ttl := range p.LeaseTTL {
		if !q.Valid() {
			return fmt.Errorf("task_profile.lease_ttl: invalid queue kind %q", string(q))
		}
		if ttl <= 0 {
			return fmt.Errorf("task_profile.lease_ttl[%s]: must be positive, got %v", string(q), ttl)
		}
	}
	for _, q := range p.QueueKinds {
		ttl, ok := p.LeaseTTL[q]
		if !ok {
			return fmt.Errorf("task_profile.lease_ttl[%s]: missing for queue kind in queue_kinds", string(q))
		}
		if ttl <= 0 {
			return fmt.Errorf("task_profile.lease_ttl[%s]: must be positive, got %v", string(q), ttl)
		}
	}

	for r, capacity := range p.PerRoleCapacity {
		if !r.Valid() {
			return fmt.Errorf("task_profile.per_role_capacity: invalid role %q", string(r))
		}
		if capacity < 0 {
			return fmt.Errorf("task_profile.per_role_capacity[%s]: must be >= 0, got %d", string(r), capacity)
		}
	}

	switch p.EnumerationScope.Reachability {
	case "", "public_api", "all":
	default:
		return fmt.Errorf("task_profile.enumeration_scope.reachability: invalid %q", p.EnumerationScope.Reachability)
	}

	// evidence_eligibility 只能收紧，不能放宽 S3/S4 的最低 event/tool 要求
	// （R-5.14）。Extra 中任何组合都必须在基础 Eligible 谓词下成立。
	for f, events := range p.EvidenceEligibility.Extra {
		if !f.Valid() {
			return fmt.Errorf("task_profile.evidence_eligibility.extra: invalid form %q", string(f))
		}
		for _, ev := range events {
			if !ev.EventType.Valid() {
				return fmt.Errorf("task_profile.evidence_eligibility.extra[%s]: invalid event_type %q", string(f), string(ev.EventType))
			}
			if !ev.Tool.Valid() {
				return fmt.Errorf("task_profile.evidence_eligibility.extra[%s]: invalid tool %q", string(f), string(ev.Tool))
			}
			if !Eligible(f, ev.EventType, ev.Tool) {
				return fmt.Errorf(
					"task_profile.evidence_eligibility.extra[%s]: (%s,%s) not eligible in base profile; eligibility may only be tightened (R-5.14)",
					string(f), string(ev.EventType), string(ev.Tool))
			}
		}
	}

	return nil
}

// ComputePolicyHash 先补齐默认值（R-14.13：默认值进入 policy_hash），
// 再计算 canonical JSON 的 sha256，并把结果写入 p.PolicyHash。
// nil profile 返回空字符串。
func ComputePolicyHash(p *TaskProfile) string {
	if p == nil {
		return ""
	}
	ApplyTaskProfileDefaults(p)
	h := PolicyHash(p)
	p.PolicyHash = h
	return h
}
