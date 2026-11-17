package lifetime

import (
	"fmt"
	"sort"
	"time"
)

// 本文件实现 §6.2–§6.6 的工作队列语义：Q1–Q5 ready 谓词、优先级、
// claim/lease 协议与过滤白名单。所有函数确定性，不写库；租约是否被占用
// 需要查询状态时由 store 负责（见各谓词注释）。

// VerifyMode Q2 工作项模式（§6.2）。
type VerifyMode string

const (
	VerifyModeProbe  VerifyMode = "probe"
	VerifyModeVerify VerifyMode = "verify"
)

// DriverSpecState candidate 的 DriverSpec 投影状态（§6.3/R-6.9）。
type DriverSpecState string

const (
	DriverMissing  DriverSpecState = "missing"
	DriverDraft    DriverSpecState = "draft"
	DriverBuilt    DriverSpecState = "built"
	DriverStale    DriverSpecState = "stale"
	DriverRejected DriverSpecState = "rejected"
)

// queueMaxAttemptsDefault 是 R-6.12 的默认 attempts 上限（§14.6 max_attempts）。
const queueMaxAttemptsDefault = 3

// ReadyQ1 实现 §6.3 的 Q1 ready 谓词。
//
//	Q1 ready(pkg) = 存在 form 的 coverage_status 不在
//	{no_risk, verified, refuted, not_applicable}
//	且 pkg.lease_owner 为空 且 index_available。
//
// applicable_forms 为空视为尚未枚举，不可开工。units 需包含该
// (family, domain_pair) 的账本行；调用方可以只传相关行。
func ReadyQ1(pkg WorkPackage, units []CoverageUnit, indexAvailable bool) bool {
	if !indexAvailable {
		return false
	}
	if pkg.LeaseOwner != "" {
		return false
	}
	if len(pkg.ApplicableForms) == 0 {
		return false
	}
	applicable := make(map[Form]bool, len(pkg.ApplicableForms))
	for _, f := range pkg.ApplicableForms {
		applicable[f] = true
	}
	for _, u := range units {
		if u.FamilyID != pkg.FamilyID || u.DomainPairID != pkg.DomainPairID {
			continue
		}
		if !applicable[u.Form] {
			continue
		}
		switch u.Status {
		case CoverageNoRisk, CoverageVerified, CoverageRefuted, CoverageNotApplicable:
			continue
		}
		return true
	}
	return false
}

// ReadyQ2 实现 §6.3 的 Q2 ready 谓词。
// probe 接受 S1 或 provisional；verify 要求恰好 S2 且 DriverSpec 处于
// {missing, stale, rejected}。candidate 的 active lease 由 store 判断。
func ReadyQ2(c Candidate, a Assessment, mode VerifyMode, driver DriverSpecState) bool {
	if c.ContractStatus == ContractConflicting {
		return false
	}
	if c.Budget.Remaining <= 0 {
		return false
	}
	switch mode {
	case VerifyModeProbe:
		if !(a.Level == LevelS1 || a.Provisional) {
			return false
		}
	case VerifyModeVerify:
		if a.Level != LevelS2 {
			return false
		}
		switch driver {
		case DriverMissing, DriverStale, DriverRejected:
		default:
			return false
		}
	default:
		return false
	}
	return true
}

// ReadyQ3 实现 §6.3 的 Q3 ready 谓词：S3/S4 claim、未 stale、open、
// 尚未 confirmed。assessment 的 active review lease 由 store 判断。
func ReadyQ3(a Assessment) bool {
	if !a.Level.AtLeast(LevelS3) {
		return false
	}
	if a.LineageRole != LineageRoleClaim {
		return false
	}
	if a.Stale {
		return false
	}
	if a.Status != AssessmentOpen {
		return false
	}
	if a.Review.Verdict == VerdictConfirmed {
		return false
	}
	return true
}

