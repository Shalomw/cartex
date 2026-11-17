package lifetime

import (
	"math"
	"testing"
	"time"
)

func covTestNearlyEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func covTestUnit(status CoverageStatus, form Form) CoverageUnit {
	return CoverageUnit{
		UnitID:       UnitID(UnitKindFamily, "fam", "", "pair", form),
		UnitKind:     UnitKindFamily,
		FamilyID:     "fam",
		DomainPairID: "pair",
		Form:         form,
		Status:       status,
		ReviewStatus: coverageReviewSelfAsserted,
		LineageID:    "lineage-1",
	}
}

func TestCoverageUnitIDDeterministic(t *testing.T) {
	a := UnitID(UnitKindFamily, "fam", "", "pair", FormRefcountRace)
	b := UnitID(UnitKindFamily, "fam", "", "pair", FormRefcountRace)
	if a != b {
		t.Fatalf("UnitID not deterministic: %s != %s", a, b)
	}
	if a == "" {
		t.Fatal("UnitID returned empty string")
	}
	if UnitID(UnitKindFamily, "fam", "", "pair", FormDanglingThis) == a {
		t.Fatal("UnitID must depend on form")
	}
	if UnitID(UnitKindFamily, "fam2", "", "pair", FormRefcountRace) == a {
		t.Fatal("UnitID must depend on family_id")
	}
	if UnitID(UnitKindRelation, "", "rel-1", "pair", FormRefcountRace) == a {
		t.Fatal("UnitID must depend on relation_id")
	}

	u := NewCoverageUnit(UnitKindRelation, "", "rel-1", "pair", FormDanglingThis, "lineage-x")
	if u.UnitID != UnitID(UnitKindRelation, "", "rel-1", "pair", FormDanglingThis) {
		t.Fatalf("NewCoverageUnit unit_id mismatch: %s", u.UnitID)
	}
	if u.Status != CoverageUnexamined {
		t.Fatalf("new unit status = %q, want unexamined", u.Status)
	}
	if u.ReviewStatus != coverageReviewSelfAsserted {
		t.Fatalf("new unit review_status = %q, want self_asserted", u.ReviewStatus)
	}
	if u.LineageID != "lineage-x" {
		t.Fatalf("new unit lineage = %q", u.LineageID)
	}
}

func TestNoRiskCoversFormPredicate(t *testing.T) {
	n := NoRisk{
		NoRiskID: "nr-1",
		Form:     FormRefcountRace,
		Arguments: []NoRiskArgument{
			{ArgumentType: NoRiskSerialized, CoversForms: []Form{FormRefcountRace, FormSharedPtrRace}},
		},
		CoversCurrentForm: true,
	}
	if !NoRiskCoversForm(n, FormRefcountRace) {
		t.Fatal("expected F1 covered")
	}
	if !NoRiskCoversForm(n, FormSharedPtrRace) {
		t.Fatal("expected F6 covered")
	}
	if NoRiskCoversForm(n, FormDanglingThis) {
		t.Fatal("F2 must not be covered")
	}
	// CoversCurrentForm is a platform-derived flag; the predicate only looks
	// at arguments.covers_forms.
	n.CoversCurrentForm = false
	if !NoRiskCoversForm(n, FormRefcountRace) {
		t.Fatal("predicate must ignore CoversCurrentForm")
	}
	if NoRiskCoversForm(NoRisk{}, FormRefcountRace) {
		t.Fatal("no arguments must not cover any form")
	}
}

