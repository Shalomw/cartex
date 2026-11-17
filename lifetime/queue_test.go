package lifetime

import (
	"testing"
	"time"
)

func qTestCandidate() Candidate {
	return Candidate{
		CandidateID:      "cand-1",
		CandidatePackage: CandidatePackage{ContractStatus: ContractKnown},
		Budget:           CandidateBudget{Remaining: 2},
	}
}

func TestReadyQ1Predicate(t *testing.T) {
	pkg := WorkPackage{
		PackageID:       "pkg-1",
		FamilyID:        "fam",
		DomainPairID:    "pair",
		ApplicableForms: []Form{FormRefcountRace},
	}
	openUnits := []CoverageUnit{
		{FamilyID: "fam", DomainPairID: "pair", Form: FormRefcountRace, Status: CoverageUnexamined},
	}
	if !ReadyQ1(pkg, openUnits, true) {
		t.Fatal("Q1 must be ready with an open form")
	}
	closedUnits := []CoverageUnit{
		{FamilyID: "fam", DomainPairID: "pair", Form: FormRefcountRace, Status: CoverageNoRisk},
		{FamilyID: "fam", DomainPairID: "pair", Form: FormRefcountRace, Status: CoverageVerified},
		{FamilyID: "fam", DomainPairID: "pair", Form: FormRefcountRace, Status: CoverageRefuted},
		{FamilyID: "fam", DomainPairID: "pair", Form: FormRefcountRace, Status: CoverageNotApplicable},
	}
	if ReadyQ1(pkg, closedUnits, true) {
		t.Fatal("Q1 must not be ready when all forms are closed")
	}
	leased := pkg
	leased.LeaseOwner = "owner-session"
	if ReadyQ1(leased, openUnits, true) {
		t.Fatal("Q1 must not be ready while a package lease is held")
	}
	if ReadyQ1(pkg, openUnits, false) {
		t.Fatal("Q1 must not be ready when index is unavailable")
	}
	noForms := pkg
	noForms.ApplicableForms = nil
	if ReadyQ1(noForms, openUnits, true) {
		t.Fatal("Q1 must not be ready when applicable_forms is empty")
	}
	otherFamily := []CoverageUnit{
		{FamilyID: "other", DomainPairID: "pair", Form: FormRefcountRace, Status: CoverageUnexamined},
	}
	if ReadyQ1(pkg, otherFamily, true) {
		t.Fatal("Q1 must only consider the package's own units")
	}
	// candidate_open / unknown / stale / deferred_budget all keep Q1 open.
	for _, st := range []CoverageStatus{CoverageCandidateOpen, CoverageUnknown, CoverageStale, CoverageDeferredBudget} {
		units := []CoverageUnit{{FamilyID: "fam", DomainPairID: "pair", Form: FormRefcountRace, Status: st}}
		if !ReadyQ1(pkg, units, true) {
			t.Fatalf("Q1 must be ready for open status %q", st)
		}
	}
}

func TestReadyQ2Predicate(t *testing.T) {
	c := qTestCandidate()

	if !ReadyQ2(c, Assessment{Level: LevelS1}, VerifyModeProbe, DriverMissing) {
		t.Fatal("probe must accept S1")
	}
	if !ReadyQ2(c, Assessment{Level: LevelS2, Provisional: true}, VerifyModeProbe, DriverDraft) {
		t.Fatal("probe must accept provisional assessments")
	}
	if ReadyQ2(c, Assessment{Level: LevelS2}, VerifyModeProbe, DriverMissing) {
		t.Fatal("probe must reject non-provisional S2")
	}
	if !ReadyQ2(c, Assessment{Level: LevelS2}, VerifyModeVerify, DriverMissing) {
		t.Fatal("verify must accept S2 with missing driver")
	}
	for _, driver := range []DriverSpecState{DriverStale, DriverRejected} {
		if !ReadyQ2(c, Assessment{Level: LevelS2}, VerifyModeVerify, driver) {
			t.Fatalf("verify must accept driver state %q", driver)
		}
	}
	for _, driver := range []DriverSpecState{DriverBuilt, DriverDraft} {
		if ReadyQ2(c, Assessment{Level: LevelS2}, VerifyModeVerify, driver) {
			t.Fatalf("verify must reject built/draft driver %q", driver)
		}
	}
	if ReadyQ2(c, Assessment{Level: LevelS1}, VerifyModeVerify, DriverMissing) {
		t.Fatal("verify must require S2")
	}

	conflicting := qTestCandidate()
	conflicting.ContractStatus = ContractConflicting
	if ReadyQ2(conflicting, Assessment{Level: LevelS1}, VerifyModeProbe, DriverMissing) {
		t.Fatal("conflicting contract must not be ready")
	}
	brokeBudget := qTestCandidate()
	brokeBudget.Budget.Remaining = 0
	if ReadyQ2(brokeBudget, Assessment{Level: LevelS1}, VerifyModeProbe, DriverMissing) {
		t.Fatal("exhausted candidate budget must not be ready")
	}
	if ReadyQ2(c, Assessment{Level: LevelS1}, VerifyMode("bogus"), DriverMissing) {
		t.Fatal("unknown verify mode must not be ready")
	}
}

