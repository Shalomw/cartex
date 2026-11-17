package lifetime

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultTaskProfilePassesValidation(t *testing.T) {
	p := DefaultTaskProfile()
	if p == nil {
		t.Fatalf("DefaultTaskProfile returned nil")
	}
	if err := ValidateTaskProfile(p); err != nil {
		t.Fatalf("DefaultTaskProfile should pass validation, got: %v", err)
	}
	if p.Name == "" || p.Version == "" {
		t.Fatalf("default profile must carry name/version")
	}
	if len(p.Forms) != 8 {
		t.Fatalf("default forms = %d, want F1–F8 (8)", len(p.Forms))
	}
	if len(p.QueueKinds) != 5 {
		t.Fatalf("default queue kinds = %d, want Q1–Q5 (5)", len(p.QueueKinds))
	}
	if p.StatisticalDefaults != StatisticalFloor() {
		t.Fatalf("default statistical defaults must equal §14.6 floor: got %+v want %+v", p.StatisticalDefaults, StatisticalFloor())
	}
	if p.DispatchMode != DispatchHybrid {
		t.Fatalf("default dispatch_mode = %q, want hybrid (R-6.23)", string(p.DispatchMode))
	}
	if p.OwnerMode != OwnerSummary {
		t.Fatalf("default owner_mode = %q, want summary", string(p.OwnerMode))
	}
	if p.EnumerationScope.Reachability != "public_api" {
		t.Fatalf("default reachability = %q, want public_api", p.EnumerationScope.Reachability)
	}
	if p.SchedulerSeed != 0 {
		t.Fatalf("default scheduler_seed = %d, want 0", p.SchedulerSeed)
	}
	if len(p.LimitedControlForms) != 0 {
		t.Fatalf("limited_control_forms must default empty (R-3.7)")
	}
	if p.PriorityWeights != (PriorityWeights{Risk: 1, Novelty: 1, ExpectedHit: 1, Cost: 1, Age: 1, Cold: 1}) {
		t.Fatalf("default priority weights = %+v, want all 1.0", p.PriorityWeights)
	}
	wantLease := map[QueueKind]time.Duration{
		QueueUnitReadyDiscover:    30 * time.Minute,
		QueueCandidateReadyVerify: 60 * time.Minute,
		QueueClaimReadyReview:     60 * time.Minute,
		QueueConfirmedReadyReport: 15 * time.Minute,
		QueueRepairTask:           30 * time.Minute,
	}
	for q, want := range wantLease {
		if got := p.LeaseTTL[q]; got != want {
			t.Fatalf("default lease_ttl[%s] = %v, want %v (§14.6)", string(q), got, want)
		}
	}
	if p.ReconciliationCadence.Incremental != 15*time.Minute || !p.ReconciliationCadence.FullDaily {
		t.Fatalf("default reconciliation cadence = %+v, want 15m incremental + daily full", p.ReconciliationCadence)
	}
	wantRoles := []Role{
		RoleLifetimeAuditor, RoleLifetimeVerifier, RoleLifetimeReviewer,
		RoleReporter, RoleOwner, RoleReconciler,
	}
	for _, r := range wantRoles {
		c, ok := p.PerRoleCapacity[r]
		if !ok || c < 1 || c > 4 {
			t.Fatalf("default per_role_capacity[%s] = %d (present=%v), want 1–4", string(r), c, ok)
		}
	}
}

func TestProfileRejectsMinRunsBelowFloor(t *testing.T) {
	p := DefaultTaskProfile()
	p.StatisticalDefaults.MinRuns = 10
	err := ValidateTaskProfile(p)
	if err == nil {
		t.Fatalf("profile with min_runs=10 must be rejected (R-5.15)")
	}
	if !strings.Contains(err.Error(), "min_runs") {
		t.Fatalf("error should name min_runs, got: %v", err)
	}
}

func TestProfileRejectsLimitedControlFormOutsideForms(t *testing.T) {
	p := DefaultTaskProfile()
	p.Forms = []Form{FormRefcountRace, FormDanglingThis}
	p.LimitedControlForms = []Form{FormPoolABA}
	err := ValidateTaskProfile(p)
	if err == nil {
		t.Fatalf("limited_control_forms not contained in forms must be rejected (R-3.7)")
	}
	if !strings.Contains(err.Error(), "limited_control_forms") {
		t.Fatalf("error should name limited_control_forms, got: %v", err)
	}
}

