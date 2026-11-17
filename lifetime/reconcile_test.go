package lifetime

import (
	"testing"
	"time"
)

func recTestNow() time.Time {
	return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
}

func recTestByCheck(fs []ReconciliationFinding, check CheckID) []ReconciliationFinding {
	var out []ReconciliationFinding
	for _, f := range fs {
		if f.CheckID == string(check) {
			out = append(out, f)
		}
	}
	return out
}

func recTestFind(fs []ReconciliationFinding, check CheckID, subject string) *ReconciliationFinding {
	for i := range fs {
		if fs[i].CheckID == string(check) && fs[i].SubjectRef == subject {
			return &fs[i]
		}
	}
	return nil
}

func TestReconcileC1MissingLedgerRow(t *testing.T) {
	now := recTestNow()
	missing := CoverageUnit{
		UnitKind:     UnitKindFamily,
		FamilyID:     "fam",
		DomainPairID: "pair",
		Form:         FormRefcountRace,
		Status:       "", // enumerated without a persisted ledger row
		LineageID:    "L1",
	}
	ref := UnitID(UnitKindFamily, "fam", "", "pair", FormRefcountRace)
	snap := ReconcileSnapshot{
		TaskID:          "task-1",
		LineageID:       "L1",
		Now:             now,
		EnumeratedUnits: []CoverageUnit{missing},
	}
	fs := RunChecks(snap, 3)
	c1 := recTestByCheck(fs, CheckC1)
	if len(c1) != 1 {
		t.Fatalf("C1 findings = %d, want 1: %+v", len(c1), fs)
	}
	f := c1[0]
	if f.Severity != SeverityMedium {
		t.Fatalf("C1 severity = %q, want medium", f.Severity)
	}
	if f.Action != ActionCreateRepairTask {
		t.Fatalf("C1 action = %q, want create_repair_task", f.Action)
	}
	if f.SubjectRef != ref {
		t.Fatalf("C1 subject = %q, want %q", f.SubjectRef, ref)
	}
	if f.Status != ReconOpen {
		t.Fatalf("C1 status = %q, want open", f.Status)
	}
	if !f.CreatedAt.Equal(now) {
		t.Fatalf("C1 created_at = %v, want %v", f.CreatedAt, now)
	}
	wantKey := DedupKey(CheckC1, ref, "ledger_row_present")
	if f.DedupKey != wantKey {
		t.Fatalf("C1 dedup = %q, want %q", f.DedupKey, wantKey)
	}

	// UnitID mismatch must also be reported as a missing/mis-keyed row.
	mismatched := missing
	mismatched.UnitID = "deadbeef"
	mismatched.Status = CoverageUnexamined
	fs = RunChecks(ReconcileSnapshot{Now: now, LineageID: "L1", EnumeratedUnits: []CoverageUnit{mismatched}}, 3)
	c1 = recTestByCheck(fs, CheckC1)
	if len(c1) != 1 || c1[0].Actual != "unit_id_mismatch" {
		t.Fatalf("unit id mismatch not reported: %+v", c1)
	}

	// A well-formed row produces no C1 finding.
	valid := NewCoverageUnit(UnitKindFamily, "fam", "", "pair", FormRefcountRace, "L1")
	fs = RunChecks(ReconcileSnapshot{Now: now, LineageID: "L1", EnumeratedUnits: []CoverageUnit{valid}}, 3)
	if got := recTestByCheck(fs, CheckC1); len(got) != 0 {
		t.Fatalf("valid row must not trigger C1: %+v", got)
	}
}

