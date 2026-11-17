package lifetime

import (
	"slices"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// test fixtures
// ---------------------------------------------------------------------------

func ltTestCandidate(form Form) *Candidate {
	return &Candidate{
		CandidateID: "cand-1",
		UnitID:      "unit-1",
		CandidatePackage: CandidatePackage{
			Family: ObjectFamily{
				FamilyID:      "fam-1",
				TypeCanonical: "Obj",
				AllocSite:     "src/obj.cpp:5",
				StorageClass:  StorageHeap,
				LifetimeForm:  LifetimeRaw,
			},
			DomainPairID:     "dp-1",
			PairBasis:        PairBasisLogical,
			DomainConfidence: DomainConfidenceHigh,
			PrimaryForm:      form,
			ContractStatus:   ContractKnown,
		},
	}
}

// ltTestStatic 返回一条从 lifetime_point 到 access 且引用已校验的静态链。
func ltTestStatic() *StaticSupport {
	return &StaticSupport{
		SupportID:   "sup-1",
		CandidateID: "cand-1",
		Chain: []EvidenceNode{
			{FactID: "fact-lp", Role: NodeLifetimePoint, File: "src/obj.cpp", Line: 10, Symbol: "~Obj"},
			{FactID: "fact-acc", Role: NodeAccess, File: "src/obj.cpp", Line: 20, Symbol: "Obj::use"},
		},
		CitationChecks: []CitationCheck{
			{Status: CitationVerified, File: "src/obj.cpp", Line: 10, Role: NodeLifetimePoint, Symbol: "~Obj"},
			{Status: CitationVerified, File: "src/obj.cpp", Line: 20, Role: NodeAccess, Symbol: "Obj::use"},
		},
		IndexHash: "idx-1",
	}
}

func ltTestIndex(quality, resolution string) *IndexOutput {
	return &IndexOutput{IndexHash: "idx-1", IndexQuality: quality, TypeResolution: resolution}
}

// ltTestCompleteF1 返回 form facts 与四问都完整的 F1 候选。
func ltTestCompleteF1() *Candidate {
	c := ltTestCandidate(FormRefcountRace)
	c.Holders = []Holder{
		{Site: "src/obj.cpp:30", Form: "member", Domain: "d1"},
	}
	c.Synchronization = []SyncPrimitive{
		{Primitive: "refcount dec relaxed", Scope: "Obj", Protects: "refcount", FileLine: "src/obj.cpp:31"},
	}
	c.OwnershipAnswers = OwnershipAnswers{
		Q1CreatedBy:   OwnerAnswer{Value: "Obj::create()", File: "src/obj.cpp", Line: 5},
		Q2HeldBy:      OwnerAnswer{Value: "Owner::ptr", Form: "member", Domain: "d1", File: "src/obj.cpp", Line: 30},
		Q3DestroyedBy: OwnerAnswer{Value: "Obj::~Obj()", Domain: "d1", File: "src/obj.cpp", Line: 10},
		Q4:            VisibilityAnswer{Value: "refcount", Status: ltQ4StatusKnown},
	}
	return c
}

// ltTestCompleteF2 返回 form facts 完整的 F2 候选（四问另行设置）。
func ltTestCompleteF2() *Candidate {
	c := ltTestCandidate(FormDanglingThis)
	c.Holders = []Holder{
		{Site: "src/task.cpp:12", Form: "raw_this", Domain: "d1"},
	}
	c.Domains = []Domain{
		{DomainClassID: "d1", Kind: DomainQueue, InstanceSite: "src/task.cpp:12",
			Serialization: SerializationSerial, ConcurrencyDegree: ConcurrencyOne},
	}
	c.LifetimeVisibility.HolderNotification = "callback captures raw this"
	c.EvidenceChain = []EvidenceNode{
		{FactID: "lp", Role: NodeLifetimePoint, File: "src/task.cpp", Line: 40},
		{FactID: "acc", Role: NodeAccess, File: "src/task.cpp", Line: 41},
	}
	c.Gap = Gap{Description: "destructor runs while queued callback is pending", Window: "submit..run"}
	c.OwnershipAnswers = OwnershipAnswers{
		Q1CreatedBy:   OwnerAnswer{Value: "Task::start()", File: "src/task.cpp", Line: 10},
		Q2HeldBy:      OwnerAnswer{Value: "queue item", Form: "raw_this", Domain: "d1", File: "src/task.cpp", Line: 12},
		Q3DestroyedBy: OwnerAnswer{Value: "~Task()", Domain: "d1", File: "src/task.cpp", Line: 40},
		Q4:            VisibilityAnswer{Value: "none", Status: ltQ4StatusKnown},
	}
	return c
}

func ltTestExp() Experiment {
	return Experiment{
		ExperimentID:  "exp-1",
		CandidateID:   "cand-1",
		Kind:          ExperimentKindCandidate,
		State:         ExperimentCompleted,
		ValidRuns:     5,
		ContractCheck: ContractCheckPassed,
		ControlResult: ControlResultNone,
		ObservationIDs: []string{
			"obs-1",
		},
	}
}

func ltTestObs() Observation {
	return Observation{
		ObservationID:   "obs-1",
		ExperimentID:    "exp-1",
		CandidateID:     "cand-1",
		EventType:       EventMemory,
		Tool:            ToolASan,
		Platform:        PlatformNative,
		Fidelity:        FidelityUnit,
		PlatformQuality: PlatformQualityHigh,
		Binding:         BindingMatched,
		BindingQuality:  BindingQualityExact,
		PairBasis:       PairBasisLogical,
		IsControl:       false,
	}
}

func ltTestPolicy() PlatformPolicy {
	return PlatformPolicy{
		S3Allowed:  []Platform{PlatformNative, PlatformEmulated},
		MinQuality: PlatformQualityMedium,
	}
}

// ---------------------------------------------------------------------------
// §12.6 / §12.4 / §4.4 / §12.2 / §12.3 form tests
// ---------------------------------------------------------------------------

func TestFormMinFidelity(t *testing.T) {
	want := map[Form]Fidelity{
		FormRefcountRace:     FidelityUnit,
		FormDanglingThis:     FidelityComponent,
		FormRegisterLeak:     FidelityUnit,
		FormContainerInvalid: FidelityComponent,
		FormStaticDestroy:    FidelityComponent,
		FormSharedPtrRace:    FidelityUnit,
		FormLazyInitRace:     FidelityUnit,
		FormPoolABA:          FidelityUnit,
	}
	for form, fid := range want {
		if got := FormMinFidelity(form); got != fid {
			t.Errorf("FormMinFidelity(%s)=%s, want %s", form, got, fid)
		}
	}
	if got := FormMinFidelity(Form("F9")); got != Fidelity("") {
		t.Errorf("FormMinFidelity(invalid)=%q, want empty", got)
	}
}

func TestEligibleForms(t *testing.T) {
	tests := []struct {
		name  string
		form  Form
		event EventType
		tool  EvidenceTool
		want  bool
	}{
		{"F1 memory asan", FormRefcountRace, EventMemory, ToolASan, true},
		{"F1 memory valgrind", FormRefcountRace, EventMemory, ToolValgrind, true},
		{"F1 invariant assertion", FormRefcountRace, EventInvariant, ToolAssertion, true},
		{"F1 memory msan rejected", FormRefcountRace, EventMemory, ToolMSan, false},
		{"F2 memory asan", FormDanglingThis, EventMemory, ToolASan, true},
		{"F3 invariant", FormRegisterLeak, EventInvariant, ToolAssertion, true},
		{"F4 memory valgrind", FormContainerInvalid, EventMemory, ToolValgrind, true},
		{"F5 hang watchdog", FormStaticDestroy, EventHang, ToolWatchdog, true},
		{"F5 memory asan", FormStaticDestroy, EventMemory, ToolASan, true},
		{"F6 invariant", FormSharedPtrRace, EventInvariant, ToolAssertion, true},
		{"F7 invariant", FormLazyInitRace, EventInvariant, ToolAssertion, true},
		{"F7 race_lead tsan (S2 only)", FormLazyInitRace, EventRaceLead, ToolTSan, true},
		{"F7 memory rejected", FormLazyInitRace, EventMemory, ToolASan, false},
		{"F8 invariant", FormPoolABA, EventInvariant, ToolAssertion, true},
		{"F8 memory rejected by default", FormPoolABA, EventMemory, ToolASan, false},
		{"F1 hang rejected", FormRefcountRace, EventHang, ToolWatchdog, false},
		{"F5 race_lead rejected", FormStaticDestroy, EventRaceLead, ToolTSan, false},
		{"invalid form", Form("F9"), EventInvariant, ToolAssertion, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Eligible(tt.form, tt.event, tt.tool); got != tt.want {
				t.Errorf("Eligible(%s,%s,%s)=%v, want %v",
					tt.form, tt.event, tt.tool, got, tt.want)
			}
		})
	}
}