func TestReadyQ3Predicate(t *testing.T) {
	base := Assessment{
		AssessmentID: "as-1",
		Level:        LevelS3,
		LineageRole:  LineageRoleClaim,
		Status:       AssessmentOpen,
	}
	if !ReadyQ3(base) {
		t.Fatal("S3 claim must be ready for review")
	}
	s4 := base
	s4.Level = LevelS4
	if !ReadyQ3(s4) {
		t.Fatal("S4 claim must be ready for review")
	}
	stale := base
	stale.Stale = true
	if ReadyQ3(stale) {
		t.Fatal("stale assessment must not be ready")
	}
	confirmed := base
	confirmed.Review.Verdict = VerdictConfirmed
	if ReadyQ3(confirmed) {
		t.Fatal("confirmed assessment must not be ready")
	}
	s2 := base
	s2.Level = LevelS2
	if ReadyQ3(s2) {
		t.Fatal("S2 assessment must not be ready")
	}
	confirmation := base
	confirmation.LineageRole = LineageRoleConfirmation
	if ReadyQ3(confirmation) {
		t.Fatal("confirmation role must not be ready")
	}
	closed := base
	closed.Status = AssessmentRefuted
	if ReadyQ3(closed) {
		t.Fatal("non-open assessment must not be ready")
	}
}

func TestReadyQ4Predicate(t *testing.T) {
	a := Assessment{
		Review:             Review{Verdict: VerdictConfirmed},
		ReportableExternal: true,
	}
	if !ReadyQ4(a, false) {
		t.Fatal("confirmed external assessment must be ready")
	}
	if ReadyQ4(a, true) {
		t.Fatal("existing finding must block Q4")
	}
	notExternal := a
	notExternal.ReportableExternal = false
	if ReadyQ4(notExternal, false) {
		t.Fatal("non-external assessment must not be ready")
	}
	unconfirmed := a
	unconfirmed.Review.Verdict = VerdictNone
	if ReadyQ4(unconfirmed, false) {
		t.Fatal("unconfirmed assessment must not be ready")
	}
}

func TestReadyQ5Predicate(t *testing.T) {
	base := ReconciliationFinding{Severity: SeverityMedium, Action: ActionRequeue}
	if !ReadyQ5(base) {
		t.Fatal("medium requeue finding must be ready")
	}
	critical := ReconciliationFinding{Severity: SeverityCritical, Action: ActionCreateRepairTask}
	if !ReadyQ5(critical) {
		t.Fatal("critical repair finding must be ready")
	}
	low := ReconciliationFinding{Severity: SeverityLow, Action: ActionRequeue}
	if ReadyQ5(low) {
		t.Fatal("low severity must not be ready")
	}
	notify := ReconciliationFinding{Severity: SeverityHigh, Action: ActionNotifyPlanner}
	if ReadyQ5(notify) {
		t.Fatal("notify_planner must not be ready for Q5")
	}
}

func TestPriorityFormula(t *testing.T) {
	w := PriorityWeights{Risk: 1, Novelty: 2, ExpectedHit: 3, Cost: 4, Age: 5, Cold: 6}
	got := Priority(w, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5)
	want := 0.5 * (1 + 2 + 3 - 4 + 5 + 6)
	if !covTestNearlyEqual(got, want) {
		t.Fatalf("priority = %v, want %v", got, want)
	}
	// Cost is subtracted.
	w2 := PriorityWeights{Cost: 2}
	if !covTestNearlyEqual(Priority(w2, 0, 0, 0, 3, 0, 0), -6) {
		t.Fatal("cost must be subtracted")
	}
}

func TestPrioritySchedulerTieBreakDeterministic(t *testing.T) {
	a := SchedulerTieBreak("item-a", 42)
	b := SchedulerTieBreak("item-a", 42)
	if a != b {
		t.Fatalf("tie-break not deterministic: %s != %s", a, b)
	}
	if a == "" {
		t.Fatal("tie-break hash must not be empty")
	}
	if SchedulerTieBreak("item-b", 42) == a {
		t.Fatal("tie-break must depend on item_id")
	}
	if SchedulerTieBreak("item-a", 43) == a {
		t.Fatal("tie-break must depend on seed")
	}
}