func TestReconcileC5LineageStale(t *testing.T) {
	now := recTestNow()
	unit := NewCoverageUnit(UnitKindFamily, "fam", "", "pair", FormRefcountRace, "old-lineage")
	snap := ReconcileSnapshot{
		LineageID:       "new-lineage",
		Now:             now,
		EnumeratedUnits: []CoverageUnit{unit},
		Assessments: []Assessment{
			{AssessmentID: "as-1", LineageID: "old-lineage"},
		},
		Experiments: []Experiment{
			{ExperimentID: "exp-1", LineageID: "old-lineage"},
		},
	}
	fs := RunChecks(snap, 3)
	c5 := recTestByCheck(fs, CheckC5)
	if len(c5) != 3 {
		t.Fatalf("C5 findings = %d, want 3: %+v", len(c5), c5)
	}
	for _, f := range c5 {
		if f.Severity != SeverityHigh {
			t.Fatalf("C5 severity = %q, want high", f.Severity)
		}
		if f.Action != ActionMarkStale {
			t.Fatalf("C5 action = %q, want mark_stale", f.Action)
		}
	}
	as := recTestFind(c5, CheckC5, "as-1")
	if as == nil {
		t.Fatal("C5 must report the stale assessment")
	}
	wantKey := DedupKey(CheckC5, "as-1", "lineage=new-lineage")
	if as.DedupKey != wantKey {
		t.Fatalf("C5 dedup = %q, want %q", as.DedupKey, wantKey)
	}
	if recTestFind(c5, CheckC5, unit.UnitID) == nil {
		t.Fatal("C5 must report the stale coverage row")
	}

	// Up-to-date entities produce no C5 findings.
	snap.Assessments[0].LineageID = "new-lineage"
	snap.Experiments[0].LineageID = "new-lineage"
	snap.EnumeratedUnits[0].LineageID = "new-lineage"
	if got := recTestByCheck(RunChecks(snap, 3), CheckC5); len(got) != 0 {
		t.Fatalf("current lineage must not trigger C5: %+v", got)
	}
}

func TestReconcileC7QueueHealth(t *testing.T) {
	now := recTestNow()
	items := []WorkItem{
		{
			ItemID: "it-expired", QueueKind: QueueCandidateReadyVerify, State: WorkItemClaimed,
			ClaimedBySession: "s1", ClaimID: "c1",
			LeaseExpiresAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Hour),
		},
		{
			ItemID: "it-orphan", QueueKind: QueueClaimReadyReview, State: WorkItemClaimed,
			ClaimedBySession: "", ClaimID: "",
			LeaseExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(-time.Hour),
		},
		{
			ItemID: "dup-a", QueueKind: QueueClaimReadyReview, State: WorkItemReady,
			DedupKey: "dk-dup", CreatedAt: now.Add(-time.Minute),
		},
		{
			ItemID: "dup-b", QueueKind: QueueClaimReadyReview, State: WorkItemReady,
			DedupKey: "dk-dup", CreatedAt: now.Add(-time.Minute),
		},
		{
			ItemID: "starved", QueueKind: QueueUnitReadyDiscover, State: WorkItemReady,
			DedupKey: "dk-starved", CreatedAt: now.Add(-2 * time.Hour),
		},
		{
			ItemID: "healthy", QueueKind: QueueClaimReadyReview, State: WorkItemClaimed,
			ClaimedBySession: "s2", ClaimID: "c2",
			LeaseExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(-time.Hour),
		},
	}
	fs := RunChecks(ReconcileSnapshot{Now: now, WorkItems: items}, 3)
	c7 := recTestByCheck(fs, CheckC7)
	if len(c7) != 4 {
		t.Fatalf("C7 findings = %d, want 4: %+v", len(c7), c7)
	}

	expired := recTestFind(c7, CheckC7, "it-expired")
	if expired == nil || expired.Expected != "lease_active_or_released" || expired.Action != ActionRequeue {
		t.Fatalf("expired lease not reported: %+v", expired)
	}
	orphan := recTestFind(c7, CheckC7, "it-orphan")
	if orphan == nil || orphan.Expected != "claim_owner_present" || orphan.Action != ActionRequeue {
		t.Fatalf("orphan claim not reported: %+v", orphan)
	}
	dup := recTestFind(c7, CheckC7, "dup-b")
	if dup == nil {
		t.Fatalf("duplicate ready item not reported: %+v", c7)
	}
	wantKey := DedupKey(CheckC7, "dup-b", "unique_ready_dedup_key")
	if dup.DedupKey != wantKey {
		t.Fatalf("C7 dedup = %q, want %q", dup.DedupKey, wantKey)
	}
	if recTestFind(c7, CheckC7, "dup-a") != nil {
		t.Fatal("only the duplicate beyond the first must be reported")
	}
	starved := recTestFind(c7, CheckC7, "starved")
	if starved == nil || starved.Expected != "claimed_within_60m" || starved.Action != ActionNotifyPlanner {
		t.Fatalf("starvation not reported: %+v", starved)
	}
	if recTestFind(c7, CheckC7, "healthy") != nil {
		t.Fatal("healthy claimed item must not be reported")
	}

	// Items already at max attempts but still ready indicate a broken projection.
	atMax := WorkItem{ItemID: "at-max", State: WorkItemReady, Attempts: 3, DedupKey: "dk-max", CreatedAt: now}
	fs = RunChecks(ReconcileSnapshot{Now: now, WorkItems: []WorkItem{atMax}}, 3)
	c7 = recTestByCheck(fs, CheckC7)
	if len(c7) != 1 || c7[0].Expected != "attempts_below_max" || c7[0].Action != ActionNotifyPlanner {
		t.Fatalf("max-attempts inconsistency not reported: %+v", c7)
	}
}