func TestFormVisibilityRequired(t *testing.T) {
	for _, f := range []Form{FormDanglingThis, FormRegisterLeak, FormStaticDestroy} {
		if !FormVisibilityRequired(f) {
			t.Errorf("FormVisibilityRequired(%s)=false, want true", f)
		}
	}
	for _, f := range []Form{FormRefcountRace, FormContainerInvalid, FormSharedPtrRace,
		FormLazyInitRace, FormPoolABA} {
		if FormVisibilityRequired(f) {
			t.Errorf("FormVisibilityRequired(%s)=true, want false", f)
		}
	}
}

func TestFormSuggestedApplicable(t *testing.T) {
	tests := []struct {
		name     string
		fam      ObjectFamily
		want     []Form
		wantConf ConfidenceLevel
	}{
		{
			name:     "static -> F5,F7",
			fam:      ObjectFamily{StorageClass: StorageStatic},
			want:     []Form{FormStaticDestroy, FormLazyInitRace},
			wantConf: ConfidenceMedium,
		},
		{
			name:     "singleton -> F5,F7",
			fam:      ObjectFamily{LifetimeForm: LifetimeSingleton},
			want:     []Form{FormStaticDestroy, FormLazyInitRace},
			wantConf: ConfidenceMedium,
		},
		{
			name:     "shared -> F6",
			fam:      ObjectFamily{LifetimeForm: LifetimeShared},
			want:     []Form{FormSharedPtrRace},
			wantConf: ConfidenceMedium,
		},
		{
			name:     "weak -> F6",
			fam:      ObjectFamily{LifetimeForm: LifetimeWeak},
			want:     []Form{FormSharedPtrRace},
			wantConf: ConfidenceMedium,
		},
		{
			name:     "refcount -> F1",
			fam:      ObjectFamily{LifetimeForm: LifetimeRefcount},
			want:     []Form{FormRefcountRace},
			wantConf: ConfidenceMedium,
		},
		{
			name:     "intrusive -> F1",
			fam:      ObjectFamily{LifetimeForm: LifetimeIntrusive},
			want:     []Form{FormRefcountRace},
			wantConf: ConfidenceMedium,
		},
		{
			name:     "pool -> F8",
			fam:      ObjectFamily{StorageClass: StoragePool},
			want:     []Form{FormPoolABA},
			wantConf: ConfidenceMedium,
		},
		{
			name:     "handle -> F8",
			fam:      ObjectFamily{StorageClass: StorageHandle},
			want:     []Form{FormPoolABA},
			wantConf: ConfidenceMedium,
		},
		{
			name:     "no rule -> empty unknown",
			fam:      ObjectFamily{StorageClass: StorageHeap, LifetimeForm: LifetimeRaw},
			want:     nil,
			wantConf: ConfidenceUnknown,
		},
		{
			name:     "explicit list keeps confidence",
			fam:      ObjectFamily{ApplicableForms: []Form{FormContainerInvalid}, Confidence: ConfidenceHigh},
			want:     []Form{FormContainerInvalid},
			wantConf: ConfidenceHigh,
		},
		{
			name:     "static+shared combines",
			fam:      ObjectFamily{StorageClass: StorageStatic, LifetimeForm: LifetimeShared},
			want:     []Form{FormStaticDestroy, FormSharedPtrRace, FormLazyInitRace},
			wantConf: ConfidenceMedium,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, conf := SuggestedApplicableForms(tt.fam)
			if !slices.Equal(got, tt.want) {
				t.Errorf("forms=%v, want %v", got, tt.want)
			}
			if conf != tt.wantConf {
				t.Errorf("confidence=%s, want %s", conf, tt.wantConf)
			}
		})
	}
}