func TestClaimApplyTransition(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	it := WorkItem{
		ItemID:    "it-1",
		QueueKind: QueueCandidateReadyVerify,
		State:     WorkItemReady,
		Attempts:  0,
		CreatedAt: now.Add(-time.Hour),
	}
	got, err := ApplyClaim(it, "session-1", "claim-1", time.Hour, now)
	if err != nil {
		t.Fatalf("ApplyClaim: %v", err)
	}
	if got.State != WorkItemClaimed {
		t.Fatalf("state = %q, want claimed", got.State)
	}
	if got.ClaimedBySession != "session-1" || got.ClaimID != "claim-1" {
		t.Fatalf("claim fields = %q/%q", got.ClaimedBySession, got.ClaimID)
	}
	if !got.LeaseExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("lease_expires_at = %v", got.LeaseExpiresAt)
	}
	if got.UpdatedAt != now {
		t.Fatalf("updated_at = %v, want %v", got.UpdatedAt, now)
	}
	if _, err := ApplyClaim(got, "session-2", "claim-2", time.Hour, now); err == nil {
		t.Fatal("claiming a claimed item must fail")
	}
	if _, err := ApplyClaim(it, "session-1", "", time.Hour, now); err == nil {
		t.Fatal("empty claim_id must fail")
	}
	done := it
	done.State = WorkItemDone
	if _, err := ApplyClaim(done, "session-1", "claim-3", time.Hour, now); err == nil {
		t.Fatal("claiming a done item must fail")
	}
}

func TestClaimRenewLease(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	it := WorkItem{
		ItemID:           "it-1",
		State:            WorkItemClaimed,
		ClaimedBySession: "session-1",
		ClaimID:          "claim-1",
		LeaseExpiresAt:   now.Add(10 * time.Minute),
	}
	got, err := ApplyRenew(it, "claim-1", 30*time.Minute, now)
	if err != nil {
		t.Fatalf("ApplyRenew: %v", err)
	}
	if !got.LeaseExpiresAt.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("renewed lease = %v", got.LeaseExpiresAt)
	}
	if _, err := ApplyRenew(it, "claim-other", 30*time.Minute, now); err == nil {
		t.Fatal("renew with a different claim must fail")
	}
	expired := it
	expired.LeaseExpiresAt = now.Add(-time.Minute)
	if _, err := ApplyRenew(expired, "claim-1", 30*time.Minute, now); err == nil {
		t.Fatal("renew after lease loss must fail (R-6.15)")
	}
	ready := it
	ready.State = WorkItemReady
	if _, err := ApplyRenew(ready, "claim-1", 30*time.Minute, now); err == nil {
		t.Fatal("renew on a ready item must fail")
	}
}

func TestClaimReleaseReturnsReady(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	it := WorkItem{
		ItemID:           "it-1",
		State:            WorkItemClaimed,
		ClaimedBySession: "session-1",
		ClaimID:          "claim-1",
		LeaseExpiresAt:   now.Add(time.Hour),
		Attempts:         2,
	}
	got, err := ApplyRelease(it, "claim-1", now)
	if err != nil {
		t.Fatalf("ApplyRelease: %v", err)
	}
	if got.State != WorkItemReady {
		t.Fatalf("state = %q, want ready", got.State)
	}
	if got.ClaimID != "" || got.ClaimedBySession != "" || !got.LeaseExpiresAt.IsZero() {
		t.Fatalf("claim fields not cleared: %+v", got)
	}
	if got.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (R-6.14)", got.Attempts)
	}
	if _, err := ApplyRelease(it, "claim-other", now); err == nil {
		t.Fatal("release with a different claim must fail")
	}
	ready := it
	ready.State = WorkItemReady
	if _, err := ApplyRelease(ready, "claim-1", now); err == nil {
		t.Fatal("release on a ready item must fail")
	}
}

func TestClaimComplete(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	it := WorkItem{
		ItemID:           "it-1",
		State:            WorkItemClaimed,
		ClaimedBySession: "session-1",
		ClaimID:          "claim-1",
		LeaseExpiresAt:   now.Add(time.Hour),
	}
	got, err := ApplyComplete(it, "claim-1", now)
	if err != nil {
		t.Fatalf("ApplyComplete: %v", err)
	}
	if got.State != WorkItemDone {
		t.Fatalf("state = %q, want done", got.State)
	}
	if got.ClaimID != "" || got.ClaimedBySession != "" || !got.LeaseExpiresAt.IsZero() {
		t.Fatalf("claim fields not cleared: %+v", got)
	}
	if _, err := ApplyComplete(it, "claim-other", now); err == nil {
		t.Fatal("complete with a different claim must fail")
	}
	ready := it
	ready.State = WorkItemReady
	if _, err := ApplyComplete(ready, "claim-1", now); err == nil {
		t.Fatal("complete on a ready item must fail")
	}
}