func TestProfileRejectsInvalidEnumAndLeaseFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*TaskProfile)
		want   string
	}{
		{"nil", nil, "nil"},
		{"empty name", func(p *TaskProfile) { p.Name = "" }, "name"},
		{"empty forms", func(p *TaskProfile) { p.Forms = nil }, "forms"},
		{"invalid form", func(p *TaskProfile) { p.Forms = append(p.Forms, Form("F9")) }, "forms"},
		{"empty queue kinds", func(p *TaskProfile) { p.QueueKinds = nil }, "queue_kinds"},
		{"invalid queue kind", func(p *TaskProfile) { p.QueueKinds = append(p.QueueKinds, QueueKind("Q9")) }, "queue_kinds"},
		{"zero lease", func(p *TaskProfile) { p.LeaseTTL[QueueUnitReadyDiscover] = 0 }, "lease_ttl"},
		{"missing lease", func(p *TaskProfile) { delete(p.LeaseTTL, QueueRepairTask) }, "lease_ttl"},
		{"invalid role", func(p *TaskProfile) { p.PerRoleCapacity[Role("nobody")] = 1 }, "per_role_capacity"},
		{"invalid dispatch", func(p *TaskProfile) { p.DispatchMode = DispatchMode("manual") }, "dispatch_mode"},
		{"invalid owner", func(p *TaskProfile) { p.OwnerMode = OwnerMode("boss") }, "owner_mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.mutate == nil {
				if err := ValidateTaskProfile(nil); err == nil {
					t.Fatalf("nil profile must be rejected")
				}
				return
			}
			p := DefaultTaskProfile()
			tc.mutate(p)
			err := ValidateTaskProfile(p)
			if err == nil {
				t.Fatalf("profile should be rejected")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestApplyTaskProfileDefaultsDoesNotOverrideExplicitValues(t *testing.T) {
	p := &TaskProfile{
		Name:    "custom",
		Version: "2.0",
		Forms:   []Form{FormRefcountRace},
		StatisticalDefaults: StatisticalDefaults{
			MinRuns: 500,
			Alpha:   0.01,
		},
		DispatchMode:  DispatchPlanner,
		OwnerMode:     OwnerPersistent,
		SchedulerSeed: 42,
		LeaseTTL: map[QueueKind]time.Duration{
			QueueUnitReadyDiscover: 5 * time.Minute,
		},
		PriorityWeights:  PriorityWeights{Risk: 2.5},
		PerRoleCapacity:  map[Role]int{RoleReporter: 3},
		EnumerationScope: EnumerationScope{Reachability: "all"},
		ReconciliationCadence: ReconciliationCadence{
			Incremental: time.Hour,
		},
	}
	ApplyTaskProfileDefaults(p)

	if p.Name != "custom" || p.Version != "2.0" {
		t.Fatalf("name/version must not be overwritten: %q %q", p.Name, p.Version)
	}
	if len(p.Forms) != 1 || p.Forms[0] != FormRefcountRace {
		t.Fatalf("explicit forms overwritten: %v", p.Forms)
	}
	if p.StatisticalDefaults.MinRuns != 500 {
		t.Fatalf("explicit min_runs overwritten: %d", p.StatisticalDefaults.MinRuns)
	}
	if p.StatisticalDefaults.Alpha != 0.01 {
		t.Fatalf("explicit alpha overwritten: %v", p.StatisticalDefaults.Alpha)
	}
	if p.StatisticalDefaults.Beta != profileDefaultStats().Beta {
		t.Fatalf("zero beta should be defaulted, got %v", p.StatisticalDefaults.Beta)
	}
	if p.StatisticalDefaults.MinControlRuns != 300 {
		t.Fatalf("zero min_control_runs should be defaulted, got %d", p.StatisticalDefaults.MinControlRuns)
	}
	if p.DispatchMode != DispatchPlanner || p.OwnerMode != OwnerPersistent {
		t.Fatalf("explicit dispatch/owner overwritten: %q %q", string(p.DispatchMode), string(p.OwnerMode))
	}
	if p.SchedulerSeed != 42 {
		t.Fatalf("explicit scheduler_seed overwritten: %d", p.SchedulerSeed)
	}
	if got := p.LeaseTTL[QueueUnitReadyDiscover]; got != 5*time.Minute {
		t.Fatalf("explicit lease_ttl[Q1] overwritten: %v", got)
	}
	if got := p.LeaseTTL[QueueCandidateReadyVerify]; got != 60*time.Minute {
		t.Fatalf("missing lease_ttl[Q2] should be defaulted to 60m, got %v", got)
	}
	if p.PriorityWeights.Risk != 2.5 {
		t.Fatalf("explicit priority risk overwritten: %v", p.PriorityWeights.Risk)
	}
	if p.PriorityWeights.Novelty != 1.0 {
		t.Fatalf("zero priority weight should be defaulted to 1.0, got %v", p.PriorityWeights.Novelty)
	}
	if p.PerRoleCapacity[RoleReporter] != 3 {
		t.Fatalf("explicit reporter capacity overwritten: %d", p.PerRoleCapacity[RoleReporter])
	}
	if p.PerRoleCapacity[RoleLifetimeAuditor] != profileDefaultRoleCapacity()[RoleLifetimeAuditor] {
		t.Fatalf("missing auditor capacity should be defaulted, got %d", p.PerRoleCapacity[RoleLifetimeAuditor])
	}
	if p.EnumerationScope.Reachability != "all" {
		t.Fatalf("explicit reachability overwritten: %q", p.EnumerationScope.Reachability)
	}
	if p.ReconciliationCadence.Incremental != time.Hour {
		t.Fatalf("explicit incremental cadence overwritten: %v", p.ReconciliationCadence.Incremental)
	}
	if !p.ReconciliationCadence.FullDaily {
		t.Fatalf("zero full_daily should be defaulted to true (§14.6)")
	}
	if len(p.QueueKinds) != 5 {
		t.Fatalf("zero queue kinds should be defaulted to Q1–Q5, got %v", p.QueueKinds)
	}
}

func TestValidateConfirmationLineageOnlyDriverMayDiffer(t *testing.T) {
	claim := LineageInput{
		SourceManifestHash:         "sm",
		IndexHash:                  "idx",
		BuildID:                    "build",
		DriverSpecHash:             "driver-claim",
		PolicyHash:                 "policy",
		ProfileVersion:             "1.0",
		HarnessVersion:             "harness",
		InstrumentationProfileHash: "instr",
	}
	confirmation := claim
	confirmation.DriverSpecHash = "driver-confirmation"
	if err := ValidateConfirmationLineage(claim, confirmation); err != nil {
		t.Fatalf("confirmation differing only in driver_spec_hash should pass (R-10.9): %v", err)
	}
	if err := ValidateConfirmationLineage(claim, claim); err != nil {
		t.Fatalf("identical lineage inputs should pass: %v", err)
	}

	mutations := []struct {
		field  string
		mutate func(*LineageInput)
	}{
		{"source_manifest_hash", func(in *LineageInput) { in.SourceManifestHash = "x" }},
		{"index_hash", func(in *LineageInput) { in.IndexHash = "x" }},
		{"build_id", func(in *LineageInput) { in.BuildID = "x" }},
		{"policy_hash", func(in *LineageInput) { in.PolicyHash = "x" }},
		{"profile_version", func(in *LineageInput) { in.ProfileVersion = "x" }},
		{"harness_version", func(in *LineageInput) { in.HarnessVersion = "x" }},
		{"instrumentation_profile_hash", func(in *LineageInput) { in.InstrumentationProfileHash = "x" }},
	}
	for _, m := range mutations {
		other := claim
		m.mutate(&other)
		err := ValidateConfirmationLineage(claim, other)
		if err == nil {
			t.Fatalf("confirmation differing in %s must be rejected (R-10.9)", m.field)
		}
		if !strings.Contains(err.Error(), m.field) {
			t.Fatalf("error %q should name field %s", err.Error(), m.field)
		}
	}
}

func TestProfileComputePolicyHashAppliesDefaults(t *testing.T) {
	p := &TaskProfile{Name: "minimal", Version: "1.0"}
	h := ComputePolicyHash(p)
	if h == "" || len(h) != 64 {
		t.Fatalf("ComputePolicyHash returned %q", h)
	}
	if p.PolicyHash != h {
		t.Fatalf("ComputePolicyHash should record the hash on the profile: field=%q returned=%q", p.PolicyHash, h)
	}
	if len(p.Forms) != 8 || len(p.QueueKinds) != 5 {
		t.Fatalf("ComputePolicyHash should apply defaults before hashing")
	}
	if err := ValidateTaskProfile(p); err != nil {
		t.Fatalf("defaulted profile should validate, got: %v", err)
	}
	if ComputePolicyHash(nil) != "" {
		t.Fatalf("ComputePolicyHash(nil) should be empty")
	}
}

func TestProfileEvidenceEligibilityMayOnlyTighten(t *testing.T) {
	p := DefaultTaskProfile()
	// MSan 对 UAF/double-free 不合格（R-12.16），F1 的 eligible 表不含 msan。
	p.EvidenceEligibility.Extra = map[Form][]EligibleEvent{
		FormRefcountRace: {{EventType: EventMemory, Tool: ToolMSan}},
	}
	err := ValidateTaskProfile(p)
	if err == nil {
		t.Fatalf("evidence_eligibility must not widen base eligible set (R-5.14)")
	}
	if !strings.Contains(err.Error(), "evidence_eligibility") {
		t.Fatalf("error should name evidence_eligibility, got: %v", err)
	}

	// 基础谓词允许的组合（F1 invariant+assertion）可以出现。
	q := DefaultTaskProfile()
	q.EvidenceEligibility.Extra = map[Form][]EligibleEvent{
		FormRefcountRace: {{EventType: EventInvariant, Tool: ToolAssertion}},
	}
	if err := ValidateTaskProfile(q); err != nil {
		t.Fatalf("eligible combination should be accepted, got: %v", err)
	}
}