func TestFormFactsComplete(t *testing.T) {
	t.Run("F1 complete", func(t *testing.T) {
		ok, missing := FormFactsComplete(&ltTestCompleteF1().CandidatePackage)
		if !ok || len(missing) != 0 {
			t.Fatalf("ok=%v missing=%v, want complete", ok, missing)
		}
	})
	t.Run("F1 empty package reports missing", func(t *testing.T) {
		ok, missing := FormFactsComplete(&ltTestCandidate(FormRefcountRace).CandidatePackage)
		if ok || len(missing) == 0 {
			t.Fatalf("ok=%v missing=%v, want incomplete", ok, missing)
		}
		if !slices.Contains(missing, "holders") || !slices.Contains(missing, "synchronization") {
			t.Errorf("missing=%v, want holders and synchronization", missing)
		}
	})
	t.Run("F2 complete", func(t *testing.T) {
		ok, missing := FormFactsComplete(&ltTestCompleteF2().CandidatePackage)
		if !ok || len(missing) != 0 {
			t.Fatalf("ok=%v missing=%v, want complete", ok, missing)
		}
	})
	t.Run("F8 needs storage, chain and generation", func(t *testing.T) {
		c := ltTestCandidate(FormPoolABA)
		c.Family.StorageClass = StoragePool
		ok, missing := FormFactsComplete(&c.CandidatePackage)
		if ok {
			t.Fatalf("ok=true, want incomplete")
		}
		if !slices.Contains(missing, "generation") {
			t.Errorf("missing=%v, want generation", missing)
		}

		c.EvidenceChain = []EvidenceNode{
			{FactID: "lp", Role: NodeLifetimePoint, File: "src/pool.cpp", Line: 10},
			{FactID: "acc", Role: NodeAccess, File: "src/pool.cpp", Line: 20},
		}
		c.OwnershipAnswers.Q4 = VisibilityAnswer{Value: "generation", Status: ltQ4StatusKnown}
		ok, missing = FormFactsComplete(&c.CandidatePackage)
		if !ok || len(missing) != 0 {
			t.Fatalf("ok=%v missing=%v, want complete", ok, missing)
		}
	})
	t.Run("invalid form", func(t *testing.T) {
		c := ltTestCandidate(Form("F9"))
		ok, missing := FormFactsComplete(&c.CandidatePackage)
		if ok || !slices.Contains(missing, "primary_form") {
			t.Fatalf("ok=%v missing=%v", ok, missing)
		}
	})
}

