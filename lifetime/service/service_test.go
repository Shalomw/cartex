package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/lifetime"
)

type fakeBackend struct {
	candidates  []lifetime.Candidate
	static      []lifetime.StaticSupport
	noRisks     []lifetime.NoRisk
	unknowns    []lifetime.UnknownRecord
	leads       []lifetime.NewLead
	probes      []string
	claimed     []string
	renewed     []string
	released    []string
	completed   []string
	reconRuns   []lifetime.ReconciliationRun
	reconFind   []lifetime.ReconciliationFinding
	reconTaskID string

	cand         *lifetime.Candidate
	support      *lifetime.StaticSupport
	unit         *lifetime.CoverageUnit
	index        *lifetime.IndexOutput
	observations []lifetime.Observation
	experiments  []lifetime.Experiment
	assessments  []lifetime.Assessment

	// bootstrap / 查询面记录。
	lineageID     string
	lineages      []lifetime.LineageInput
	profiles      []lifetime.TaskProfile
	ingestTaskID  string
	ingestLineage string
	ingested      []lifetime.IndexOutput
	initTaskID    string
	initLineageID string
	initUnits     []lifetime.CoverageUnit
	initPackages  []lifetime.WorkPackage
	families      []lifetime.ObjectFamily
	familyDetails map[string]*lifetime.FamilyDetail
	coverageList  []lifetime.CoverageUnit
	listedCands   []lifetime.Candidate
}

func (f *fakeBackend) RecordStaticSupport(_ context.Context, _ string, s lifetime.StaticSupport) (string, error) {
	f.static = append(f.static, s)
	return "sup_1", nil
}
func (f *fakeBackend) RecordCandidate(_ context.Context, _ string, c lifetime.Candidate) (string, error) {
	f.candidates = append(f.candidates, c)
	return "cand_1", nil
}
func (f *fakeBackend) RecordNoRisk(_ context.Context, _ string, n lifetime.NoRisk) (string, error) {
	f.noRisks = append(f.noRisks, n)
	return "nr_1", nil
}
func (f *fakeBackend) RecordUnknown(_ context.Context, _ string, u lifetime.UnknownRecord) (string, error) {
	f.unknowns = append(f.unknowns, u)
	return "unk_1", nil
}
func (f *fakeBackend) RecordNewLead(_ context.Context, _ string, l lifetime.NewLead) (string, error) {
	f.leads = append(f.leads, l)
	return "lead_1", nil
}
func (f *fakeBackend) RequestProbe(_ context.Context, _, candidateID, _, _ string) (string, error) {
	f.probes = append(f.probes, candidateID)
	return "probe_1", nil
}
func (f *fakeBackend) Claim(_ context.Context, _ string, q lifetime.QueueKind, session string, _ time.Duration) (*lifetime.WorkItem, error) {
	f.claimed = append(f.claimed, session)
	return &lifetime.WorkItem{ItemID: "item_1", QueueKind: q, State: lifetime.WorkItemClaimed, ClaimID: "claim_1", ClaimedBySession: session}, nil
}
func (f *fakeBackend) Renew(_ context.Context, itemID, claimID, _ string, _ time.Duration) (*lifetime.WorkItem, error) {
	f.renewed = append(f.renewed, itemID+"/"+claimID)
	return &lifetime.WorkItem{ItemID: itemID, ClaimID: claimID, State: lifetime.WorkItemClaimed}, nil
}
func (f *fakeBackend) Release(_ context.Context, itemID, claimID, _, _ string) error {
	f.released = append(f.released, itemID+"/"+claimID)
	return nil
}
func (f *fakeBackend) Complete(_ context.Context, itemID, claimID, _, _ string) error {
	f.completed = append(f.completed, itemID+"/"+claimID)
	return nil
}
func (f *fakeBackend) SnapshotForReconcile(_ context.Context, taskID string) (*lifetime.ReconcileSnapshot, error) {
	f.reconTaskID = taskID
	return &lifetime.ReconcileSnapshot{TaskID: taskID, Now: time.Now().UTC()}, nil
}
func (f *fakeBackend) SaveReconciliation(_ context.Context, _ string, run lifetime.ReconciliationRun, findings []lifetime.ReconciliationFinding) error {
	f.reconRuns = append(f.reconRuns, run)
	f.reconFind = append(f.reconFind, findings...)
	return nil
}
func (f *fakeBackend) GetCandidate(context.Context, string) (*lifetime.Candidate, error) {
	if f.cand == nil {
		return nil, errors.New("no candidate")
	}
	return f.cand, nil
}
func (f *fakeBackend) GetStaticSupport(context.Context, string) (*lifetime.StaticSupport, error) {
	if f.support == nil {
		return nil, errors.New("no support")
	}
	return f.support, nil
}
func (f *fakeBackend) GetCoverage(context.Context, string) (*lifetime.CoverageUnit, error) {
	if f.unit == nil {
		return nil, errors.New("no coverage")
	}
	return f.unit, nil
}
func (f *fakeBackend) GetCoverageForTask(context.Context, string, string) (*lifetime.CoverageUnit, error) {
	return f.GetCoverage(context.Background(), "")
}
func (f *fakeBackend) GetIndexOutput(context.Context, string, string) (*lifetime.IndexOutput, error) {
	if f.index == nil {
		return nil, errors.New("no index")
	}
	return f.index, nil
}
func (f *fakeBackend) ListObservations(context.Context, string) ([]lifetime.Observation, error) {
	return f.observations, nil
}
func (f *fakeBackend) ListExperiments(context.Context, string) ([]lifetime.Experiment, error) {
	return f.experiments, nil
}
func (f *fakeBackend) MaterializeAssessment(_ context.Context, _ string, a lifetime.Assessment) error {
	f.assessments = append(f.assessments, a)
	return nil
}
func (f *fakeBackend) ListAssessments(context.Context, string) ([]lifetime.Assessment, error) {
	return f.assessments, nil
}

