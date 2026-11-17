package lifetime

import (
	"slices"
	"testing"
)

func TestStateAllowedTransitions(t *testing.T) {
	allowed := [][2]LifecycleState{
		{StatePrepare, StateDiscover},
		{StateDiscover, StateCandidate},
		{StateDiscover, StateProbe},
		{StateCandidate, StateProbe},
		{StateCandidate, StateVerify},
		{StateCandidate, StateDiscover},
		{StateProbe, StateVerify},
		{StateProbe, StateRefute},
		{StateVerify, StateEvidence},
		{StateVerify, StateInconclusive},
		{StateVerify, StateDiscover},
		{StateEvidence, StateReproduce},
		{StateEvidence, StateReview},
		{StateReproduce, StateReview},
		{StateReview, StateReport},
		{StateReview, StateRefute},
		{StateReport, StateDiscover},
		{StateRefute, StateDiscover},
		{StateInconclusive, StateDiscover},
		{StateInconclusive, StateVerify},
		{StateInconclusive, StateDeferred},
		{StateBlocked, StateDiscover},
		{StateBlocked, StateDeferred},
		{StateIndexRevision, StatePrepare},
		{StateIndexRevision, StateDiscover},
	}
	// §7.2 通配边：* → INDEX_REVISION / BLOCKED / DEFERRED（不含自环）。
	for _, from := range ltAllLifecycleStates {
		for _, to := range []LifecycleState{StateIndexRevision, StateBlocked, StateDeferred} {
			if from != to {
				allowed = append(allowed, [2]LifecycleState{from, to})
			}
		}
	}

	for _, pair := range allowed {
		if !CanTransition(pair[0], pair[1]) {
			t.Errorf("CanTransition(%s,%s)=false, want true", pair[0], pair[1])
		}
	}

	disallowed := [][2]LifecycleState{
		{StatePrepare, StateReport},
		{StatePrepare, StateVerify},
		{StatePrepare, StateEvidence},
		{StateDiscover, StateReport},
		{StateDiscover, StateEvidence},
		{StateCandidate, StateEvidence},
		{StateCandidate, StateReport},
		{StateProbe, StateEvidence},
		{StateEvidence, StateReport},
		{StateReproduce, StateReport},
		{StateReport, StateReview},
		{StateRefute, StateVerify},
		{StateDeferred, StateDiscover},
	}
	for _, pair := range disallowed {
		if CanTransition(pair[0], pair[1]) {
			t.Errorf("CanTransition(%s,%s)=true, want false", pair[0], pair[1])
		}
	}

	// 自环与非法状态。
	for _, s := range ltAllLifecycleStates {
		if CanTransition(s, s) {
			t.Errorf("CanTransition(%s,%s)=true, want false (no self loops)", s, s)
		}
	}
	if CanTransition(LifecycleState("PAUSED"), StateDiscover) {
		t.Error("CanTransition(invalid, DISCOVER)=true, want false")
	}
	if CanTransition(StatePrepare, LifecycleState("PAUSED")) {
		t.Error("CanTransition(PREPARE, invalid)=true, want false")
	}

	// 图完整性：覆盖全部状态、目标合法、无重复、无自环。
	graph := AllowedTransitions()
	if len(graph) != len(ltAllLifecycleStates) {
		t.Fatalf("AllowedTransitions has %d states, want %d", len(graph), len(ltAllLifecycleStates))
	}
	for from, tos := range graph {
		if !from.Valid() {
			t.Errorf("graph contains invalid source %q", from)
		}
		seen := make(map[LifecycleState]struct{}, len(tos))
		for _, to := range tos {
			if !to.Valid() {
				t.Errorf("%s -> invalid target %q", from, to)
			}
			if to == from {
				t.Errorf("%s -> self loop", from)
			}
			if _, ok := seen[to]; ok {
				t.Errorf("%s -> %s duplicated", from, to)
			}
			seen[to] = struct{}{}
		}
	}
}

func TestTransitionValidate(t *testing.T) {
	valid := TransitionEvent{
		From:        StatePrepare,
		To:          StateDiscover,
		Trigger:     "platform_event",
		Actor:       "platform",
		Reason:      "build/index complete",
		LineageHash: "lineage-hash-1",
	}
	if err := ValidateTransition(valid); err != nil {
		t.Fatalf("valid transition rejected: %v", err)
	}

	tests := []struct {
		name string
		edit func(*TransitionEvent)
	}{
		{"invalid from", func(e *TransitionEvent) { e.From = LifecycleState("PAUSED") }},
		{"invalid to", func(e *TransitionEvent) { e.To = LifecycleState("PAUSED") }},
		{"not allowed", func(e *TransitionEvent) { e.To = StateReport }},
		{"missing trigger", func(e *TransitionEvent) { e.Trigger = "" }},
		{"missing actor", func(e *TransitionEvent) { e.Actor = "" }},
		{"missing lineage hash", func(e *TransitionEvent) { e.LineageHash = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := valid
			tt.edit(&e)
			if err := ValidateTransition(e); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestClosingCoverageStatuses(t *testing.T) {
	want := []CoverageStatus{
		CoverageNoRisk,
		CoverageVerified,
		CoverageRefuted,
		CoverageDeferredBudget,
		CoverageBlocked,
		CoverageUnknown,
		CoverageNotApplicable,
	}
	got := ClosingCoverageStatuses()
	if !slices.Equal(got, want) {
		t.Fatalf("ClosingCoverageStatuses()=%v, want %v", got, want)
	}
	for _, s := range got {
		if !s.Valid() {
			t.Errorf("invalid coverage status %q", s)
		}
	}
	if slices.Contains(got, CoverageStale) {
		t.Error("stale must not be a closing status (R-7.11)")
	}
	if slices.Contains(got, CoverageCandidateOpen) {
		t.Error("candidate_open must not be a closing status (R-7.12)")
	}

	// 返回新切片：修改返回值不得影响下一次调用。
	got[0] = CoverageStale
	if again := ClosingCoverageStatuses(); again[0] != CoverageNoRisk {
		t.Fatal("ClosingCoverageStatuses returned a shared slice")
	}
}