func TestFourQuestions(t *testing.T) {
	t.Run("complete known", func(t *testing.T) {
		if err := ValidateFourQuestions(&ltTestCompleteF1().CandidatePackage); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("missing q1 file", func(t *testing.T) {
		c := ltTestCompleteF1()
		c.OwnershipAnswers.Q1CreatedBy.File = ""
		if err := ValidateFourQuestions(&c.CandidatePackage); err == nil {
			t.Fatal("want error for missing q1 file")
		}
	})
	t.Run("missing q2 form", func(t *testing.T) {
		c := ltTestCompleteF1()
		c.OwnershipAnswers.Q2HeldBy.Form = ""
		if err := ValidateFourQuestions(&c.CandidatePackage); err == nil {
			t.Fatal("want error for missing q2 form")
		}
	})
	t.Run("invalid q4 status", func(t *testing.T) {
		c := ltTestCompleteF1()
		c.OwnershipAnswers.Q4.Status = "maybe"
		if err := ValidateFourQuestions(&c.CandidatePackage); err == nil {
			t.Fatal("want error for invalid q4 status")
		}
	})
	t.Run("required form q4 unknown rejected", func(t *testing.T) {
		c := ltTestCompleteF2()
		c.OwnershipAnswers.Q4 = VisibilityAnswer{
			Value: "unknown", Status: ltQ4StatusUnknown, Reason: ltQ4ReasonNotDocumented,
		}
		c.LifetimeVisibility.VisibilityUnknown = true
		if err := ValidateFourQuestions(&c.CandidatePackage); err == nil {
			t.Fatal("want R-12.9 error for required visibility form")
		}
	})
	t.Run("optional form q4 unknown needs visibility_unknown", func(t *testing.T) {
		c := ltTestCompleteF1()
		c.OwnershipAnswers.Q4 = VisibilityAnswer{
			Value: "unknown", Status: ltQ4StatusUnknown, Reason: ltQ4ReasonNotDocumented,
		}
		if err := ValidateFourQuestions(&c.CandidatePackage); err == nil {
			t.Fatal("want R-12.10 error without visibility_unknown")
		}
		c.LifetimeVisibility.VisibilityUnknown = true
		if err := ValidateFourQuestions(&c.CandidatePackage); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("optional form q4 unknown invalid reason", func(t *testing.T) {
		c := ltTestCompleteF1()
		c.OwnershipAnswers.Q4 = VisibilityAnswer{
			Value: "unknown", Status: ltQ4StatusUnknown, Reason: "because",
		}
		c.LifetimeVisibility.VisibilityUnknown = true
		if err := ValidateFourQuestions(&c.CandidatePackage); err == nil {
			t.Fatal("want error for non-structured q4 reason")
		}
	})
}

func TestFormDecisiveContract(t *testing.T) {
	want := map[Form][]string{
		FormRefcountRace:     {"thread_safety", "caller_locking", "ownership"},
		FormDanglingThis:     {"destroy_while_callback", "callback_cancellation", "thread_affinity"},
		FormRegisterLeak:     {"unregister_obligation", "callback_cancellation", "ownership"},
		FormContainerInvalid: {"thread_safety", "caller_locking"},
		FormStaticDestroy:    {"close_semantics", "thread_affinity", "ownership"},
		FormSharedPtrRace:    {"thread_safety"},
		FormLazyInitRace:     {"thread_safety", "ownership"},
		FormPoolABA:          {"close_semantics", "ownership", "generation_guarantee"},
	}
	for form, fields := range want {
		if got := DecisiveContractFields(form); !slices.Equal(got, fields) {
			t.Errorf("DecisiveContractFields(%s)=%v, want %v", form, got, fields)
		}
	}
	if got := DecisiveContractFields(Form("F9")); len(got) != 0 {
		t.Errorf("DecisiveContractFields(invalid)=%v, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// S1 / S2
// ---------------------------------------------------------------------------

func TestS1Predicate(t *testing.T) {
	full := ltTestIndex("full", "exact")

	t.Run("complete chain verified citations full exact", func(t *testing.T) {
		c := ltTestCandidate(FormRefcountRace)
		ok, reasons := EvaluateS1(c, ltTestStatic(), full)
		if !ok {
			t.Fatalf("ok=false reasons=%v", reasons)
		}
	})
	t.Run("citation unresolved", func(t *testing.T) {
		s := ltTestStatic()
		s.CitationChecks[0].Status = CitationUnresolved
		ok, reasons := EvaluateS1(ltTestCandidate(FormRefcountRace), s, full)
		if ok {
			t.Fatal("ok=true, want false")
		}
		if len(reasons) == 0 {
			t.Fatal("want reasons")
		}
	})
	t.Run("missing access", func(t *testing.T) {
		s := ltTestStatic()
		s.Chain = s.Chain[:1] // lifetime_point only
		s.CitationChecks = s.CitationChecks[:1]
		ok, reasons := EvaluateS1(ltTestCandidate(FormRefcountRace), s, full)
		if ok {
			t.Fatal("ok=true, want false")
		}
		if !slices.ContainsFunc(reasons, func(r string) bool {
			return strings.Contains(r, "no access")
		}) {
			t.Errorf("reasons=%v, want missing access", reasons)
		}
	})
	t.Run("heuristic index is provisional only", func(t *testing.T) {
		c := ltTestCandidate(FormRefcountRace)
		heur := ltTestIndex("heuristic", "partial")
		if ok, _ := EvaluateS1(c, ltTestStatic(), heur); ok {
			t.Fatal("S1=true with heuristic index")
		}
		if ok, reasons := EvaluateS1Provisional(c, ltTestStatic(), heur); !ok {
			t.Fatalf("S1_provisional=false reasons=%v", reasons)
		}
	})
	t.Run("full index partial type resolution", func(t *testing.T) {
		ok, _ := EvaluateS1(ltTestCandidate(FormRefcountRace), ltTestStatic(),
			ltTestIndex("full", "partial"))
		if ok {
			t.Fatal("ok=true, want false for type_resolution=partial")
		}
	})
	t.Run("empty chain", func(t *testing.T) {
		s := ltTestStatic()
		s.Chain = nil
		if ok, _ := EvaluateS1(ltTestCandidate(FormRefcountRace), s, full); ok {
			t.Fatal("ok=true, want false for empty chain")
		}
	})
}

func TestS2Predicate(t *testing.T) {
	t.Run("complete form facts and four questions", func(t *testing.T) {
		c := ltTestCompleteF1()
		ok, reasons := EvaluateS2(c, ltTestStatic())
		if !ok {
			t.Fatalf("ok=false reasons=%v", reasons)
		}
	})
	t.Run("q4 unknown on required form", func(t *testing.T) {
		c := ltTestCompleteF2()
		c.OwnershipAnswers.Q4 = VisibilityAnswer{
			Value: "unknown", Status: ltQ4StatusUnknown, Reason: ltQ4ReasonNotDocumented,
		}
		c.LifetimeVisibility.VisibilityUnknown = true
		ok, reasons := EvaluateS2(c, ltTestStatic())
		if ok {
			t.Fatal("ok=true, want false for visibility-required q4 unknown")
		}
		if len(reasons) == 0 {
			t.Fatal("want reasons")
		}
	})
	t.Run("q4 unknown on optional form with visibility_unknown", func(t *testing.T) {
		c := ltTestCompleteF1()
		c.OwnershipAnswers.Q4 = VisibilityAnswer{
			Value: "unknown", Status: ltQ4StatusUnknown, Reason: ltQ4ReasonInsufficientIndex,
		}
		c.LifetimeVisibility.VisibilityUnknown = true
		ok, reasons := EvaluateS2(c, ltTestStatic())
		if !ok {
			t.Fatalf("ok=false reasons=%v", reasons)
		}
	})
	t.Run("contract conflicting", func(t *testing.T) {
		c := ltTestCompleteF1()
		c.ContractStatus = ContractConflicting
		if ok, _ := EvaluateS2(c, ltTestStatic()); ok {
			t.Fatal("ok=true, want false for contract_status=conflicting")
		}
	})
	t.Run("incomplete form facts", func(t *testing.T) {
		c := ltTestCompleteF1()
		c.Synchronization = nil
		if ok, _ := EvaluateS2(c, ltTestStatic()); ok {
			t.Fatal("ok=true, want false for incomplete form facts")
		}
	})
}

// ---------------------------------------------------------------------------
// S3 / S4
// ---------------------------------------------------------------------------

func TestS3Predicate(t *testing.T) {
	t.Run("matched asan uaf with valid completed run", func(t *testing.T) {
		c := ltTestCompleteF1()
		ok, reasons := EvaluateS3(c, []Experiment{ltTestExp()}, []Observation{ltTestObs()}, ltTestPolicy())
		if !ok {
			t.Fatalf("ok=false reasons=%v", reasons)
		}
	})
	t.Run("race_lead excluded", func(t *testing.T) {
		c := ltTestCompleteF1()
		o := ltTestObs()
		o.EventType = EventRaceLead
		o.Tool = ToolTSan
		if ok, _ := EvaluateS3(c, []Experiment{ltTestExp()}, []Observation{o}, ltTestPolicy()); ok {
			t.Fatal("ok=true, want false for race_lead")
		}
	})
	t.Run("foreign platform excluded", func(t *testing.T) {
		c := ltTestCompleteF1()
		o := ltTestObs()
		o.Platform = PlatformForeign
		policy := PlatformPolicy{S3Allowed: []Platform{PlatformNative, PlatformForeign},
			MinQuality: PlatformQualityLow}
		if ok, _ := EvaluateS3(c, []Experiment{ltTestExp()}, []Observation{o}, policy); ok {
			t.Fatal("ok=true, want false for foreign platform (R-3.8)")
		}
	})
	t.Run("binding partial excluded", func(t *testing.T) {
		c := ltTestCompleteF1()
		o := ltTestObs()
		o.Binding = BindingPartial
		if ok, _ := EvaluateS3(c, []Experiment{ltTestExp()}, []Observation{o}, ltTestPolicy()); ok {
			t.Fatal("ok=true, want false for binding=partial")
		}
	})
	t.Run("control failed excluded", func(t *testing.T) {
		c := ltTestCompleteF1()
		e := ltTestExp()
		e.ControlResult = ControlResultFailed
		if ok, _ := EvaluateS3(c, []Experiment{e}, []Observation{ltTestObs()}, ltTestPolicy()); ok {
			t.Fatal("ok=true, want false for control_result=failed")
		}
	})
	t.Run("heuristic binding quality accepted at S3", func(t *testing.T) {
		c := ltTestCompleteF1()
		o := ltTestObs()
		o.BindingQuality = BindingQualityHeuristic
		if ok, reasons := EvaluateS3(c, []Experiment{ltTestExp()}, []Observation{o}, ltTestPolicy()); !ok {
			t.Fatalf("ok=false reasons=%v", reasons)
		}
	})
	t.Run("F8 pool without generation excluded", func(t *testing.T) {
		c := ltTestCandidate(FormPoolABA)
		c.Family.StorageClass = StoragePool
		o := ltTestObs()
		o.EventType = EventInvariant
		o.Tool = ToolAssertion
		o.RuntimeContext.Generation = 0
		if ok, _ := EvaluateS3(c, []Experiment{ltTestExp()}, []Observation{o}, ltTestPolicy()); ok {
			t.Fatal("ok=true, want false for pool generation=0 (R-9.23)")
		}

		o.RuntimeContext.Generation = 7
		if ok, reasons := EvaluateS3(c, []Experiment{ltTestExp()}, []Observation{o}, ltTestPolicy()); !ok {
			t.Fatalf("ok=false with generation=7 reasons=%v", reasons)
		}
	})
}

func TestS4Predicate(t *testing.T) {
	baseExp := func() Experiment {
		e := ltTestExp()
		e.ControlResult = ControlResultPassed
		e.ControlKind = ControlKindTarget
		e.ControlPowerAdequate = true
		e.Reproduction = Reproduction{
			Mode:                   ReproDeterministicReplay,
			N:                      20,
			Hits:                   18,
			ReplayArtifactVerified: true,
		}
		return e
	}

	t.Run("deterministic replay 20/18 verified", func(t *testing.T) {
		c := ltTestCompleteF1()
		ok, reasons := EvaluateS4(c, []Experiment{baseExp()}, []Observation{ltTestObs()}, ltTestPolicy())
		if !ok {
			t.Fatalf("ok=false reasons=%v", reasons)
		}
	})
	t.Run("deterministic replay 20/17 insufficient", func(t *testing.T) {
		c := ltTestCompleteF1()
		e := baseExp()
		e.Reproduction.Hits = 17
		if ok, _ := EvaluateS4(c, []Experiment{e}, []Observation{ltTestObs()}, ltTestPolicy()); ok {
			t.Fatal("ok=true, want false for 17/20")
		}
	})
	t.Run("deterministic replay without verified artifact", func(t *testing.T) {
		c := ltTestCompleteF1()
		e := baseExp()
		e.Reproduction.ReplayArtifactVerified = false
		if ok, _ := EvaluateS4(c, []Experiment{e}, []Observation{ltTestObs()}, ltTestPolicy()); ok {
			t.Fatal("ok=true, want false without replay artifact")
		}
	})
	t.Run("stochastic stable 300 at lower bound", func(t *testing.T) {
		c := ltTestCompleteF1()
		e := baseExp()
		e.Reproduction = Reproduction{Mode: ReproStochasticStable, N: 300, CPLowerBound95: 0.02}
		if ok, reasons := EvaluateS4(c, []Experiment{e}, []Observation{ltTestObs()}, ltTestPolicy()); !ok {
			t.Fatalf("ok=false reasons=%v", reasons)
		}
	})
	t.Run("stochastic stable below lower bound", func(t *testing.T) {
		c := ltTestCompleteF1()
		e := baseExp()
		e.Reproduction = Reproduction{Mode: ReproStochasticStable, N: 300, CPLowerBound95: 0.019}
		if ok, _ := EvaluateS4(c, []Experiment{e}, []Observation{ltTestObs()}, ltTestPolicy()); ok {
			t.Fatal("ok=true, want false below min_repro_lower_bound")
		}
	})
	t.Run("s4 requires s3", func(t *testing.T) {
		c := ltTestCompleteF1()
		o := ltTestObs()
		o.Binding = BindingUnmatched
		if ok, _ := EvaluateS4(c, []Experiment{baseExp()}, []Observation{o}, ltTestPolicy()); ok {
			t.Fatal("ok=true, want false without S3")
		}
	})
}

// ---------------------------------------------------------------------------
// assessment / external gates
// ---------------------------------------------------------------------------

func TestAssessmentEvaluation(t *testing.T) {
	confirmed := func() Review {
		return Review{Verdict: VerdictConfirmed, Independence: IndependenceNewDriverReviewer,
			ReviewerSessionID: "reviewer-1"}
	}

	t.Run("S4 confirmed native exact is external high", func(t *testing.T) {
		c := ltTestCompleteF1()
		e := ltTestExp()
		e.ControlResult = ControlResultPassed
		e.ControlKind = ControlKindTarget
		e.ControlPowerAdequate = true
		e.Reproduction = Reproduction{Mode: ReproDeterministicReplay, N: 20, Hits: 18,
			ReplayArtifactVerified: true}
		ev := EvaluateAssessment(c, ltTestStatic(), ltTestIndex("full", "exact"),
			[]Experiment{e}, []Observation{ltTestObs()}, ltTestPolicy(), nil, confirmed())
		if ev.Level != LevelS4 {
			t.Fatalf("level=%s, want S4; reasons=%v", ev.Level, ev.Reasons)
		}
		if !ev.ReportableInternal || !ev.ReportableExternal {
			t.Fatalf("reportable internal=%v external=%v, want both",
				ev.ReportableInternal, ev.ReportableExternal)
		}
		if ev.Confidence != ConfidenceHigh {
			t.Fatalf("confidence=%s, want high", ev.Confidence)
		}
	})
	t.Run("S3 without review is internal only", func(t *testing.T) {
		c := ltTestCompleteF1()
		ev := EvaluateAssessment(c, ltTestStatic(), ltTestIndex("full", "exact"),
			[]Experiment{ltTestExp()}, []Observation{ltTestObs()}, ltTestPolicy(), nil,
			Review{Verdict: VerdictNone})
		if ev.Level != LevelS3 {
			t.Fatalf("level=%s, want S3; reasons=%v", ev.Level, ev.Reasons)
		}
		if !ev.ReportableInternal || ev.ReportableExternal {
			t.Fatalf("reportable internal=%v external=%v, want internal only",
				ev.ReportableInternal, ev.ReportableExternal)
		}
		if ev.Confidence != ConfidenceLow {
			t.Fatalf("confidence=%s, want low", ev.Confidence)
		}
	})
	t.Run("S3 confirmed heuristic binding is not external", func(t *testing.T) {
		c := ltTestCompleteF1()
		o := ltTestObs()
		o.BindingQuality = BindingQualityHeuristic
		ev := EvaluateAssessment(c, ltTestStatic(), ltTestIndex("full", "exact"),
			[]Experiment{ltTestExp()}, []Observation{o}, ltTestPolicy(), nil, confirmed())
		if ev.Level != LevelS3 {
			t.Fatalf("level=%s, want S3", ev.Level)
		}
		if ev.ReportableExternal {
			t.Fatal("external=true, want false for heuristic binding (R-3.10)")
		}
		if ev.Confidence != ConfidenceMedium {
			t.Fatalf("confidence=%s, want medium", ev.Confidence)
		}
	})
	t.Run("S3 confirmed emulated requires policy and high quality", func(t *testing.T) {
		c := ltTestCompleteF1()
		o := ltTestObs()
		o.Platform = PlatformEmulated
		o.PlatformQuality = PlatformQualityHigh
		// 对外门槛的 S3 路径还需要 target 控制与 cross_seed_ok。
		e := ltTestExp()
		e.ControlResult = ControlResultPassed
		e.ControlKind = ControlKindTarget
		e.ControlPowerAdequate = true
		e.DistinctSeedsHit = 3
		ev := EvaluateAssessment(c, ltTestStatic(), ltTestIndex("full", "exact"),
			[]Experiment{e}, []Observation{o}, ltTestPolicy(), nil, confirmed())
		if ev.Level != LevelS3 {
			t.Fatalf("level=%s, want S3", ev.Level)
		}
		if ev.ReportableExternal {
			t.Fatal("external=true without allow_emulated_external")
		}
		policy := ltTestPolicy()
		policy.AllowEmulatedExternal = true
		ev = EvaluateAssessment(c, ltTestStatic(), ltTestIndex("full", "exact"),
			[]Experiment{e}, []Observation{o}, policy, nil, confirmed())
		if !ev.ReportableExternal {
			t.Fatalf("external=false, want true; reasons=%v", ev.Reasons)
		}
	})
	t.Run("S1 only", func(t *testing.T) {
		c := ltTestCandidate(FormRefcountRace) // no facts / four questions
		ev := EvaluateAssessment(c, ltTestStatic(), ltTestIndex("full", "exact"),
			nil, nil, ltTestPolicy(), nil, Review{})
		if ev.Level != LevelS1 {
			t.Fatalf("level=%s, want S1; reasons=%v", ev.Level, ev.Reasons)
		}
		if ev.ReportableInternal || ev.ReportableExternal {
			t.Fatal("reportable=true, want false for S1")
		}
	})
	t.Run("heuristic index gives provisional only", func(t *testing.T) {
		c := ltTestCompleteF1()
		ev := EvaluateAssessment(c, ltTestStatic(), ltTestIndex("heuristic", "partial"),
			nil, nil, ltTestPolicy(), nil, Review{})
		if ev.Level != Level("") {
			t.Fatalf("level=%q, want empty", ev.Level)
		}
		if !ev.Provisional {
			t.Fatal("provisional=false, want true")
		}
	})
	t.Run("limited control whitelist is external at S3", func(t *testing.T) {
		c := ltTestCandidate(FormLazyInitRace)
		c.Domains = []Domain{{DomainClassID: "d1", Kind: DomainThread}}
		c.Synchronization = []SyncPrimitive{
			{Primitive: "std::call_once", Scope: "init", Protects: "instance", FileLine: "src/init.cpp:9"},
		}
		c.EvidenceChain = []EvidenceNode{
			{FactID: "lp", Role: NodeLifetimePoint, File: "src/init.cpp", Line: 10},
			{FactID: "acc", Role: NodeAccess, File: "src/init.cpp", Line: 20},
		}
		c.OwnershipAnswers = OwnershipAnswers{
			Q1CreatedBy:   OwnerAnswer{Value: "init()", File: "src/init.cpp", Line: 10},
			Q2HeldBy:      OwnerAnswer{Value: "static", Form: "singleton", Domain: "d1", File: "src/init.cpp", Line: 10},
			Q3DestroyedBy: OwnerAnswer{Value: "atexit", Domain: "d1", File: "src/init.cpp", Line: 30},
			Q4:            VisibilityAnswer{Value: "lock", Status: ltQ4StatusKnown},
		}

		o := ltTestObs()
		o.EventType = EventInvariant
		o.Tool = ToolAssertion
		o.InvariantID = "inv-init-ordering"
		e := ltTestExp()
		e.ControlResult = ControlResultNotApplicable

		profile := &TaskProfile{LimitedControlForms: []Form{FormLazyInitRace}}

		ev := EvaluateAssessment(c, ltTestStatic(), ltTestIndex("full", "exact"),
			[]Experiment{e}, []Observation{o}, ltTestPolicy(), profile, confirmed())
		if ev.Level != LevelS3 {
			t.Fatalf("level=%s, want S3; reasons=%v", ev.Level, ev.Reasons)
		}
		if !ev.ReportableExternal {
			t.Fatalf("external=false, want true; reasons=%v", ev.Reasons)
		}
		if ev.Confidence != ConfidenceMedium {
			t.Fatalf("confidence=%s, want medium (R-3.7 cap)", ev.Confidence)
		}
	})
}

func TestExternalPlatformOK(t *testing.T) {
	policy := PlatformPolicy{AllowEmulatedExternal: true}
	cases := []struct {
		name   string
		obs    Observation
		policy PlatformPolicy
		want   bool
	}{
		{"native", Observation{Platform: PlatformNative}, PlatformPolicy{}, true},
		{"emulated allowed high", Observation{Platform: PlatformEmulated,
			PlatformQuality: PlatformQualityHigh}, policy, true},
		{"emulated allowed medium", Observation{Platform: PlatformEmulated,
			PlatformQuality: PlatformQualityMedium}, policy, false},
		{"emulated not allowed", Observation{Platform: PlatformEmulated,
			PlatformQuality: PlatformQualityHigh}, PlatformPolicy{}, false},
		{"foreign", Observation{Platform: PlatformForeign}, policy, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlatformExternalOK(tc.obs, tc.policy); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExternalCrossSeed(t *testing.T) {
	cases := []struct {
		name string
		exp  Experiment
		sd   StatisticalDefaults
		want bool
	}{
		{"absolute min 3", Experiment{DistinctSeedsHit: 3}, StatisticalDefaults{}, true},
		{"below absolute min", Experiment{DistinctSeedsHit: 2, PlannedSeeds: 10},
			StatisticalDefaults{}, false},
		{"fraction and min runs", Experiment{DistinctSeedsHit: 2, PlannedSeeds: 20, ValidRuns: 300},
			StatisticalDefaults{}, true},
		{"fraction below", Experiment{DistinctSeedsHit: 2, PlannedSeeds: 100, ValidRuns: 300},
			StatisticalDefaults{}, false},
		{"fraction but runs below", Experiment{DistinctSeedsHit: 2, PlannedSeeds: 20, ValidRuns: 299},
			StatisticalDefaults{}, false},
		{"profile tightens absolute", Experiment{DistinctSeedsHit: 3},
			StatisticalDefaults{CrossSeedMin: 5}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CrossSeedOK(tc.exp, tc.sd); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExternalLimitedControlOracle(t *testing.T) {
	profile := &TaskProfile{LimitedControlForms: []Form{FormLazyInitRace}}
	if !LimitedControlAllowed(FormLazyInitRace, profile) {
		t.Fatal("LimitedControlAllowed=false, want true")
	}
	if LimitedControlAllowed(FormRefcountRace, profile) {
		t.Fatal("LimitedControlAllowed=true for non-whitelisted form")
	}
	if LimitedControlAllowed(FormLazyInitRace, nil) {
		t.Fatal("LimitedControlAllowed=true for nil profile")
	}

	good := InvariantSpec{
		InvariantID:             "inv-1",
		OracleType:              OracleStateMachine,
		OracleEvaluationContext: OracleAtOperationSite,
		MetaTest:                InvariantMetaTest{SerializedPass: true, TriggerFail: true},
	}
	if !DeterministicOracleVerified(good) {
		t.Fatal("DeterministicOracleVerified=false for valid oracle")
	}
	bad := good
	bad.MetaTest.TriggerFail = false
	if DeterministicOracleVerified(bad) {
		t.Fatal("DeterministicOracleVerified=true without trigger_fail")
	}
	bad = good
	bad.OracleEvaluationContext = OracleEvalContext("ambient")
	if DeterministicOracleVerified(bad) {
		t.Fatal("DeterministicOracleVerified=true for invalid context")
	}
	bad = good
	bad.OracleType = OracleType("timing")
	if DeterministicOracleVerified(bad) {
		t.Fatal("DeterministicOracleVerified=true for invalid oracle type")
	}
}

func TestExternalFindingPredicate(t *testing.T) {
	primary := func() Assessment {
		return Assessment{
			CandidateKey: "ck-1",
			LineageID:    "L-claim",
			LineageRole:  LineageRoleClaim,
			Level:        LevelS3,
			Review:       Review{Verdict: VerdictConfirmed, Independence: IndependenceSameDriver},
			Provenance:   Provenance{DriverAuthorSession: "author-1"},
		}
	}

	t.Run("confirmation lineage new driver reviewer", func(t *testing.T) {
		conf := Assessment{
			CandidateKey:   "ck-1",
			LineageID:      "L-conf",
			LineageRole:    LineageRoleConfirmation,
			ConfirmationOf: "L-claim",
			Level:          LevelS3,
			Review: Review{Verdict: VerdictConfirmed,
				Independence: IndependenceNewDriverReviewer},
		}
		if !ExternalFindingPredicate(primary(), []Assessment{conf}, nil) {
			t.Fatal("want true for confirmation lineage")
		}
	})
	t.Run("same driver confirmation is not enough", func(t *testing.T) {
		conf := Assessment{
			CandidateKey:   "ck-1",
			LineageID:      "L-conf",
			LineageRole:    LineageRoleConfirmation,
			ConfirmationOf: "L-claim",
			Level:          LevelS3,
			Review: Review{Verdict: VerdictConfirmed,
				Independence: IndependenceSameDriver},
		}
		if ExternalFindingPredicate(primary(), []Assessment{conf}, nil) {
			t.Fatal("want false for same_driver confirmation")
		}
	})
	t.Run("independent reproduction with verified replay", func(t *testing.T) {
		p := primary()
		p.Review = Review{Verdict: VerdictConfirmed,
			Independence: IndependenceIndependentRepro, ReviewerSessionID: "reviewer-2"}
		exp := &Experiment{Reproduction: Reproduction{ReplayArtifactVerified: true}}
		if !ExternalFindingPredicate(p, nil, exp) {
			t.Fatal("want true for independent reproduction")
		}
	})
	t.Run("independent reproduction without replay artifact", func(t *testing.T) {
		p := primary()
		p.Review = Review{Verdict: VerdictConfirmed,
			Independence: IndependenceIndependentRepro, ReviewerSessionID: "reviewer-2"}
		exp := &Experiment{Reproduction: Reproduction{ReplayArtifactVerified: false}}
		if ExternalFindingPredicate(p, nil, exp) {
			t.Fatal("want false without replay artifact")
		}
	})
	t.Run("independent reproduction reviewer is driver author", func(t *testing.T) {
		p := primary()
		p.Review = Review{Verdict: VerdictConfirmed,
			Independence: IndependenceIndependentRepro, ReviewerSessionID: "author-1"}
		exp := &Experiment{Reproduction: Reproduction{ReplayArtifactVerified: true}}
		if ExternalFindingPredicate(p, nil, exp) {
			t.Fatal("want false when reviewer is the driver author")
		}
	})
	t.Run("primary below S3", func(t *testing.T) {
		p := primary()
		p.Level = LevelS2
		if ExternalFindingPredicate(p, nil, nil) {
			t.Fatal("want false for S2 primary")
		}
	})
}

func TestReviewIndependence(t *testing.T) {
	cases := []struct {
		name     string
		author   string
		runner   string
		reviewer string
		newDrv   bool
		repro    bool
		want     Independence
	}{
		{"same driver", "author-1", "runner-1", "author-1", false, false, IndependenceSameDriver},
		{"runner reviews same driver", "author-1", "runner-1", "runner-1", false, false, IndependenceSameDriver},
		{"reviewer writes new driver", "author-1", "runner-1", "reviewer-1", true, false, IndependenceNewDriverReviewer},
		{"author writes new driver", "author-1", "runner-1", "author-1", true, false, IndependenceNewDriverSameVerifier},
		{"independent reproduction wins", "author-1", "runner-1", "reviewer-1", false, true, IndependenceIndependentRepro},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReviewIndependence(tc.author, tc.runner, tc.reviewer, tc.newDrv, tc.repro)
			if got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestReviewDeriveConfidence(t *testing.T) {
	confirmed := Review{Verdict: VerdictConfirmed}
	cases := []struct {
		name     string
		level    Level
		review   Review
		external bool
		want     ConfidenceLevel
	}{
		{"S4 confirmed external", LevelS4, confirmed, true, ConfidenceHigh},
		{"S4 confirmed not external", LevelS4, confirmed, false, ConfidenceMedium},
		{"S3 confirmed", LevelS3, confirmed, false, ConfidenceMedium},
		{"S2 confirmed", LevelS2, confirmed, false, ConfidenceLow},
		{"S3 unconfirmed", LevelS3, Review{}, false, ConfidenceLow},
		{"empty level", Level(""), confirmed, true, ConfidenceLow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveConfidence(tc.level, tc.review, tc.external); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}
