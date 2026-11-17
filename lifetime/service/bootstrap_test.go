package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/lifetime"
	"github.com/Autumn-27/artex/lifetime/index"
)

// stubBuildIndex 用固定索引产物替换 index.Build，返回记录调用参数的指针。
func stubBuildIndex(t *testing.T, idx *lifetime.IndexOutput) *index.Options {
	t.Helper()
	var got index.Options
	old := buildIndex
	buildIndex = func(opts index.Options) (*lifetime.IndexOutput, error) {
		got = opts
		return idx, nil
	}
	t.Cleanup(func() { buildIndex = old })
	return &got
}

func doBootstrap(t *testing.T, s *Server, role, session, taskID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/bootstrap", strings.NewReader(body))
	if role != "" {
		req.Header.Set("X-Lifetime-Role", role)
	}
	if session != "" {
		req.Header.Set("X-Lifetime-Session", session)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func doGet(t *testing.T, s *Server, role, session, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if role != "" {
		req.Header.Set("X-Lifetime-Role", role)
	}
	if session != "" {
		req.Header.Set("X-Lifetime-Session", session)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, want, rec.Body.String())
	}
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func bootstrapFixture() *lifetime.IndexOutput {
	pair := lifetime.DomainPair{
		PairClassID:      lifetime.PairClassID("dom-a", "dom-b"),
		DomainA:          "dom-a",
		DomainB:          "dom-b",
		DomainConfidence: lifetime.DomainConfidenceHigh,
	}
	return &lifetime.IndexOutput{
		IndexHash:      "idx-1",
		IndexQuality:   "heuristic",
		TypeResolution: "partial",
		ObjectFamilies: []lifetime.ObjectFamily{{
			FamilyID:        "fam-numfmt",
			TypeCanonical:   "v8::internal::JSNumberFormat",
			AllocSite:       "src/objects/js-number-format.cc:42",
			StorageClass:    lifetime.StorageHeap,
			LifetimeForm:    lifetime.LifetimeRaw,
			ApplicableForms: []lifetime.Form{lifetime.FormDanglingThis, lifetime.FormStaticDestroy},
		}},
		Domains: []lifetime.Domain{{
			DomainClassID:     "dom-a",
			Kind:              lifetime.DomainThread,
			InstanceSite:      "src/a.cc:1",
			Serialization:     lifetime.SerializationConcurrent,
			ConcurrencyDegree: lifetime.ConcurrencyOne,
		}},
		DomainPairs: []lifetime.DomainPair{pair},
	}
}

func TestBootstrapRoleGate(t *testing.T) {
	s, fb := newTestServer(t)
	body := `{"source_root":"/src/v8","target_files":["src/objects/js-number-format.cc"]}`

	// 缺 envelope → 401。
	rec := doBootstrap(t, s, "", "", "task-1", body)
	wantStatus(t, rec, http.StatusUnauthorized)

	// 非 planner/reconciler → 403，且不得触碰后端。
	rec = doBootstrap(t, s, string(lifetime.RoleLifetimeAuditor), "aud-1", "task-1", body)
	wantStatus(t, rec, http.StatusForbidden)
	if len(fb.lineages) != 0 || len(fb.ingested) != 0 || len(fb.initUnits) != 0 {
		t.Fatalf("forbidden bootstrap wrote backend state: lineages=%d ingested=%d units=%d",
			len(fb.lineages), len(fb.ingested), len(fb.initUnits))
	}
}

func TestBootstrapValidation(t *testing.T) {
	s, _ := newTestServer(t)
	cases := []string{
		`{"target_files":["a.cc"]}`,                                                            // 缺 source_root
		`{"source_root":"/src/v8"}`,                                                            // 缺 target
		`{"source_root":"/src/v8","target_files":["a.cc"],"max_files":-1}`,                     // 负 max_files
		`{"source_root":"/src/v8","target_files":["a.cc"],"profile":{"name":"","version":""}}`, // 非法 profile
		``, // 空 body
	}
	for _, body := range cases {
		rec := doBootstrap(t, s, string(lifetime.RolePlanner), "planner-1", "task-1", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400 (%s)", body, rec.Code, rec.Body.String())
		}
	}
}

func TestBootstrapBuildsCoverage(t *testing.T) {
	s, fb := newTestServer(t)
	idx := bootstrapFixture()
	gotOpts := stubBuildIndex(t, idx)

	body := `{
		"source_root":"/src/v8",
		"target_files":["src/objects/js-number-format.cc"],
		"target_subdir":["src/objects"],
		"max_files":400
	}`
	rec := doBootstrap(t, s, string(lifetime.RolePlanner), "planner-1", "task-boot", body)
	wantStatus(t, rec, http.StatusOK)

	var result lifetime.BootstrapResult
	decodeInto(t, rec, &result)

	if result.TaskID != "task-boot" || result.LineageID == "" {
		t.Fatalf("result identity = %+v", result)
	}
	if result.PolicyHash == "" || result.IndexHash != "idx-1" || result.IndexQuality != "heuristic" {
		t.Fatalf("result hashes = %+v", result)
	}
	if result.Families != 1 || result.Domains != 1 || result.DomainPairs != 1 {
		t.Fatalf("index counts = %+v", result)
	}
	if result.CoverageUnits != 2 || result.WorkItems != 1 {
		t.Fatalf("coverage counts = %+v", result)
	}

	// 索引器收到映射后的 Options。
	if gotOpts.SourceRoot != "/src/v8" || gotOpts.MaxFiles != 400 ||
		len(gotOpts.TargetFiles) != 1 || len(gotOpts.TargetSubdirs) != 1 {
		t.Fatalf("index options = %+v", gotOpts)
	}

	// lineage 必须按规格确定性重算。
	if len(fb.lineages) != 1 {
		t.Fatalf("CreateLineage calls = %d", len(fb.lineages))
	}
	in := fb.lineages[0]
	if in.IndexHash != "idx-1" || in.BuildID != "bootstrap" || in.DriverSpecHash != "none" ||
		in.HarnessVersion != "m0" || in.InstrumentationProfileHash != "none" ||
		in.PolicyHash != result.PolicyHash || in.ProfileVersion != "1.0" {
		t.Fatalf("lineage input = %+v", in)
	}
	if want := bootstrapSourceHash([]string{"src/objects/js-number-format.cc"}, []string{"src/objects"}, "idx-1"); in.SourceManifestHash != want {
		t.Fatalf("source hash = %s, want %s", in.SourceManifestHash, want)
	}
	if lifetime.LineageIDFrom(in) != result.LineageID {
		t.Fatalf("lineage id = %s, recomputed %s", result.LineageID, lifetime.LineageIDFrom(in))
	}

	if len(fb.profiles) != 1 || fb.profiles[0].PolicyHash != result.PolicyHash {
		t.Fatalf("registered profiles = %+v", fb.profiles)
	}
	if len(fb.ingested) != 1 || fb.ingestTaskID != "task-boot" || fb.ingestLineage != result.LineageID {
		t.Fatalf("ingest = task %q lineage %q count %d", fb.ingestTaskID, fb.ingestLineage, len(fb.ingested))
	}

	if len(fb.initUnits) != 2 || len(fb.initPackages) != 1 {
		t.Fatalf("InitCoverage units=%d packages=%d", len(fb.initUnits), len(fb.initPackages))
	}
	if fb.initTaskID != "task-boot" || fb.initLineageID != result.LineageID {
		t.Fatalf("InitCoverage task=%q lineage=%q", fb.initTaskID, fb.initLineageID)
	}

	pairID := lifetime.PairClassID("dom-a", "dom-b")
	forms := map[lifetime.Form]bool{}
	for _, u := range fb.initUnits {
		if u.FamilyID != "fam-numfmt" || u.DomainPairID != pairID {
			t.Fatalf("unit = %+v", u)
		}
		if u.UnitKind != lifetime.UnitKindFamily || u.Status != lifetime.CoverageUnexamined ||
			u.ReviewStatus != "self_asserted" || u.LineageID != result.LineageID {
			t.Fatalf("unit defaults = %+v", u)
		}
		if want := lifetime.UnitID(lifetime.UnitKindFamily, "fam-numfmt", "", pairID, u.Form); u.UnitID != want {
			t.Fatalf("unit id = %s, want %s", u.UnitID, want)
		}
		forms[u.Form] = true
	}
	if !forms[lifetime.FormDanglingThis] || !forms[lifetime.FormStaticDestroy] {
		t.Fatalf("coverage forms = %v", forms)
	}

	pkg := fb.initPackages[0]
	if pkg.PackageID != lifetime.CanonicalHash("pkg", "fam-numfmt", pairID) {
		t.Fatalf("package id = %s", pkg.PackageID)
	}
	if pkg.FamilyID != "fam-numfmt" || pkg.DomainPairID != pairID || len(pkg.ApplicableForms) != 2 {
		t.Fatalf("package = %+v", pkg)
	}
}

func TestBootstrapSynthesizesPairAndForm(t *testing.T) {
	s, fb := newTestServer(t)
	idx := &lifetime.IndexOutput{
		IndexHash:    "idx-synth",
		IndexQuality: "full",
		ObjectFamilies: []lifetime.ObjectFamily{{
			FamilyID:      "fam-bare",
			TypeCanonical: "Foo",
			AllocSite:     "foo.cc:1",
			StorageClass:  lifetime.StorageHeap,
			LifetimeForm:  lifetime.LifetimeRaw,
			// 无 applicable_forms，无 domain_pairs。
		}},
	}
	stubBuildIndex(t, idx)

	body := `{"source_root":"/src/v8","target_files":["foo.cc"]}`
	rec := doBootstrap(t, s, string(lifetime.RoleReconciler), "recon-1", "task-synth", body)
	wantStatus(t, rec, http.StatusOK)

	var result lifetime.BootstrapResult
	decodeInto(t, rec, &result)
	if result.Families != 1 || result.CoverageUnits != 1 || result.WorkItems != 1 {
		t.Fatalf("counts = %+v", result)
	}
	if result.Domains != 0 || result.DomainPairs != 0 {
		t.Fatalf("index counts should stay as produced by indexer: %+v", result)
	}

	d := lifetime.DomainClassID(lifetime.DomainThread, "/src/v8:1", "")
	wantPair := lifetime.PairClassID(d, d)
	if len(fb.initUnits) != 1 || len(fb.initPackages) != 1 {
		t.Fatalf("units=%d packages=%d", len(fb.initUnits), len(fb.initPackages))
	}
	u := fb.initUnits[0]
	if u.DomainPairID != wantPair || u.FamilyID != "fam-bare" {
		t.Fatalf("synthetic unit = %+v", u)
	}
	if u.Form != lifetime.FormDanglingThis {
		t.Fatalf("fallback form = %s, want F2", u.Form)
	}
	if u.UnitID != lifetime.UnitID(lifetime.UnitKindFamily, "fam-bare", "", wantPair, lifetime.FormDanglingThis) {
		t.Fatalf("unit id = %s", u.UnitID)
	}
	pkg := fb.initPackages[0]
	if pkg.DomainPairID != wantPair || len(pkg.ApplicableForms) != 1 || pkg.ApplicableForms[0] != lifetime.FormDanglingThis {
		t.Fatalf("synthetic package = %+v", pkg)
	}

	// 同一请求重复 bootstrap：lineage / unit id 必须稳定（确定性要求）。
	rec = doBootstrap(t, s, string(lifetime.RoleReconciler), "recon-1", "task-synth", body)
	wantStatus(t, rec, http.StatusOK)
	var second lifetime.BootstrapResult
	decodeInto(t, rec, &second)
	if second.LineageID != result.LineageID {
		t.Fatalf("lineage not deterministic: %s != %s", second.LineageID, result.LineageID)
	}
	if len(fb.initUnits) != 2 || fb.initUnits[1].UnitID != u.UnitID {
		t.Fatalf("unit id not deterministic: %+v", fb.initUnits)
	}
}

func TestFamiliesFilter(t *testing.T) {
	s, fb := newTestServer(t)
	fb.families = []lifetime.ObjectFamily{
		{FamilyID: "f1", TypeCanonical: "v8::internal::JSNumberFormat", AllocSite: "src/objects/js-number-format.cc:42"},
		{FamilyID: "f2", TypeCanonical: "v8::internal::Context", AllocSite: "src/runtime/context.cc:10"},
		{FamilyID: "f3", TypeCanonical: "v8::internal::JSNumberFormatCache", AllocSite: "src/objects/js-number-format.cc:99"},
	}

	type familiesResp struct {
		Families []lifetime.ObjectFamily `json:"families"`
	}

	// file 对 alloc_site / type_canonical 做子串匹配。
	rec := doGet(t, s, string(lifetime.RoleLifetimeAuditor), "aud-1", "/v1/tasks/t1/families?file=NumberFormat")
	wantStatus(t, rec, http.StatusOK)
	var got familiesResp
	decodeInto(t, rec, &got)
	if len(got.Families) != 2 || got.Families[0].FamilyID == "f2" || got.Families[1].FamilyID == "f2" {
		t.Fatalf("file filter = %+v", got.Families)
	}

	// subdir 对 alloc_site 做前缀匹配。
	rec = doGet(t, s, string(lifetime.RoleLifetimeAuditor), "aud-1", "/v1/tasks/t1/families?subdir=src/runtime")
	wantStatus(t, rec, http.StatusOK)
	got = familiesResp{}
	decodeInto(t, rec, &got)
	if len(got.Families) != 1 || got.Families[0].FamilyID != "f2" {
		t.Fatalf("subdir filter = %+v", got.Families)
	}

	// limit 截断。
	rec = doGet(t, s, string(lifetime.RoleLifetimeAuditor), "aud-1", "/v1/tasks/t1/families?limit=1")
	wantStatus(t, rec, http.StatusOK)
	got = familiesResp{}
	decodeInto(t, rec, &got)
	if len(got.Families) != 1 {
		t.Fatalf("limit filter = %+v", got.Families)
	}

	// 非法 limit。
	rec = doGet(t, s, string(lifetime.RoleLifetimeAuditor), "aud-1", "/v1/tasks/t1/families?limit=abc")
	wantStatus(t, rec, http.StatusBadRequest)

	// 缺 envelope。
	rec = doGet(t, s, "", "", "/v1/tasks/t1/families")
	wantStatus(t, rec, http.StatusUnauthorized)
}

func TestFamilyDetail(t *testing.T) {
	s, fb := newTestServer(t)
	fb.familyDetails = map[string]*lifetime.FamilyDetail{
		"f1": {
			Family:         lifetime.ObjectFamily{FamilyID: "f1", TypeCanonical: "Foo"},
			Coverage:       []lifetime.CoverageUnit{{UnitID: "u1", FamilyID: "f1"}},
			LifetimePoints: []lifetime.LifetimePoint{{FamilyID: "f1", Site: "a.cc:1"}},
			LineageID:      "lin-1",
			IndexHash:      "idx-1",
		},
	}

	rec := doGet(t, s, string(lifetime.RoleOwner), "owner-1", "/v1/tasks/t1/families/f1")
	wantStatus(t, rec, http.StatusOK)
	var detail lifetime.FamilyDetail
	decodeInto(t, rec, &detail)
	if detail.Family.FamilyID != "f1" || detail.LineageID != "lin-1" ||
		len(detail.Coverage) != 1 || len(detail.LifetimePoints) != 1 {
		t.Fatalf("detail = %+v", detail)
	}

	// fake 对未知族返回错误 → 404。
	rec = doGet(t, s, string(lifetime.RoleOwner), "owner-1", "/v1/tasks/t1/families/nope")
	wantStatus(t, rec, http.StatusNotFound)
}

func TestCoverageEndpoint(t *testing.T) {
	s, fb := newTestServer(t)
	fb.coverageList = []lifetime.CoverageUnit{
		{UnitID: "u1", UnitKind: lifetime.UnitKindFamily, FamilyID: "f1", DomainPairID: "p", Form: lifetime.FormDanglingThis,
			Status: lifetime.CoverageNoRisk, ReviewStatus: "reviewed", LineageID: "lin-1"},
		{UnitID: "u2", UnitKind: lifetime.UnitKindFamily, FamilyID: "f2", DomainPairID: "p", Form: lifetime.FormStaticDestroy,
			Status: lifetime.CoverageUnexamined, ReviewStatus: "self_asserted", LineageID: "lin-1"},
	}

	rec := doGet(t, s, string(lifetime.RolePlanner), "planner-1", "/v1/tasks/t1/coverage")
	wantStatus(t, rec, http.StatusOK)

	var got struct {
		Stats lifetime.CoverageStats  `json:"stats"`
		Units []lifetime.CoverageUnit `json:"units"`
	}
	decodeInto(t, rec, &got)
	if len(got.Units) != 2 {
		t.Fatalf("units = %+v", got.Units)
	}
	if got.Stats.EnumeratedUnits != 2 || got.Stats.ConcludedUnits != 1 ||
		got.Stats.RiskClosedUnits != 1 || got.Stats.ReviewedConcludedUnits != 1 {
		t.Fatalf("stats = %+v", got.Stats)
	}
	if got.Stats.CoveragePct != 50 || got.Stats.RiskClosedPctAll != 50 {
		t.Fatalf("percentages = %+v", got.Stats)
	}

	// 空账本返回数组而不是 null。
	fb.coverageList = nil
	rec = doGet(t, s, string(lifetime.RolePlanner), "planner-1", "/v1/tasks/t1/coverage")
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"units":[]`) {
		t.Fatalf("empty units should be []: %s", rec.Body.String())
	}
}

func TestCandidatesEndpoint(t *testing.T) {
	s, fb := newTestServer(t)
	fb.listedCands = []lifetime.Candidate{{
		CandidateID:      "cand-1",
		CandidatePackage: lifetime.CandidatePackage{Family: lifetime.ObjectFamily{FamilyID: "f1"}},
	}}

	rec := doGet(t, s, string(lifetime.RoleLifetimeReviewer), "rev-1", "/v1/tasks/t1/candidates")
	wantStatus(t, rec, http.StatusOK)

	var got struct {
		Candidates []lifetime.Candidate `json:"candidates"`
	}
	decodeInto(t, rec, &got)
	if len(got.Candidates) != 1 || got.Candidates[0].CandidateID != "cand-1" {
		t.Fatalf("candidates = %+v", got.Candidates)
	}
}