func TestNoRiskAccessSetValidation(t *testing.T) {
	expected := []Access{
		{Site: "src/a.cc:10", Domain: "d1", GuardedBy: "mu"},
		{Site: "src/a.cc:20", Domain: "d2", GuardedBy: ""},
	}
	n := NoRisk{
		NoRiskID: "nr-1",
		Arguments: []NoRiskArgument{
			{ArgumentType: NoRiskSerialized, AccessSet: expected},
		},
	}
	if err := ValidateNoRiskAccessSet(n, expected); err != nil {
		t.Fatalf("matching access set rejected: %v", err)
	}

	missing := NoRisk{
		NoRiskID: "nr-2",
		Arguments: []NoRiskArgument{
			{ArgumentType: NoRiskSerialized, AccessSet: expected[:1]},
		},
	}
	if err := ValidateNoRiskAccessSet(missing, expected); err == nil {
		t.Fatal("missing access must be rejected (R-4.25)")
	}

	extra := NoRisk{
		NoRiskID: "nr-3",
		Arguments: []NoRiskArgument{
			{ArgumentType: NoRiskSerialized, AccessSet: append(append([]Access{}, expected...), Access{Site: "src/b.cc:1"})},
		},
	}
	if err := ValidateNoRiskAccessSet(extra, expected); err == nil {
		t.Fatal("extra access must be rejected (R-4.25)")
	}

	multi := NoRisk{
		NoRiskID: "nr-4",
		Arguments: []NoRiskArgument{
			{ArgumentType: NoRiskSerialized, AccessSet: expected},
			{ArgumentType: NoRiskGuarded, AccessSet: expected[:1]},
		},
	}
	if err := ValidateNoRiskAccessSet(multi, expected); err == nil {
		t.Fatal("each argument must match the unit access set")
	}
}