// --- bootstrap / 发现查询面 -------------------------------------------------

func (f *fakeBackend) CreateLineage(_ context.Context, _ string, in lifetime.LineageInput) (string, error) {
	f.lineages = append(f.lineages, in)
	if f.lineageID != "" {
		return f.lineageID, nil
	}
	return lifetime.LineageIDFrom(in), nil
}

func (f *fakeBackend) RegisterTaskProfile(_ context.Context, _ string, p *lifetime.TaskProfile) (string, error) {
	f.profiles = append(f.profiles, *p)
	return p.PolicyHash, nil
}

func (f *fakeBackend) IngestIndex(_ context.Context, taskID, lineageID string, idx lifetime.IndexOutput) error {
	f.ingestTaskID = taskID
	f.ingestLineage = lineageID
	f.ingested = append(f.ingested, idx)
	return nil
}

func (f *fakeBackend) InitCoverage(_ context.Context, taskID, lineageID string, units []lifetime.CoverageUnit, packages []lifetime.WorkPackage) error {
	f.initTaskID = taskID
	f.initLineageID = lineageID
	f.initUnits = append(f.initUnits, units...)
	f.initPackages = append(f.initPackages, packages...)
	return nil
}

func (f *fakeBackend) ListFamilies(_ context.Context, _ string) ([]lifetime.ObjectFamily, error) {
	return f.families, nil
}

func (f *fakeBackend) GetFamilyDetail(_ context.Context, _, familyID string) (*lifetime.FamilyDetail, error) {
	if d, ok := f.familyDetails[familyID]; ok {
		return d, nil
	}
	return nil, errors.New("family not found")
}

func (f *fakeBackend) ListCoverage(_ context.Context, _ string) ([]lifetime.CoverageUnit, error) {
	return f.coverageList, nil
}

func (f *fakeBackend) ListCandidates(_ context.Context, _ string) ([]lifetime.Candidate, error) {
	return f.listedCands, nil
}