func TestReconcileC9ReportGate(t *testing.T) {
	now := recTestNow()
	snap := ReconcileSnapshot{
		LineageID: "L1",
		Now:       now,
		Assessments: []Assessment{
			{
				AssessmentID: "as-1", CandidateID: "c1", LineageID: "L1",
				Review: Review{Verdict: VerdictConfirmed}, ReportableExternal: true,
			},
			{
				AssessmentID: "as-2", CandidateID: "c2", LineageID: "L1",
				Review: Review{Verdict: VerdictConfirmed}, ReportableExternal: true,
			},
		},
		FindingExists: map[string]bool{"c2": true, "c3": true},
	}
	fs := RunChecks(snap, 3)
	c9 := recTestByCheck(fs, CheckC9)
	if len(c9) != 2 {
		t.Fatalf("C9 findings = %d, want 2: %+v", len(c9), c9)
	}
	missing := recTestFind(c9, CheckC9, "c1")
	if missing == nil {
		t.Fatalf("missing finding gate not reported: %+v", c9)
	}
	if missing.Severity != SeverityHigh || missing.Action != ActionCreateRepairTask || missing.Actual != "finding_missing" {
		t.Fatalf("C9 forward finding = %+v", missing)
	}
	wantKey := DedupKey(CheckC9, "c1", "finding_exists_for_confirmed_candidate")
	if missing.DedupKey != wantKey {
		t.Fatalf("C9 dedup = %q, want %q", missing.DedupKey, wantKey)
	}
	orphan := recTestFind(c9, CheckC9, "c3")
	if orphan == nil || orphan.Severity != SeverityMedium || orphan.Actual != "confirmed_assessment_missing" {
		t.Fatalf("finding without confirmation not reported: %+v", orphan)
	}
	if recTestFind(c9, CheckC9, "c2") != nil {
		t.Fatal("confirmed candidate with finding must not be reported")
	}
}