// ReadyQ4 实现 §6.3 的 Q4 ready 谓词：confirmed + reportable_external +
// finding 尚不存在。active report lease 由 store 判断。
func ReadyQ4(a Assessment, findingExists bool) bool {
	if a.Review.Verdict != VerdictConfirmed {
		return false
	}
	if !a.ReportableExternal {
		return false
	}
	return !findingExists
}

// ReadyQ5 实现 §6.3 的 Q5 ready 谓词：severity >= medium 且 action 属于
// 可自动修复集合。dedup_key 是否已有 active repair task 由 store 判断。
func ReadyQ5(f ReconciliationFinding) bool {
	if reconcileSeverityRank(f.Severity) < reconcileSeverityRank(SeverityMedium) {
		return false
	}
	switch f.Action {
	case ActionRequeue, ActionMarkStale, ActionCreateRepairTask:
		return true
	}
	return false
}

// Priority 实现 §6.6：
//
//	priority = w_risk*risk + w_novelty*novelty + w_hit*expected_hit
//	         - w_cost*cost + w_age*age + w_cold*cold
func Priority(w PriorityWeights, risk, novelty, expectedHit, cost, age, cold float64) float64 {
	return w.Risk*risk +
		w.Novelty*novelty +
		w.ExpectedHit*expectedHit -
		w.Cost*cost +
		w.Age*age +
		w.Cold*cold
}

// SchedulerTieBreak 返回 §6.6/R-6.43 的并列打破键 hash(item_id, scheduler_seed)。
func SchedulerTieBreak(itemID string, seed int64) string {
	return SortKeyHash(itemID, seed)
}

// ApplyClaim 把 ready 工作项原子地转给 session（§6.4/R-6.10）。
// 调用方负责在 store 中执行 `UPDATE ... WHERE state='ready'` 的原子语义；
// 本函数只在内存副本上校验与赋值。
func ApplyClaim(it WorkItem, sessionID, claimID string, ttl time.Duration, now time.Time) (WorkItem, error) {
	if it.State != WorkItemReady {
		return it, fmt.Errorf("work item %s: cannot claim in state %q", it.ItemID, it.State)
	}
	if claimID == "" {
		return it, fmt.Errorf("work item %s: empty claim_id", it.ItemID)
	}
	if sessionID == "" {
		return it, fmt.Errorf("work item %s: empty session_id", it.ItemID)
	}
	it.State = WorkItemClaimed
	it.ClaimedBySession = sessionID
	it.ClaimID = claimID
	it.LeaseExpiresAt = now.Add(ttl)
	it.Reason = ""
	it.UpdatedAt = now
	return it, nil
}

// ApplyRenew 延长 claim 的租约（§6.4）。claim 必须匹配且尚未过期
// （R-6.15：租约丢失后平台拒绝后续写）。
func ApplyRenew(it WorkItem, claimID string, ttl time.Duration, now time.Time) (WorkItem, error) {
	if it.State != WorkItemClaimed {
		return it, fmt.Errorf("work item %s: cannot renew in state %q", it.ItemID, it.State)
	}
	if claimID == "" || it.ClaimID != claimID {
		return it, fmt.Errorf("work item %s: claim_id mismatch", it.ItemID)
	}
	if !it.LeaseExpiresAt.After(now) {
		return it, fmt.Errorf("work item %s: lease already expired", it.ItemID)
	}
	it.LeaseExpiresAt = now.Add(ttl)
	it.UpdatedAt = now
	return it, nil
}

// ApplyRelease 把工作项放回 ready；attempts 不变（R-6.14）。
func ApplyRelease(it WorkItem, claimID string, now time.Time) (WorkItem, error) {
	if it.State != WorkItemClaimed {
		return it, fmt.Errorf("work item %s: cannot release in state %q", it.ItemID, it.State)
	}
	if claimID == "" || it.ClaimID != claimID {
		return it, fmt.Errorf("work item %s: claim_id mismatch", it.ItemID)
	}
	it.State = WorkItemReady
	it.ClaimedBySession = ""
	it.ClaimID = ""
	it.LeaseExpiresAt = time.Time{}
	it.Reason = ""
	it.UpdatedAt = now
	return it, nil
}

