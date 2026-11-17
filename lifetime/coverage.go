package lifetime

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"
)

// 本文件实现 §4.5–§4.8 的覆盖账本语义：unit identity、no_risk 校验、
// 收尾规则与覆盖统计。所有函数均为确定性纯函数，不写库。

// reviewStatus 取值（§4.7）。
const (
	coverageReviewSelfAsserted = "self_asserted"
	coverageReviewReviewed     = "reviewed"
)

// lifetimeLedgerHash 对字段做长度前缀的 SHA-256，避免字段拼接歧义。
// 返回小写 hex，用于 unit_id / dedup_key 等确定性标识。
func lifetimeLedgerHash(parts ...string) string {
	h := sha256.New()
	var size [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(size[:], uint64(len(p)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// UnitID 生成覆盖单位 (family|relation) × domain_pair × form 的确定性 ID。
// 所有字段都参与哈希；kind=family 时 relationID 为空，kind=relation 时
// familyID 为空（R-4.14/R-4.15）。
func UnitID(kind UnitKind, familyID, relationID, domainPairID string, form Form) string {
	return lifetimeLedgerHash("coverage_unit", string(kind), familyID, relationID, domainPairID, string(form))
}

// NewCoverageUnit 新建账本行：默认 unexamined + self_asserted（§4.6/§4.7）。
func NewCoverageUnit(kind UnitKind, familyID, relationID, domainPairID string, form Form, lineageID string) CoverageUnit {
	return CoverageUnit{
		UnitID:       UnitID(kind, familyID, relationID, domainPairID, form),
		UnitKind:     kind,
		FamilyID:     familyID,
		RelationID:   relationID,
		DomainPairID: domainPairID,
		Form:         form,
		Status:       CoverageUnexamined,
		ReviewStatus: coverageReviewSelfAsserted,
		LineageID:    lineageID,
	}
}

// coverageUnitRef 返回账本行的稳定引用：优先用已持久化的 UnitID，
// 缺失时按字段重算，保证对账 dedup_key 可复现。
func coverageUnitRef(u CoverageUnit) string {
	if u.UnitID != "" {
		return u.UnitID
	}
	return UnitID(u.UnitKind, u.FamilyID, u.RelationID, u.DomainPairID, u.Form)
}

// NoRiskCoversForm 实现 R-4.23：该行 form 被至少一个 argument 的
// covers_forms 覆盖。它只做平台侧派生计算，不读取 CoversCurrentForm 标志。
func NoRiskCoversForm(n NoRisk, form Form) bool {
	for _, arg := range n.Arguments {
		for _, f := range arg.CoversForms {
			if f == form {
				return true
			}
		}
	}
	return false
}

// ValidateNoRiskAccessSet 实现 R-4.25：每个 argument 的 access_set 必须等于
// 索引对该 unit 的完整 access set；平台通过 AccessSetHash 比较，缺失 access
// 即拒收。
func ValidateNoRiskAccessSet(n NoRisk, expected []Access) error {
	want := AccessSetHash(expected)
	for i, arg := range n.Arguments {
		got := AccessSetHash(arg.AccessSet)
		if got != want {
			return fmt.Errorf("no_risk %s: argument %d access_set_hash %s != expected %s (R-4.25)", n.NoRiskID, i, got, want)
		}
	}
	return nil
}

// noRiskProofPresent 报告 proof 是否具备可引用的证据。
func noRiskProofPresent(p Proof) bool {
	return p.Kind != "" && len(p.Refs) > 0
}

// ValidateNoRiskArgument 实现 R-4.26 的结构化校验：
//   - serialized/guarded 必须有 sync，且每个 sync.protects 非空；
//   - unreachable 必须有 proof；
//   - generation 必须带 generation 论证（以 proof 承载“每次解引用都检查
//     generation”的证明）；
//   - 所有 citation_check 必须 verified。
func ValidateNoRiskArgument(n NoRisk, lineageID string) error {
	for i, arg := range n.Arguments {
		if !arg.ArgumentType.Valid() {
			return fmt.Errorf("no_risk %s (lineage %s): argument %d has invalid argument_type %q", n.NoRiskID, lineageID, i, arg.ArgumentType)
		}
		switch arg.ArgumentType {
		case NoRiskSerialized, NoRiskGuarded:
			if len(arg.Sync) == 0 {
				return fmt.Errorf("no_risk %s (lineage %s): argument %d (%s) requires sync primitives (R-4.26)", n.NoRiskID, lineageID, i, arg.ArgumentType)
			}
			for j, s := range arg.Sync {
				if s.Protects == "" {
					return fmt.Errorf("no_risk %s (lineage %s): argument %d sync %d has empty protects (R-4.26)", n.NoRiskID, lineageID, i, j)
				}
			}
		case NoRiskUnreachable:
			if !noRiskProofPresent(arg.Proof) {
				return fmt.Errorf("no_risk %s (lineage %s): argument %d (unreachable) requires proof (R-4.26)", n.NoRiskID, lineageID, i)
			}
		case NoRiskGeneration:
			if !noRiskProofPresent(arg.Proof) {
				return fmt.Errorf("no_risk %s (lineage %s): argument %d (generation) requires generation proof (R-4.26)", n.NoRiskID, lineageID, i)
			}
		}
		for j, c := range arg.CitationCheck {
			if c.Status != CitationVerified {
				return fmt.Errorf("no_risk %s (lineage %s): argument %d citation_check %d status %q is not verified (R-4.26)", n.NoRiskID, lineageID, i, j, c.Status)
			}
		}
	}
	return nil
}

// coverageClosingStatus 报告状态是否属于 R-7.11 允许的收尾状态集合。
func coverageClosingStatus(status CoverageStatus) bool {
	switch status {
	case CoverageNoRisk, CoverageVerified, CoverageRefuted, CoverageDeferredBudget,
		CoverageBlocked, CoverageUnknown, CoverageNotApplicable:
		return true
	}
	return false
}

// ValidateCoverageClose 实现 R-4.17–R-4.22 的严格收尾校验：
//   - refuted 必须 candidates_total == candidates_refuted 且 > 0；
//   - verified 必须 candidates_total > 0（candidate_open 由调用方保证不存在）；
//   - no_risk 必须有覆盖该 form 的论证，且 domain_confidence != low；
//   - domain_confidence=low 只能收尾为 unknown；
//   - not_applicable 必须带 proof_ref；
//   - candidate_open 不是收尾状态（R-4.21）；
//   - 非收尾状态（unexamined/examined_unreviewed/stale 等）一律拒绝。
func ValidateCoverageClose(u CoverageUnit, noRisk *NoRisk, candidatesTotal, candidatesRefuted int, domainConfidence DomainConfidence) error {
	ref := coverageUnitRef(u)
	if !u.Status.Valid() {
		return fmt.Errorf("unit %s: invalid coverage status %q", ref, u.Status)
	}
	if u.Status == CoverageCandidateOpen {
		return fmt.Errorf("unit %s: candidate_open is not a closing status; resolve to verified/refuted/deferred_budget first (R-4.21)", ref)
	}
	if !coverageClosingStatus(u.Status) {
		return fmt.Errorf("unit %s: status %q is not a closing status (R-7.11)", ref, u.Status)
	}
	if domainConfidence == DomainConfidenceLow && u.Status != CoverageUnknown {
		return fmt.Errorf("unit %s: domain_confidence=low only allows unknown (R-4.20)", ref)
	}
	if candidatesTotal < 0 || candidatesRefuted < 0 {
		return fmt.Errorf("unit %s: negative candidate counts (total=%d refuted=%d)", ref, candidatesTotal, candidatesRefuted)
	}
	if candidatesRefuted > candidatesTotal {
		return fmt.Errorf("unit %s: candidates_refuted %d exceeds candidates_total %d", ref, candidatesRefuted, candidatesTotal)
	}

	switch u.Status {
	case CoverageRefuted:
		if candidatesTotal == 0 {
			return fmt.Errorf("unit %s: refuted requires at least one enumerated candidate (R-4.17)", ref)
		}
		if candidatesRefuted != candidatesTotal {
			return fmt.Errorf("unit %s: refuted requires all candidates refuted (total=%d refuted=%d, R-4.17)", ref, candidatesTotal, candidatesRefuted)
		}
	case CoverageVerified:
		if candidatesTotal == 0 {
			return fmt.Errorf("unit %s: verified requires at least one resolved candidate (R-4.18)", ref)
		}
	case CoverageNoRisk:
		if noRisk == nil {
			return fmt.Errorf("unit %s: no_risk requires a no_risk argument set (R-4.19)", ref)
		}
		if !NoRiskCoversForm(*noRisk, u.Form) {
			return fmt.Errorf("unit %s: no_risk does not cover form %s (R-4.19/R-4.23)", ref, u.Form)
		}
		if domainConfidence == DomainConfidenceLow {
			return fmt.Errorf("unit %s: no_risk requires domain_confidence != low (R-4.19)", ref)
		}
	case CoverageNotApplicable:
		if u.ProofRef == "" {
			return fmt.Errorf("unit %s: not_applicable requires proof_ref (R-4.22)", ref)
		}
	case CoverageDeferredBudget:
		if u.Reason == "" {
			return fmt.Errorf("unit %s: deferred_budget requires a reason (R-7.13)", ref)
		}
	}
	return nil
}

// coveragePct 返回 `100 * num/den`；分母为 0 时返回 0（§4.8）。
func coveragePct(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den) * 100
}

// ComputeCoverageStats 按 §4.8 公式计算覆盖统计。
//
//	enumerated_units  = 状态 != not_applicable 的账本行
//	concluded_units   = no_risk + verified + refuted
//	risk_closed_units = no_risk + refuted
//	finding_units     = verified
//	reviewed_*        = 同上但仅 review_status=reviewed 的行
//
// 所有比率字段均以百分数（0–100）返回，与 `_pct` 命名和报告门禁
// （如 unknown_rate ≤ 20%）一致；分母为 0 时返回 0。
func ComputeCoverageStats(units []CoverageUnit, unknownEstimate int) CoverageStats {
	if unknownEstimate < 0 {
		unknownEstimate = 0
	}
	st := CoverageStats{UnknownEstimate: unknownEstimate}
	for _, u := range units {
		if u.Status == CoverageNotApplicable {
			continue // R-4.22：not_applicable 有证明才移出分母
		}
		st.EnumeratedUnits++
		if u.Status == CoverageUnknown {
			st.UnknownRows++
		}
		if u.Status == CoverageCandidateOpen {
			st.CandidateUnits++
		}
		if !u.Status.Concluded() {
			continue
		}
		st.ConcludedUnits++
		reviewed := u.ReviewStatus == coverageReviewReviewed
		switch u.Status {
		case CoverageNoRisk:
			st.RiskClosedUnits++
			if reviewed {
				st.ReviewedConcludedUnits++
				st.ReviewedRiskClosedUnits++
			}
		case CoverageRefuted:
			st.RiskClosedUnits++
			if reviewed {
				st.ReviewedConcludedUnits++
				st.ReviewedRiskClosedUnits++
			}
		case CoverageVerified:
			st.FindingUnits++
			if reviewed {
				st.ReviewedConcludedUnits++
			}
		}
	}

	st.ExaminedUnits = st.CandidateUnits + st.ConcludedUnits
	st.CoveragePct = coveragePct(st.ConcludedUnits, st.EnumeratedUnits)
	st.CandidateRate = coveragePct(st.CandidateUnits, st.EnumeratedUnits)
	st.RiskClosedPctAll = coveragePct(st.RiskClosedUnits, st.EnumeratedUnits)
	st.RiskClosedPctReviewed = coveragePct(st.ReviewedRiskClosedUnits, st.ReviewedConcludedUnits)
	st.HitRateUnits = coveragePct(st.FindingUnits, st.EnumeratedUnits)
	st.UnknownRate = coveragePct(st.UnknownRows+unknownEstimate, st.EnumeratedUnits+unknownEstimate)
	st.ReviewedConcludedRate = coveragePct(st.ReviewedConcludedUnits, st.ConcludedUnits)
	return st
}

// CanCloseUnit 报告账本行是否已得出结论（no_risk/verified/refuted，§4.6）。
func CanCloseUnit(u CoverageUnit) bool {
	return u.Status.Concluded()
}

// ApplyCoverageStatusAt 是 ApplyCoverageStatus 的可注入时间版本。
// 它校验 status 与 review_status，更新账本字段并把 UpdatedAt 设为 at(UTC)。
// lineageID 不在本函数中更新（调用方通过 ApplyCoverageStatus 传入）。
func ApplyCoverageStatusAt(u *CoverageUnit, status CoverageStatus, reason, evidenceRef, proofRef, reviewStatus string, at time.Time) error {
	if u == nil {
		return fmt.Errorf("apply coverage status: nil coverage unit")
	}
	if !status.Valid() {
		return fmt.Errorf("unit %s: invalid coverage status %q", coverageUnitRef(*u), status)
	}
	if reviewStatus != coverageReviewSelfAsserted && reviewStatus != coverageReviewReviewed {
		return fmt.Errorf("unit %s: invalid review_status %q (want self_asserted|reviewed)", coverageUnitRef(*u), reviewStatus)
	}
	u.Status = status
	u.Reason = reason
	u.EvidenceRef = evidenceRef
	u.ProofRef = proofRef
	u.ReviewStatus = reviewStatus
	u.UpdatedAt = at.UTC()
	return nil
}

// ApplyCoverageStatus 更新账本行状态，使用当前 UTC 时间作为 UpdatedAt。
// 非空 lineageID 会同步写入（lineage 变化由对账 C5 驱动）。
func ApplyCoverageStatus(u *CoverageUnit, status CoverageStatus, reason, evidenceRef, proofRef, reviewStatus, lineageID string) error {
	if err := ApplyCoverageStatusAt(u, status, reason, evidenceRef, proofRef, reviewStatus, time.Now().UTC()); err != nil {
		return err
	}
	if lineageID != "" {
		u.LineageID = lineageID
	}
	return nil
}