func TestReconcileC11DuplicateCandidates(t *testing.T) {
	now := recTestNow()
	snap := ReconcileSnapshot{
		Now: now,
		Candidates: []Candidate{
			{CandidateID: "c1", CandidatePackage: CandidatePackage{CandidateKey: "k1", MechanismKey: "m1"}},
			{CandidateID: "c2", CandidatePackage: CandidatePackage{CandidateKey: "k1", MechanismKey: "m2"}},
			{CandidateID: "c3", CandidatePackage: CandidatePackage{CandidateKey: "k3", MechanismKey: "m1"}},
			{CandidateID: "c4", CandidatePackage: CandidatePackage{CandidateKey: "k4", MechanismKey: "m4"}},
		},
	}
	fs := RunChecks(snap, 3)
	c11 := recTestByCheck(fs, CheckC11)
	if len(c11) != 2 {
		t.Fatalf("C11 findings = %d, want 2: %+v", len(c11), c11)
	}
	ck := recTestFind(c11, CheckC11, "c1,c2")
	if ck == nil {
		t.Fatalf("candidate_key duplicate not reported: %+v", c11)
	}
	if ck.Severity != SeverityMedium || ck.Action != ActionMerge {
		t.Fatalf("C11 severity/action = %q/%q", ck.Severity, ck.Action)
	}
	wantKey := DedupKey(CheckC11, "c1,c2", "single_candidate_for_candidate_key")
	if ck.DedupKey != wantKey {
		t.Fatalf("C11 dedup = %q, want %q", ck.DedupKey, wantKey)
	}
	mk := recTestFind(c11, CheckC11, "c1,c3")
	if mk == nil || mk.Expected != "single_candidate_for_mechanism_key" {
		t.Fatalf("mechanism_key duplicate not reported: %+v", c11)
	}
	if recTestFind(c11, CheckC11, "c4") != nil {
		t.Fatal("unique candidate must not be reported")
	}
}

func TestReconcileC2ClosingCandidateOpen(t *testing.T) {
	now := recTestNow()
	unit := NewCoverageUnit(UnitKindFamily, "fam", "", "pair", FormRefcountRace, "L1")
	unit.Status = CoverageCandidateOpen
	snap := ReconcileSnapshot{
		LineageID:       "L1",
		Now:             now,
		BudgetExhausted: true,
		EnumeratedUnits: []CoverageUnit{unit},
	}
	fs := RunChecks(snap, 3)
	c2 := recTestByCheck(fs, CheckC2)
	if len(c2) != 1 {
		t.Fatalf("C2 findings = %d, want 1: %+v", len(c2), c2)
	}
	if c2[0].Severity != SeverityHigh || c2[0].Action != ActionNotifyPlanner {
		t.Fatalf("C2 finding = %+v", c2[0])
	}
	if c2[0].SubjectRef != unit.UnitID {
		t.Fatalf("C2 subject = %q, want %q", c2[0].SubjectRef, unit.UnitID)
	}
}

func TestReconcileC3CandidateStall(t *testing.T) {
	now := recTestNow()
	snap := ReconcileSnapshot{
		LineageID: "L1",
		Now:       now,
		Candidates: []Candidate{
			{CandidateID: "c1", CreatedAt: now.Add(-2 * time.Hour)},
		},
		Assessments: []Assessment{
			{AssessmentID: "as-1", CandidateID: "c1", Level: LevelS2, Status: AssessmentOpen, LineageID: "L1"},
		},
	}
	fs := RunChecks(snap, 3)
	c3 := recTestByCheck(fs, CheckC3)
	if len(c3) != 1 {
		t.Fatalf("C3 findings = %d, want 1: %+v", len(c3), c3)
	}
	f := c3[0]
	if f.Action != ActionRequeue || f.Severity != SeverityMedium || f.SubjectRef != "c1" {
		t.Fatalf("C3 finding = %+v", f)
	}
	if f.DedupKey != DedupKey(CheckC3, "c1", "verify_lease_within_60m") {
		t.Fatalf("C3 dedup = %q", f.DedupKey)
	}

	// An active Q2 lease suppresses C3.
	snap.WorkItems = []WorkItem{{
		ItemID: "q2", QueueKind: QueueCandidateReadyVerify, ItemRef: "c1",
		State: WorkItemClaimed, ClaimedBySession: "s", ClaimID: "cl",
		LeaseExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}}
	if got := recTestByCheck(RunChecks(snap, 3), CheckC3); len(got) != 0 {
		t.Fatalf("active verify lease must suppress C3: %+v", got)
	}

	// A recent candidate is not stalled.
	snap.WorkItems = nil
	snap.Candidates[0].CreatedAt = now.Add(-time.Minute)
	if got := recTestByCheck(RunChecks(snap, 3), CheckC3); len(got) != 0 {
		t.Fatalf("recent candidate must not trigger C3: %+v", got)
	}
}