func newTestServer(t *testing.T) (*Server, *fakeBackend) {
	t.Helper()
	fb := &fakeBackend{}
	srv, err := New(fb, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, fb
}

func doTool(t *testing.T, s *Server, role, session, taskID, tool, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/tools/"+tool, strings.NewReader(body))
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

func decodeResp(t *testing.T, rec *httptest.ResponseRecorder) ToolResponse {
	t.Helper()
	var out ToolResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestHealth(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
}

func TestMissingEnvelopeRejected(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doTool(t, s, "", "", "task1", lifetime.ToolRecordUnknown, `{}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRoleToolSurfaceEnforced(t *testing.T) {
	s, _ := newTestServer(t)
	cases := []struct {
		role string
		tool string
		want int
	}{
		{string(lifetime.RoleLifetimeAuditor), lifetime.ToolBuildDriver, http.StatusForbidden},
		{string(lifetime.RoleLifetimeVerifier), lifetime.ToolRecordCandidate, http.StatusForbidden},
		{string(lifetime.RoleReporter), lifetime.ToolReportFinding, http.StatusNotImplemented},
		{string(lifetime.RoleLifetimeReviewer), lifetime.ToolBuildDriver, http.StatusNotImplemented},
	}
	for _, tc := range cases {
		rec := doTool(t, s, tc.role, "session-1", "task1", tc.tool, `{}`)
		if rec.Code != tc.want {
			t.Errorf("%s %s status = %d, want %d (body %s)", tc.role, tc.tool, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestAuditorRecordCandidate(t *testing.T) {
	s, fb := newTestServer(t)
	body := `{
		"unit_id":"unit-1",
		"support_id":"sup-1",
		"package":{
			"family":{"family_id":"f1","type_canonical":"Foo","alloc_site":"a.cc:10","storage_class":"heap","lifetime_form":"raw"},
			"domain_pair_id":"unresolved:f1:abc",
			"pair_basis":"logical",
			"domain_confidence":"high",
			"primary_form":"F1",
			"contract_status":"known"
		}
	}`
	rec := doTool(t, s, string(lifetime.RoleLifetimeAuditor), "auditor-1", "task1", lifetime.ToolRecordCandidate, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(fb.candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(fb.candidates))
	}
	if fb.candidates[0].UnitID != "unit-1" || fb.candidates[0].PrimaryForm != lifetime.FormRefcountRace {
		t.Fatalf("candidate = %+v", fb.candidates[0])
	}
}

func TestClaimQueuePermission(t *testing.T) {
	s, fb := newTestServer(t)
	// 审计员不能 claim Q2：工具组允许 claim，但队列白名单只给 Q1/Q5。
	rec := doTool(t, s, string(lifetime.RoleLifetimeAuditor), "auditor-1", "task1", lifetime.ToolClaim, `{"queue_kind":"Q2"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("auditor Q2 claim status = %d, want 403", rec.Code)
	}
	// verifier 自取 Q2。
	rec = doTool(t, s, string(lifetime.RoleLifetimeVerifier), "verifier-1", "task1", lifetime.ToolClaim, `{"queue_kind":"Q2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("verifier Q2 claim status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(fb.claimed) != 1 || fb.claimed[0] != "verifier-1" {
		t.Fatalf("claimed = %v", fb.claimed)
	}
}

func TestClaimFilterRejectsExpansion(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doTool(t, s, string(lifetime.RoleLifetimeVerifier), "verifier-1", "task1", lifetime.ToolClaim, `{"queue_kind":"Q2","filter":{"family_id":"f1"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestReconcileRoleGate(t *testing.T) {
	s, fb := newTestServer(t)
	// verifier 不能跑全量对账。
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/task1/reconcile", nil)
	req.Header.Set("X-Lifetime-Role", string(lifetime.RoleLifetimeVerifier))
	req.Header.Set("X-Lifetime-Session", "verifier-1")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("verifier reconcile status = %d, want 403", rec.Code)
	}
	// reconciler 可以。
	req = httptest.NewRequest(http.MethodPost, "/v1/tasks/task1/reconcile", nil)
	req.Header.Set("X-Lifetime-Role", string(lifetime.RoleReconciler))
	req.Header.Set("X-Lifetime-Session", "recon-1")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reconciler reconcile status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(fb.reconRuns) != 1 || fb.reconTaskID != "task1" {
		t.Fatalf("recon runs=%v task=%q", fb.reconRuns, fb.reconTaskID)
	}
}

func TestAuditorCannotRequestVerification(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doTool(t, s, string(lifetime.RoleLifetimeAuditor), "auditor-1", "task1", lifetime.ToolSubmitVerification, `{}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestUnknownToolRejected(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doTool(t, s, string(lifetime.RoleLifetimeAuditor), "auditor-1", "task1", "not_a_tool", `{}`)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400/403", rec.Code)
	}
}

func TestProfileEndpoint(t *testing.T) {
	fb := &fakeBackend{}
	p := lifetime.DefaultTaskProfile()
	srv, err := New(fb, Options{Profile: p})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/profile", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"profile"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