// ApplyComplete 完成工作项；清空 claim 字段（§6.4）。
func ApplyComplete(it WorkItem, claimID string, now time.Time) (WorkItem, error) {
	if it.State != WorkItemClaimed {
		return it, fmt.Errorf("work item %s: cannot complete in state %q", it.ItemID, it.State)
	}
	if claimID == "" || it.ClaimID != claimID {
		return it, fmt.Errorf("work item %s: claim_id mismatch", it.ItemID)
	}
	it.State = WorkItemDone
	it.ClaimedBySession = ""
	it.ClaimID = ""
	it.LeaseExpiresAt = time.Time{}
	it.UpdatedAt = now
	return it, nil
}

// ApplyExpire 处理租约过期（R-6.12）：回 ready、attempts++、清 claim；
// attempts 达到上限（3）时置 blocked。返回 (新项, 是否发生过期)。
func ApplyExpire(it WorkItem, now time.Time) (WorkItem, bool) {
	if it.State != WorkItemClaimed {
		return it, false
	}
	if it.LeaseExpiresAt.After(now) {
		return it, false
	}
	it.Attempts++
	it.State = WorkItemReady
	it.ClaimedBySession = ""
	it.ClaimID = ""
	it.LeaseExpiresAt = time.Time{}
	it.UpdatedAt = now
	if it.Attempts >= queueMaxAttemptsDefault {
		it.State = WorkItemBlocked
		it.Reason = "max_attempts_exceeded"
	} else {
		it.Reason = "lease_expired"
	}
	return it, true
}

// claimFilterWhitelist 是 §16.5/R-16.6 允许的 claim filter 键：
// filter 只能收窄 ready 集合，未知键直接拒绝。
var claimFilterWhitelist = map[QueueKind]map[string]bool{
	QueueUnitReadyDiscover:    {"area_id": true, "family_id": true},
	QueueCandidateReadyVerify: {"mode": true, "candidate_id": true},
	QueueClaimReadyReview:     {"assessment_id": true},
	QueueConfirmedReadyReport: {"finding_id": true},
	QueueRepairTask:           {"check_id": true, "severity": true},
}

// ValidateClaimFilter 校验 filter 只能收窄（R-16.6）：键必须属于该队列的
// 白名单。空 filter 合法；未知 queue kind 或未知键报错。按键排序遍历，
// 保证错误信息确定性。
func ValidateClaimFilter(k QueueKind, filter map[string]string) error {
	allowed, ok := claimFilterWhitelist[k]
	if !ok {
		return fmt.Errorf("unknown queue kind %q", k)
	}
	keys := make([]string, 0, len(filter))
	for key := range filter {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !allowed[key] {
			return fmt.Errorf("filter key %q is not allowed for %s", key, k)
		}
	}
	return nil
}

// LeaseTTL 返回队列租约 TTL：优先 TaskProfile.LeaseTTL，未设置时使用
// §14.6 默认值（Q1 30m / Q2 60m / Q3 60m / Q4 15m / Q5 30m）。
func LeaseTTL(p *TaskProfile, q QueueKind) time.Duration {
	if p != nil {
		if ttl, ok := p.LeaseTTL[q]; ok && ttl > 0 {
			return ttl
		}
	}
	switch q {
	case QueueUnitReadyDiscover:
		return 30 * time.Minute
	case QueueCandidateReadyVerify:
		return 60 * time.Minute
	case QueueClaimReadyReview:
		return 60 * time.Minute
	case QueueConfirmedReadyReport:
		return 15 * time.Minute
	case QueueRepairTask:
		return 30 * time.Minute
	}
	return 0
}