func TestReconcileC4ClaimStall(t *testing.T) {
	now := recTestNow()
	snap := ReconcileSnapshot{
		LineageID: "L1",
		Now:       now,
		Assessments: []Assessment{
			{
				AssessmentID: "as-1", CandidateID: "c1", Level: LevelS3,
				LineageRole: LineageRoleClaim, Status: AssessmentOpen,
				DerivedAt: now.Add(-2 * time.Hour), LineageID: "L1",
			},
		},
		// Suppress C6 by providing a passed control experiment.
		Experiments: []Experiment{
			{ExperimentID: "ctrl-1", CandidateID: "c1", Kind: ExperimentKindControl, ControlResult: ControlResultPassed, LineageID: "L1"},
		},
	}
	fs := RunChecks(snap, 3)
	c4 := recTestByCheck(fs, CheckC4)
	if len(c4) != 1 {
		t.Fatalf("C4 findings = %d, want 1: %+v", len(c4), c4)
	}
	f := c4[0]
	if f.Action != ActionRequeue || f.Severity != SeverityMedium || f.SubjectRef != "as-1" {
		t.Fatalf("C4 finding = %+v", f)
	}
	if f.DedupKey != DedupKey(CheckC4, "as-1", "review_lease_within_60m") {
		t.Fatalf("C4 dedup = %q", f.DedupKey)
	}

	snap.WorkItems = []WorkItem{{
		ItemID: "q3", QueueKind: QueueClaimReadyReview, ItemRef: "as-1",
		State: WorkItemClaimed, ClaimedBySession: "s", ClaimID: "cl",
		LeaseExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}}
	if got := recTestByCheck(RunChecks(snap, 3), CheckC4); len(got) != 0 {
		t.Fatalf("active review lease must suppress C4: %+v", got)
	}
}

func TestReconcileC6ControlMissing(t *testing.T) {
	now := recTestNow()
	assessment := Assessment{
		AssessmentID: "as-1", CandidateID: "c1", Level: LevelS3,
		LineageRole: LineageRoleClaim, Status: AssessmentOpen,
		DerivedAt: now, LineageID: "L1",
	}
	base := ReconcileSnapshot{LineageID: "L1", Now: now, Assessments: []Assessment{assessment}}

	fs := RunChecks(base, 3)
	c6 := recTestByCheck(fs, CheckC6)
	if len(c6) != 1 || c6[0].Actual != "control_missing" {
		t.Fatalf("missing control not reported: %+v", c6)
	}
	if c6[0].Severity != SeverityHigh || c6[0].Action != ActionCreateRepairTask {
		t.Fatalf("C6 missing finding = %+v", c6[0])
	}

	failed := base
	failed.Experiments = []Experiment{
		{ExperimentID: "ctrl-1", CandidateID: "c1", Kind: ExperimentKindControl, ControlResult: ControlResultFailed, LineageID: "L1"},
	}
	c6 = recTestByCheck(RunChecks(failed, 3), CheckC6)
	if len(c6) != 1 || c6[0].Actual != "control_failed" || c6[0].Action != ActionMarkStale {
		t.Fatalf("failed control not reported: %+v", c6)
	}

	passed := base
	passed.Experiments = []Experiment{
		{ExperimentID: "ctrl-1", CandidateID: "c1", Kind: ExperimentKindControl, ControlResult: ControlResultPassed, LineageID: "L1"},
	}
	if got := recTestByCheck(RunChecks(passed, 3), CheckC6); len(got) != 0 {
		t.Fatalf("passed control must not trigger C6: %+v", got)
	}
}

