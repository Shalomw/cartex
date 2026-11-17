package lifetime

// 本文件实现 §7.1/§7.2 的状态机与 R-7.11/R-7.12 的收尾覆盖状态集合。

import (
	"fmt"
	"strings"
)

// ltAllLifecycleStates 是所有合法状态（§7.1），用于展开 §7.2 的 `*` 通配边。
var ltAllLifecycleStates = []LifecycleState{
	StatePrepare,
	StateDiscover,
	StateCandidate,
	StateProbe,
	StateVerify,
	StateEvidence,
	StateReproduce,
	StateReview,
	StateReport,
	StateRefute,
	StateInconclusive,
	StateBlocked,
	StateDeferred,
	StateIndexRevision,
}

// ltStatesContain 报告 states 中是否包含 want。
func ltStatesContain(states []LifecycleState, want LifecycleState) bool {
	for _, s := range states {
		if s == want {
			return true
		}
	}
	return false
}

// AllowedTransitions 返回 §7.1/§7.2 的状态转移图。
//
// 说明：
//   - 显式边来自 §7.1 的“出口”列与 §7.2 的触发表；
//   - §7.2 的 `* → INDEX_REVISION` 与 `* → BLOCKED/DEFERRED` 通配边被展开到
//     每个非目标状态上（不含自环）；
//   - `VERIFY → DISCOVER` 来自 §7.4 的反馈边（binding=unmatched / new_lead）；
//   - `DEFERRED` 只有“收尾”出口，而收尾不是 LifecycleState；因此它只剩通配边；
//   - 收尾的覆盖状态不在本图中，见 ClosingCoverageStatuses。
func AllowedTransitions() map[LifecycleState][]LifecycleState {
	explicit := map[LifecycleState][]LifecycleState{
		StatePrepare: {
			StateDiscover, StateBlocked, StateDeferred,
		},
		StateDiscover: {
			StateCandidate, StateProbe, StateBlocked, StateDeferred,
		},
		StateCandidate: {
			StateProbe, StateVerify, StateDiscover, StateDeferred,
		},
		StateProbe: {
			StateCandidate, StateVerify, StateRefute, StateInconclusive,
		},
		StateVerify: {
			// §7.1 出口 + §7.4 的 VERIFY → DISCOVER 反馈边。
			StateEvidence, StateInconclusive, StateCandidate, StateBlocked,
			StateDeferred, StateDiscover,
		},
		StateEvidence: {
			StateReproduce, StateReview, StateVerify, StateInconclusive,
		},
		StateReproduce: {
			StateReview, StateInconclusive, StateVerify,
		},
		StateReview: {
			StateReport, StateRefute, StateInconclusive, StateVerify,
		},
		StateReport: {
			StateDiscover,
		},
		StateRefute: {
			StateDiscover,
		},
		StateInconclusive: {
			StateDiscover, StateVerify, StateDeferred,
		},
		StateBlocked: {
			StateDiscover, StateDeferred,
		},
		StateDeferred: {
			// 收尾不是 LifecycleState；只剩通配边。
		},
		StateIndexRevision: {
			StatePrepare, StateDiscover,
		},
	}

	out := make(map[LifecycleState][]LifecycleState, len(ltAllLifecycleStates))
	for from, tos := range explicit {
		seen := make(map[LifecycleState]struct{}, len(tos)+3)
		list := make([]LifecycleState, 0, len(tos)+3)
		add := func(to LifecycleState) {
			if to == from {
				return
			}
			if _, ok := seen[to]; ok {
				return
			}
			seen[to] = struct{}{}
			list = append(list, to)
		}
		for _, to := range tos {
			add(to)
		}
		// §7.2 通配边。
		add(StateIndexRevision)
		add(StateBlocked)
		add(StateDeferred)
		out[from] = list
	}
	return out
}

// CanTransition 报告 from → to 是否为合法转移（R-7.3）。
// 非法状态、自环与未定义的边返回 false。
func CanTransition(from, to LifecycleState) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	return ltStatesContain(AllowedTransitions()[from], to)
}

// ValidateTransition 校验一条状态转移审计事件（R-7.2/R-7.3）：
// from/to 合法、转移被允许、trigger/actor/lineage_hash 非空。
func ValidateTransition(e TransitionEvent) error {
	if !e.From.Valid() {
		return fmt.Errorf("transition.from: invalid state %q", string(e.From))
	}
	if !e.To.Valid() {
		return fmt.Errorf("transition.to: invalid state %q", string(e.To))
	}
	if !CanTransition(e.From, e.To) {
		return fmt.Errorf("transition %s -> %s is not allowed (R-7.3)",
			string(e.From), string(e.To))
	}
	if strings.TrimSpace(e.Trigger) == "" {
		return fmt.Errorf("transition.trigger: required (R-7.2)")
	}
	if strings.TrimSpace(e.Actor) == "" {
		return fmt.Errorf("transition.actor: required (R-7.2)")
	}
	if strings.TrimSpace(e.LineageHash) == "" {
		return fmt.Errorf("transition.lineage_hash: required (R-7.2)")
	}
	return nil
}

// ClosingCoverageStatuses 返回 R-7.11 允许的收尾覆盖状态：
// no_risk / verified / refuted / deferred_budget / blocked / unknown /
// not_applicable。这些是 CoverageStatus，不直接对应 LifecycleState。
// 返回新切片，调用方可安全修改。
func ClosingCoverageStatuses() []CoverageStatus {
	return []CoverageStatus{
		CoverageNoRisk,
		CoverageVerified,
		CoverageRefuted,
		CoverageDeferredBudget,
		CoverageBlocked,
		CoverageUnknown,
		CoverageNotApplicable,
	}
}