func TestClaimExpireAttempts(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	base := WorkItem{
		ItemID:           "it-1",
		State:            WorkItemClaimed,
		ClaimedBySession: "session-1",
		ClaimID:          "claim-1",
		LeaseExpiresAt:   now.Add(-time.Second),
		Attempts:         0,
	}
	got, expired := ApplyExpire(base, now)
	if !expired {
		t.Fatal("expired lease must be reported")
	}
	if got.State != WorkItemReady {
		t.Fatalf("state = %q, want ready", got.State)
	}
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", got.Attempts)
	}
	if got.ClaimID != "" || got.ClaimedBySession != "" || !got.LeaseExpiresAt.IsZero() {
		t.Fatalf("claim fields not cleared: %+v", got)
	}

	maxed := base
	maxed.Attempts = 2
	got, expired = ApplyExpire(maxed, now)
	if !expired {
		t.Fatal("expired lease must be reported")
	}
	if got.State != WorkItemBlocked {
		t.Fatalf("state = %q, want blocked after max attempts", got.State)
	}
	if got.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", got.Attempts)
	}

	future := base
	future.LeaseExpiresAt = now.Add(time.Hour)
	if _, expired := ApplyExpire(future, now); expired {
		t.Fatal("unexpired lease must not expire")
	}
	ready := base
	ready.State = WorkItemReady
	if _, expired := ApplyExpire(ready, now); expired {
		t.Fatal("ready item must not be expired")
	}
	zeroLease := base
	zeroLease.LeaseExpiresAt = time.Time{}
	if _, expired := ApplyExpire(zeroLease, now); !expired {
		t.Fatal("zero lease must be treated as expired")
	}
}

func TestLeaseTTLDefaults(t *testing.T) {
	cases := []struct {
		q    QueueKind
		want time.Duration
	}{
		{QueueUnitReadyDiscover, 30 * time.Minute},
		{QueueCandidateReadyVerify, 60 * time.Minute},
		{QueueClaimReadyReview, 60 * time.Minute},
		{QueueConfirmedReadyReport, 15 * time.Minute},
		{QueueRepairTask, 30 * time.Minute},
	}
	for _, tc := range cases {
		if got := LeaseTTL(nil, tc.q); got != tc.want {
			t.Fatalf("LeaseTTL(nil, %s) = %v, want %v", tc.q, got, tc.want)
		}
	}
	profile := &TaskProfile{LeaseTTL: map[QueueKind]time.Duration{
		QueueCandidateReadyVerify: 5 * time.Minute,
		QueueUnitReadyDiscover:    0, // zero falls back to default
	}}
	if got := LeaseTTL(profile, QueueCandidateReadyVerify); got != 5*time.Minute {
		t.Fatalf("profile override = %v, want 5m", got)
	}
	if got := LeaseTTL(profile, QueueUnitReadyDiscover); got != 30*time.Minute {
		t.Fatalf("zero override must fall back to default, got %v", got)
	}
	if got := LeaseTTL(nil, QueueKind("Q9")); got != 0 {
		t.Fatalf("unknown queue TTL = %v, want 0", got)
	}
}

func TestClaimFilterWhitelist(t *testing.T) {
	if err := ValidateClaimFilter(QueueUnitReadyDiscover, nil); err != nil {
		t.Fatalf("empty filter must be valid: %v", err)
	}
	allowed := map[QueueKind]map[string]string{
		QueueUnitReadyDiscover:    {"area_id": "a", "family_id": "f"},
		QueueCandidateReadyVerify: {"mode": "probe", "candidate_id": "c"},
		QueueClaimReadyReview:     {"assessment_id": "a"},
		QueueConfirmedReadyReport: {"finding_id": "f"},
		QueueRepairTask:           {"check_id": "C1", "severity": "high"},
	}
	for q, filter := range allowed {
		if err := ValidateClaimFilter(q, filter); err != nil {
			t.Fatalf("%s allowed filter rejected: %v", q, err)
		}
	}
	if err := ValidateClaimFilter(QueueUnitReadyDiscover, map[string]string{"mode": "probe"}); err == nil {
		t.Fatal("Q1 must reject the mode filter key")
	}
	if err := ValidateClaimFilter(QueueCandidateReadyVerify, map[string]string{"family_id": "f"}); err == nil {
		t.Fatal("Q2 must reject family_id")
	}
	if err := ValidateClaimFilter(QueueClaimReadyReview, map[string]string{"candidate_id": "c"}); err == nil {
		t.Fatal("Q3 must reject candidate_id")
	}
	if err := ValidateClaimFilter(QueueRepairTask, map[string]string{"action": "requeue"}); err == nil {
		t.Fatal("Q5 must reject unknown keys")
	}
	if err := ValidateClaimFilter(QueueKind("Q9"), nil); err == nil {
		t.Fatal("unknown queue kind must be rejected")
	}
}