func TestReconcileC8NoRiskIncomplete(t *testing.T) {
	now := recTestNow()
	cases := []struct {
		name string
		nr   NoRisk
		want string
	}{
		{
			name: "covers_current_form_false",
			nr:   NoRisk{NoRiskID: "nr-1", Form: FormRefcountRace, CoversCurrentForm: false},
			want: "covers_current_form_false",
		},
		{
			name: "arguments_missing_form_coverage",
			nr: NoRisk{
				NoRiskID: "nr-2", Form: FormRefcountRace, CoversCurrentForm: true,
				Arguments: []NoRiskArgument{{ArgumentType: NoRiskSerialized, CoversForms: []Form{FormSharedPtrRace}}},
			},
			want: "arguments_missing_form_coverage",
		},
		{
			name: "argument_access_set_mismatch",
			nr: NoRisk{
				NoRiskID: "nr-3", Form: FormRefcountRace, CoversCurrentForm: true,
				Arguments: []NoRiskArgument{
					{ArgumentType: NoRiskSerialized, CoversForms: []Form{FormRefcountRace}, AccessSet: []Access{{Site: "a", Domain: "d"}}},
					{ArgumentType: NoRiskGuarded, CoversForms: []Form{FormRefcountRace}, AccessSet: []Access{{Site: "b", Domain: "d"}}},
				},
			},
			want: "argument_1_access_set_mismatch",
		},
	}
	for _, tc := range cases {
		fs := RunChecks(ReconcileSnapshot{Now: now, NoRisks: []NoRisk{tc.nr}}, 3)
		c8 := recTestByCheck(fs, CheckC8)
		if len(c8) != 1 {
			t.Fatalf("%s: C8 findings = %d, want 1: %+v", tc.name, len(c8), c8)
		}
		if c8[0].Actual != tc.want {
			t.Fatalf("%s: actual = %q, want %q", tc.name, c8[0].Actual, tc.want)
		}
		if c8[0].Severity != SeverityMedium || c8[0].Action != ActionCreateRepairTask {
			t.Fatalf("%s: finding = %+v", tc.name, c8[0])
		}
	}
}

func TestReconcileC10BudgetExhausted(t *testing.T) {
	now := recTestNow()
	openUnit := NewCoverageUnit(UnitKindFamily, "fam", "", "pair", FormRefcountRace, "L1")
	deferred := NewCoverageUnit(UnitKindFamily, "fam", "", "pair", FormSharedPtrRace, "L1")
	deferred.Status = CoverageDeferredBudget
	deferred.Reason = "budget exhausted"
	snap := ReconcileSnapshot{
		LineageID:       "L1",
		Now:             now,
		BudgetExhausted: true,
		EnumeratedUnits: []CoverageUnit{openUnit, deferred},
	}
	fs := RunChecks(snap, 3)
	c10 := recTestByCheck(fs, CheckC10)
	if len(c10) != 1 {
		t.Fatalf("C10 findings = %d, want 1: %+v", len(c10), c10)
	}
	if c10[0].SubjectRef != openUnit.UnitID {
		t.Fatalf("C10 subject = %q, want %q", c10[0].SubjectRef, openUnit.UnitID)
	}
	if c10[0].Severity != SeverityHigh || c10[0].Action != ActionNotifyPlanner {
		t.Fatalf("C10 finding = %+v", c10[0])
	}
	if recTestFind(c10, CheckC10, deferred.UnitID) != nil {
		t.Fatal("deferred_budget unit must not be reported")
	}
}