func TestNoRiskArgumentValidation(t *testing.T) {
	proof := Proof{Kind: "entry_scope", Refs: []string{"fact-1"}}
	cases := []struct {
		name    string
		arg     NoRiskArgument
		wantErr bool
	}{
		{
			name:    "serialized_without_sync",
			arg:     NoRiskArgument{ArgumentType: NoRiskSerialized, CoversForms: []Form{FormRefcountRace}},
			wantErr: true,
		},
		{
			name: "serialized_sync_without_protects",
			arg: NoRiskArgument{
				ArgumentType: NoRiskSerialized,
				Sync:         []SyncPrimitive{{Primitive: "mutex", Scope: "member"}},
			},
			wantErr: true,
		},
		{
			name: "guarded_with_sync",
			arg: NoRiskArgument{
				ArgumentType: NoRiskGuarded,
				Sync:         []SyncPrimitive{{Primitive: "mutex", Scope: "member", Protects: "all accesses"}},
			},
			wantErr: false,
		},
		{
			name: "relation_safe_minimal",
			arg:  NoRiskArgument{ArgumentType: NoRiskRelationSafe},
		},
		{
			name:    "unreachable_without_proof",
			arg:     NoRiskArgument{ArgumentType: NoRiskUnreachable},
			wantErr: true,
		},
		{
			name:    "unreachable_with_proof",
			arg:     NoRiskArgument{ArgumentType: NoRiskUnreachable, Proof: proof},
			wantErr: false,
		},
		{
			name:    "unreachable_proof_without_refs",
			arg:     NoRiskArgument{ArgumentType: NoRiskUnreachable, Proof: Proof{Kind: "entry_scope"}},
			wantErr: true,
		},
		{
			name:    "generation_without_proof",
			arg:     NoRiskArgument{ArgumentType: NoRiskGeneration},
			wantErr: true,
		},
		{
			name:    "generation_with_proof",
			arg:     NoRiskArgument{ArgumentType: NoRiskGeneration, Proof: proof},
			wantErr: false,
		},
		{
			name: "citation_unresolved",
			arg: NoRiskArgument{
				ArgumentType:  NoRiskUnreachable,
				Proof:         proof,
				CitationCheck: []CitationCheck{{Status: CitationUnresolved, File: "a.cc", Line: 1}},
			},
			wantErr: true,
		},
		{
			name: "citation_verified",
			arg: NoRiskArgument{
				ArgumentType:  NoRiskUnreachable,
				Proof:         proof,
				CitationCheck: []CitationCheck{{Status: CitationVerified, File: "a.cc", Line: 1}},
			},
			wantErr: false,
		},
	}
	for _, tc := range cases {
		n := NoRisk{NoRiskID: "nr", Arguments: []NoRiskArgument{tc.arg}}
		err := ValidateNoRiskArgument(n, "lineage-1")
		if tc.wantErr && err == nil {
			t.Fatalf("%s: expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.name, err)
		}
	}
}

func TestCoverageCloseValidation(t *testing.T) {
	goodNoRisk := NoRisk{
		NoRiskID: "nr-ok",
		Form:     FormRefcountRace,
		Arguments: []NoRiskArgument{
			{ArgumentType: NoRiskSerialized, CoversForms: []Form{FormRefcountRace}, Sync: []SyncPrimitive{{Protects: "mu"}}},
		},
		CoversCurrentForm: true,
	}
	wrongFormNoRisk := NoRisk{
		NoRiskID: "nr-wrong",
		Form:     FormSharedPtrRace,
		Arguments: []NoRiskArgument{
			{ArgumentType: NoRiskSerialized, CoversForms: []Form{FormSharedPtrRace}},
		},
		CoversCurrentForm: true,
	}
	naNoProof := covTestUnit(CoverageNotApplicable, FormRefcountRace)
	naWithProof := naNoProof
	naWithProof.ProofRef = "proof:pair"
	deferredNoReason := covTestUnit(CoverageDeferredBudget, FormRefcountRace)
	deferredWithReason := deferredNoReason
	deferredWithReason.Reason = "budget exhausted"

	cases := []struct {
		name    string
		unit    CoverageUnit
		noRisk  *NoRisk
		total   int
		refuted int
		conf    DomainConfidence
		wantErr bool
	}{
		{"refuted_all", covTestUnit(CoverageRefuted, FormRefcountRace), nil, 3, 3, DomainConfidenceHigh, false},
		{"refuted_partial", covTestUnit(CoverageRefuted, FormRefcountRace), nil, 3, 2, DomainConfidenceHigh, true},
		{"refuted_zero", covTestUnit(CoverageRefuted, FormRefcountRace), nil, 0, 0, DomainConfidenceHigh, true},
		{"verified_resolved", covTestUnit(CoverageVerified, FormRefcountRace), nil, 2, 0, DomainConfidenceHigh, false},
		{"verified_zero", covTestUnit(CoverageVerified, FormRefcountRace), nil, 0, 0, DomainConfidenceHigh, true},
		{"no_risk_ok", covTestUnit(CoverageNoRisk, FormRefcountRace), &goodNoRisk, 0, 0, DomainConfidenceHigh, false},
		{"no_risk_nil", covTestUnit(CoverageNoRisk, FormRefcountRace), nil, 0, 0, DomainConfidenceHigh, true},
		{"no_risk_wrong_form", covTestUnit(CoverageNoRisk, FormRefcountRace), &wrongFormNoRisk, 0, 0, DomainConfidenceHigh, true},
		{"no_risk_low_confidence", covTestUnit(CoverageNoRisk, FormRefcountRace), &goodNoRisk, 0, 0, DomainConfidenceLow, true},
		{"unknown_low_confidence", covTestUnit(CoverageUnknown, FormRefcountRace), nil, 0, 0, DomainConfidenceLow, false},
		{"verified_low_confidence", covTestUnit(CoverageVerified, FormRefcountRace), nil, 1, 0, DomainConfidenceLow, true},
		{"not_applicable_without_proof", naNoProof, nil, 0, 0, DomainConfidenceHigh, true},
		{"not_applicable_with_proof", naWithProof, nil, 0, 0, DomainConfidenceHigh, false},
		{"candidate_open_rejected", covTestUnit(CoverageCandidateOpen, FormRefcountRace), nil, 0, 0, DomainConfidenceHigh, true},
		{"deferred_without_reason", deferredNoReason, nil, 0, 0, DomainConfidenceHigh, true},
		{"deferred_with_reason", deferredWithReason, nil, 0, 0, DomainConfidenceHigh, false},
		{"unexamined_rejected", covTestUnit(CoverageUnexamined, FormRefcountRace), nil, 0, 0, DomainConfidenceHigh, true},
		{"stale_rejected", covTestUnit(CoverageStale, FormRefcountRace), nil, 0, 0, DomainConfidenceHigh, true},
	}
	for _, tc := range cases {
		err := ValidateCoverageClose(tc.unit, tc.noRisk, tc.total, tc.refuted, tc.conf)
		if tc.wantErr && err == nil {
			t.Fatalf("%s: expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.name, err)
		}
	}
}

func TestCoverageStatsFormula(t *testing.T) {
	units := []CoverageUnit{
		{UnitID: "u1", Status: CoverageNoRisk, ReviewStatus: coverageReviewReviewed},
		{UnitID: "u2", Status: CoverageNoRisk, ReviewStatus: coverageReviewReviewed},
		{UnitID: "u3", Status: CoverageVerified, ReviewStatus: coverageReviewReviewed},
		{UnitID: "u4", Status: CoverageRefuted, ReviewStatus: coverageReviewReviewed},
		{UnitID: "u5", Status: CoverageRefuted, ReviewStatus: coverageReviewReviewed},
		{UnitID: "u6", Status: CoverageRefuted, ReviewStatus: coverageReviewSelfAsserted},
		{UnitID: "u7", Status: CoverageUnexamined, ReviewStatus: coverageReviewSelfAsserted},
		{UnitID: "u8", Status: CoverageUnexamined, ReviewStatus: coverageReviewSelfAsserted},
		{UnitID: "u9", Status: CoverageExaminedUnreviewed, ReviewStatus: coverageReviewSelfAsserted},
		{UnitID: "u10", Status: CoverageCandidateOpen, ReviewStatus: coverageReviewSelfAsserted},
	}
	st := ComputeCoverageStats(units, 5)

	if st.EnumeratedUnits != 10 {
		t.Errorf("enumerated = %d, want 10", st.EnumeratedUnits)
	}
	if st.ConcludedUnits != 6 {
		t.Errorf("concluded = %d, want 6", st.ConcludedUnits)
	}
	if st.RiskClosedUnits != 5 {
		t.Errorf("risk_closed = %d, want 5", st.RiskClosedUnits)
	}
	if st.FindingUnits != 1 {
		t.Errorf("finding_units = %d, want 1", st.FindingUnits)
	}
	if st.UnknownRows != 0 {
		t.Errorf("unknown_rows = %d, want 0", st.UnknownRows)
	}
	if st.CandidateUnits != 1 {
		t.Errorf("candidate_units = %d, want 1", st.CandidateUnits)
	}
	if st.ExaminedUnits != 7 {
		t.Errorf("examined_units = %d, want 7 (6 concluded + 1 candidate_open)", st.ExaminedUnits)
	}
	if !covTestNearlyEqual(st.CandidateRate, 10) {
		t.Errorf("candidate_rate = %v, want 10", st.CandidateRate)
	}
	if st.ReviewedConcludedUnits != 5 {
		t.Errorf("reviewed_concluded = %d, want 5", st.ReviewedConcludedUnits)
	}
	if st.ReviewedRiskClosedUnits != 4 {
		t.Errorf("reviewed_risk_closed = %d, want 4", st.ReviewedRiskClosedUnits)
	}
	if !covTestNearlyEqual(st.CoveragePct, 60) {
		t.Errorf("coverage_pct = %v, want 60", st.CoveragePct)
	}
	if !covTestNearlyEqual(st.RiskClosedPctAll, 50) {
		t.Errorf("risk_closed_pct_all = %v, want 50", st.RiskClosedPctAll)
	}
	if !covTestNearlyEqual(st.RiskClosedPctReviewed, 80) {
		t.Errorf("risk_closed_pct_reviewed = %v, want 80", st.RiskClosedPctReviewed)
	}
	if !covTestNearlyEqual(st.HitRateUnits, 10) {
		t.Errorf("hit_rate_units = %v, want 10", st.HitRateUnits)
	}
	if !covTestNearlyEqual(st.UnknownRate, 5.0/15.0*100) {
		t.Errorf("unknown_rate = %v, want %v", st.UnknownRate, 5.0/15.0*100)
	}
	if !covTestNearlyEqual(st.ReviewedConcludedRate, 5.0/6.0*100) {
		t.Errorf("reviewed_concluded_rate = %v, want %v", st.ReviewedConcludedRate, 5.0/6.0*100)
	}
}

func TestCoverageStatsZeroDenominator(t *testing.T) {
	st := ComputeCoverageStats(nil, 0)
	if st.EnumeratedUnits != 0 || st.CoveragePct != 0 || st.UnknownRate != 0 || st.ReviewedConcludedRate != 0 {
		t.Fatalf("empty stats must be zero, got %+v", st)
	}

	// not_applicable 移出分母（R-4.22）。
	st = ComputeCoverageStats([]CoverageUnit{
		covTestUnit(CoverageNotApplicable, FormRefcountRace),
		covTestUnit(CoverageNoRisk, FormSharedPtrRace),
	}, 0)
	if st.EnumeratedUnits != 1 {
		t.Fatalf("enumerated = %d, want 1 (not_applicable excluded)", st.EnumeratedUnits)
	}
	if !covTestNearlyEqual(st.CoveragePct, 100) {
		t.Fatalf("coverage_pct = %v, want 100", st.CoveragePct)
	}

	// unknown 行计入分母与 unknown_rows。
	st = ComputeCoverageStats([]CoverageUnit{
		covTestUnit(CoverageUnknown, FormRefcountRace),
	}, 2)
	if st.EnumeratedUnits != 1 || st.UnknownRows != 1 {
		t.Fatalf("unknown stats = %+v", st)
	}
	if !covTestNearlyEqual(st.UnknownRate, 100) {
		t.Fatalf("unknown_rate = %v, want 100", st.UnknownRate)
	}
}

func TestCoverageApplyStatus(t *testing.T) {
	u := NewCoverageUnit(UnitKindFamily, "fam", "", "pair", FormRefcountRace, "lineage-1")
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if err := ApplyCoverageStatusAt(&u, CoverageVerified, "S3 claim", "evidence-1", "", coverageReviewReviewed, at); err != nil {
		t.Fatalf("ApplyCoverageStatusAt: %v", err)
	}
	if u.Status != CoverageVerified || u.Reason != "S3 claim" || u.EvidenceRef != "evidence-1" {
		t.Fatalf("status fields not applied: %+v", u)
	}
	if u.ReviewStatus != coverageReviewReviewed {
		t.Fatalf("review_status = %q", u.ReviewStatus)
	}
	if !u.UpdatedAt.Equal(at) {
		t.Fatalf("updated_at = %v, want %v", u.UpdatedAt, at)
	}

	if err := ApplyCoverageStatusAt(&u, CoverageStatus("bogus"), "", "", "", coverageReviewReviewed, at); err == nil {
		t.Fatal("invalid status must be rejected")
	}
	if err := ApplyCoverageStatusAt(&u, CoverageNoRisk, "", "", "proof", "trusted", at); err == nil {
		t.Fatal("invalid review_status must be rejected")
	}
	if err := ApplyCoverageStatusAt(nil, CoverageNoRisk, "", "", "", coverageReviewReviewed, at); err == nil {
		t.Fatal("nil unit must be rejected")
	}

	if err := ApplyCoverageStatus(&u, CoverageNoRisk, "covered", "", "proof-1", coverageReviewReviewed, "lineage-2"); err != nil {
		t.Fatalf("ApplyCoverageStatus: %v", err)
	}
	if u.LineageID != "lineage-2" {
		t.Fatalf("lineage = %q, want lineage-2", u.LineageID)
	}
	if u.UpdatedAt.IsZero() {
		t.Fatal("ApplyCoverageStatus must set UpdatedAt")
	}
	before := u.UpdatedAt
	if err := ApplyCoverageStatus(&u, CoverageRefuted, "all refuted", "", "", coverageReviewReviewed, ""); err != nil {
		t.Fatalf("ApplyCoverageStatus: %v", err)
	}
	if u.LineageID != "lineage-2" {
		t.Fatalf("empty lineage must not clear existing lineage, got %q", u.LineageID)
	}
	if u.UpdatedAt.Before(before) {
		t.Fatal("UpdatedAt must move forward")
	}
}

func TestCoverageCanCloseUnit(t *testing.T) {
	for _, st := range []CoverageStatus{CoverageNoRisk, CoverageVerified, CoverageRefuted} {
		if !CanCloseUnit(CoverageUnit{Status: st}) {
			t.Fatalf("status %q must be closable", st)
		}
	}
	for _, st := range []CoverageStatus{CoverageUnexamined, CoverageCandidateOpen, CoverageUnknown, CoverageStale, CoverageNotApplicable} {
		if CanCloseUnit(CoverageUnit{Status: st}) {
			t.Fatalf("status %q must not be closable", st)
		}
	}
}