func TestReconcileC12OwnerJournalStale(t *testing.T) {
	now := recTestNow()
	snap := ReconcileSnapshot{
		LineageID: "L2",
		Now:       now,
		Assessments: []Assessment{
			{AssessmentID: "as-1", LineageID: "L1"},
		},
		OwnerJournal: []OwnerJournalEntry{
			{EntryID: "j1", Stale: false, Refs: []string{"as-1"}, SourceHash: "s1", IndexHash: "i1"},
			{EntryID: "j2", Stale: false, Refs: nil, SourceHash: "s1", IndexHash: "i1"},
			{EntryID: "j3", Stale: true, Refs: []string{"as-1"}, SourceHash: "s1", IndexHash: "i1"},
			{EntryID: "j4", Stale: false, SourceHash: "", IndexHash: "i1"},
		},
	}
	fs := RunChecks(snap, 3)
	c12 := recTestByCheck(fs, CheckC12)
	if len(c12) != 2 {
		t.Fatalf("C12 findings = %d, want 2: %+v", len(c12), c12)
	}
	j1 := recTestFind(c12, CheckC12, "j1")
	if j1 == nil || j1.Actual != "refs_stale_lineage" {
		t.Fatalf("stale ref not reported: %+v", c12)
	}
	if j1.Severity != SeverityLow || j1.Action != ActionMarkStale {
		t.Fatalf("C12 finding = %+v", j1)
	}
	if recTestFind(c12, CheckC12, "j4") == nil {
		t.Fatal("journal entry without source hash must be marked stale")
	}
	if recTestFind(c12, CheckC12, "j2") != nil {
		t.Fatal("current journal entry must not be reported")
	}
	if recTestFind(c12, CheckC12, "j3") != nil {
		t.Fatal("already-stale entry must not be re-reported")
	}
}

func TestDedupAgainstExistingKeys(t *testing.T) {
	findings := []ReconciliationFinding{
		{SubjectRef: "s1", DedupKey: "k1"},
		{SubjectRef: "s2", DedupKey: "k2"},
		{SubjectRef: "s3", DedupKey: ""},
		{SubjectRef: "s4", DedupKey: "k3"},
	}
	out := DedupAgainstExisting(findings, []string{"k2"})
	if len(out) != 3 {
		t.Fatalf("filtered findings = %d, want 3", len(out))
	}
	if out[0].DedupKey != "k1" || out[1].DedupKey != "" || out[2].DedupKey != "k3" {
		t.Fatalf("unexpected order/content: %+v", out)
	}
	if got := DedupAgainstExisting(findings, nil); len(got) != 4 {
		t.Fatalf("nil existing must keep all findings, got %d", len(got))
	}
	if got := DedupAgainstExisting(findings, []string{"k1", "k2", "k3"}); len(got) != 1 || got[0].DedupKey != "" {
		t.Fatalf("empty dedup_key must be preserved: %+v", got)
	}
}

func TestCheckDedupKeyStability(t *testing.T) {
	a := DedupKey(CheckC1, "subject", "expected")
	if a == "" {
		t.Fatal("dedup key must not be empty")
	}
	if DedupKey(CheckC1, "subject", "expected") != a {
		t.Fatal("dedup key must be deterministic")
	}
	if DedupKey(CheckC2, "subject", "expected") == a {
		t.Fatal("dedup key must depend on check_id")
	}
	if DedupKey(CheckC1, "subject-2", "expected") == a {
		t.Fatal("dedup key must depend on subject_ref")
	}
	if DedupKey(CheckC1, "subject", "expected-2") == a {
		t.Fatal("dedup key must depend on expected state")
	}
}

func TestReconcileNoFindingsForCleanSnapshot(t *testing.T) {
	now := recTestNow()
	unit := NewCoverageUnit(UnitKindFamily, "fam", "", "pair", FormRefcountRace, "L1")
	unit.Status = CoverageNoRisk
	unit.ReviewStatus = coverageReviewReviewed
	unit.ProofRef = "proof:1"
	snap := ReconcileSnapshot{
		LineageID:       "L1",
		Now:             now,
		EnumeratedUnits: []CoverageUnit{unit},
		NoRisks: []NoRisk{{
			NoRiskID: "nr-1", Form: FormRefcountRace, CoversCurrentForm: true,
			Arguments: []NoRiskArgument{{
				ArgumentType: NoRiskSerialized, CoversForms: []Form{FormRefcountRace},
				Sync: []SyncPrimitive{{Protects: "mu"}},
			}},
		}},
	}
	if fs := RunChecks(snap, 3); len(fs) != 0 {
		t.Fatalf("clean snapshot must produce no findings: %+v", fs)
	}
}
