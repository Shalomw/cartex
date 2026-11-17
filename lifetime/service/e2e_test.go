package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Autumn-27/artex/lifetime"
	"github.com/Autumn-27/artex/lifetime/store"
)

// TestEndToEndDiscoveryQueue 用真实 PostgreSQL 走一遍
// IngestIndex → InitCoverage → Q1 claim → record_candidate → Q2 claim → complete。
// 未设置 LIFETIME_TEST_DSN 时跳过；测试只清理本 task 的行。
func TestEndToEndDiscoveryQueue(t *testing.T) {
	dsn := os.Getenv("LIFETIME_TEST_DSN")
	if dsn == "" {
		t.Skip("LIFETIME_TEST_DSN is not set")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	taskID := fmt.Sprintf("e2e-%d", time.Now().UnixNano())
	defer cleanupLifetimeTask(t, st, taskID)

	lineageID, err := st.CreateLineage(ctx, taskID, lifetime.LineageInput{
		SourceManifestHash: "src-e2e", IndexHash: "idx-e2e", BuildID: "build-e2e",
		DriverSpecHash: "driver-e2e", PolicyHash: "policy-e2e",
		ProfileVersion: "v1.8", HarnessVersion: "m0",
		InstrumentationProfileHash: "instr-e2e",
	})
	if err != nil {
		t.Fatalf("CreateLineage: %v", err)
	}
	p := lifetime.DefaultTaskProfile()
	if _, err := st.RegisterTaskProfile(ctx, taskID, p); err != nil {
		t.Fatalf("RegisterTaskProfile: %v", err)
	}

	fam := lifetime.ObjectFamily{
		FamilyID: "fam-e2e", TypeCanonical: "Foo", AllocSite: "foo.cc:10",
		StorageClass: lifetime.StorageHeap, LifetimeForm: lifetime.LifetimeRaw,
		ApplicableForms: []lifetime.Form{lifetime.FormRefcountRace},
	}
	idx := lifetime.IndexOutput{
		IndexHash: "idx-e2e", IndexQuality: "full", TypeResolution: "exact",
		ObjectFamilies: []lifetime.ObjectFamily{fam},
		Domains: []lifetime.Domain{
			{DomainClassID: "d1", Kind: lifetime.DomainThread, InstanceSite: "t.cc:1", Serialization: lifetime.SerializationConcurrent, ConcurrencyDegree: lifetime.ConcurrencyN},
			{DomainClassID: "d2", Kind: lifetime.DomainThread, InstanceSite: "t.cc:2", Serialization: lifetime.SerializationConcurrent, ConcurrencyDegree: lifetime.ConcurrencyN},
		},
		DomainPairs: []lifetime.DomainPair{
			{PairClassID: "pair-e2e", DomainA: "d1", DomainB: "d2", DomainConfidence: lifetime.DomainConfidenceHigh},
		},
		LifetimePoints: []lifetime.LifetimePoint{
			{FamilyID: "fam-e2e", Site: "foo.cc:10", Role: lifetime.NodeLifetimePoint, SourceRef: "foo.cc:10"},
		},
	}
	if err := st.IngestIndex(ctx, taskID, lineageID, idx); err != nil {
		t.Fatalf("IngestIndex: %v", err)
	}

	unitID := lifetime.UnitID(lifetime.UnitKindFamily, "fam-e2e", "", "pair-e2e", lifetime.FormRefcountRace)
	unit := lifetime.NewCoverageUnit(lifetime.UnitKindFamily, "fam-e2e", "", "pair-e2e", lifetime.FormRefcountRace, lineageID)
	unit.UnitID = unitID
	pkg := lifetime.WorkPackage{
		PackageID: "pkg-e2e", FamilyID: "fam-e2e", DomainPairID: "pair-e2e",
		ApplicableForms: []lifetime.Form{lifetime.FormRefcountRace},
	}
	if err := st.InitCoverage(ctx, taskID, lineageID, []lifetime.CoverageUnit{unit}, []lifetime.WorkPackage{pkg}); err != nil {
		t.Fatalf("InitCoverage: %v", err)
	}

	// 静态支持：S1 要求 chain 含 lifetime_point/access，且每个节点有 verified citation。
	now := time.Now().UTC()
	supID, err := st.RecordStaticSupport(ctx, taskID, lifetime.StaticSupport{
		UnitID: unitID, IndexHash: "idx-e2e", CreatedBy: "auditor-e2e",
		Facts: []lifetime.EvidenceNode{
			{FactID: "f1", Role: lifetime.NodeLifetimePoint, File: "foo.cc", Line: 10, Symbol: "Foo::Foo"},
			{FactID: "f2", Role: lifetime.NodeHolder, File: "hold.cc", Line: 3, Symbol: "holder"},
			{FactID: "f3", Role: lifetime.NodeAccess, File: "use.cc", Line: 7, Symbol: "refs--"},
		},
		Chain: []lifetime.EvidenceNode{
			{FactID: "f1", Role: lifetime.NodeLifetimePoint, File: "foo.cc", Line: 10, Symbol: "Foo::Foo"},
			{FactID: "f2", Role: lifetime.NodeHolder, File: "hold.cc", Line: 3, Symbol: "holder"},
			{FactID: "f3", Role: lifetime.NodeAccess, File: "use.cc", Line: 7, Symbol: "refs--"},
		},
		CitationChecks: []lifetime.CitationCheck{
			{Status: lifetime.CitationVerified, File: "foo.cc", Line: 10, Role: lifetime.NodeLifetimePoint, CheckedAt: now, CheckerVersion: "m0"},
			{Status: lifetime.CitationVerified, File: "hold.cc", Line: 3, Role: lifetime.NodeHolder, CheckedAt: now, CheckerVersion: "m0"},
			{Status: lifetime.CitationVerified, File: "use.cc", Line: 7, Role: lifetime.NodeAccess, CheckedAt: now, CheckerVersion: "m0"},
		},
	})
	if err != nil {
		t.Fatalf("RecordStaticSupport: %v", err)
	}

	srv, err := New(st, Options{})
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Q1：auditor 自取并完成发现租约。
	item := claimItem(t, ts.URL, string(lifetime.RoleLifetimeAuditor), "auditor-e2e", taskID, string(lifetime.QueueUnitReadyDiscover))
	if item.QueueKind != lifetime.QueueUnitReadyDiscover {
		t.Fatalf("Q1 item = %+v", item)
	}
	if err := postToolOK(t, ts.URL, string(lifetime.RoleLifetimeAuditor), "auditor-e2e", taskID, lifetime.ToolComplete,
		map[string]any{"item_id": item.ItemID, "claim_id": item.ClaimID, "outcome_ref": "unit-examined"}); err != nil {
		t.Fatalf("complete Q1: %v", err)
	}

	// 发现写回：一个 F1 候选。
	candidateBody := map[string]any{
		"unit_id":    unitID,
		"support_id": supID,
		"package": map[string]any{
			"family": map[string]any{
				"family_id": "fam-e2e", "type_canonical": "Foo", "alloc_site": "foo.cc:10",
				"storage_class": "heap", "lifetime_form": "raw",
				"applicable_forms": []string{"F1"},
			},
			"domains": []map[string]any{
				{"domain_class_id": "d1", "kind": "thread", "instance_site": "t.cc:1", "serialization": "concurrent", "concurrency_degree": "N"},
				{"domain_class_id": "d2", "kind": "thread", "instance_site": "t.cc:2", "serialization": "concurrent", "concurrency_degree": "N"},
			},
			"domain_pair_id":    "pair-e2e",
			"pair_basis":        "logical",
			"domain_confidence": "high",
			"holders":           []map[string]any{{"site": "hold.cc:3", "form": "raw", "domain": "d1"}},
			"competing_accesses": []map[string]any{
				{"site": "use.cc:7", "domain": "d1", "guarded_by": ""},
				{"site": "use.cc:9", "domain": "d2", "guarded_by": ""},
			},
			"evidence_chain": []map[string]any{
				{"fact_id": "f1", "role": "lifetime_point", "file": "foo.cc", "line": 10, "symbol": "Foo::Foo"},
				{"fact_id": "f2", "role": "holder", "file": "hold.cc", "line": 3, "symbol": "holder"},
				{"fact_id": "f3", "role": "access", "file": "use.cc", "line": 7, "symbol": "refs--"},
				{"fact_id": "f4", "role": "access", "file": "use.cc", "line": 9, "symbol": "refs++"},
			},
			"synchronization": []map[string]any{
				{"primitive": "mutex", "scope": "obj", "protects": "Foo::refs", "file_line": "sync.cc:1"},
			},
			"lifetime_visibility": map[string]any{
				"holder_notification": "refcount",
				"q4_answer":           map[string]any{"value": "refcount", "status": "known"},
			},
			"ownership_answers": map[string]any{
				"q1_created_by":   map[string]any{"value": "caller", "file": "foo.cc", "line": 10},
				"q2_held_by":      map[string]any{"value": "refcount", "form": "raw", "domain": "d1", "file": "hold.cc", "line": 3},
				"q3_destroyed_by": map[string]any{"value": "last-dec", "domain": "d2", "file": "foo.cc", "line": 40},
				"q4":              map[string]any{"value": "refcount", "status": "known"},
			},
			"gap":                map[string]any{"description": "dec/read race", "window": "last-dec"},
			"trigger_hypothesis": map[string]any{"domains": []string{"d1", "d2"}, "interleaving": "inc/dec"},
			"primary_form":       "F1",
			"contract_status":    "known",
		},
	}
	if err := postToolOK(t, ts.URL, string(lifetime.RoleLifetimeAuditor), "auditor-e2e", taskID, lifetime.ToolRecordCandidate, candidateBody); err != nil {
		t.Fatalf("record_candidate: %v", err)
	}

	// 平台评估：S1/S2 → MaterializeAssessment 自动投影 Q2（R-6.9）。
	cands, err := st.ListCandidates(ctx, taskID)
	if err != nil || len(cands) != 1 {
		t.Fatalf("ListCandidates: n=%d err=%v", len(cands), err)
	}
	cand := cands[0]
	if err := postAssessOK(t, ts.URL, string(lifetime.RolePlanner), "planner-e2e", taskID, cand.CandidateID); err != nil {
		t.Fatalf("assess: %v", err)
	}

	// Q2：verifier 自取、续租、完成。
	item2 := claimItem(t, ts.URL, string(lifetime.RoleLifetimeVerifier), "verifier-e2e", taskID, string(lifetime.QueueCandidateReadyVerify))
	if item2.ItemRef != cand.CandidateID {
		t.Fatalf("Q2 item_ref = %q, want %q", item2.ItemRef, cand.CandidateID)
	}
	if err := postToolOK(t, ts.URL, string(lifetime.RoleLifetimeVerifier), "verifier-e2e", taskID, lifetime.ToolRenew,
		map[string]any{"item_id": item2.ItemID, "claim_id": item2.ClaimID, "lease_ttl_seconds": 120}); err != nil {
		t.Fatalf("renew Q2: %v", err)
	}
	if err := postToolOK(t, ts.URL, string(lifetime.RoleLifetimeVerifier), "verifier-e2e", taskID, lifetime.ToolComplete,
		map[string]any{"item_id": item2.ItemID, "claim_id": item2.ClaimID, "outcome_ref": "experiment-e2e"}); err != nil {
		t.Fatalf("complete Q2: %v", err)
	}

	logs, err := st.ClaimLog(ctx, taskID, 20)
	if err != nil {
		t.Fatalf("ClaimLog: %v", err)
	}
	if len(logs) < 5 {
		t.Fatalf("claim_log entries = %d, want >=5", len(logs))
	}
}

func claimItem(t *testing.T, base, role, session, taskID string, q string) lifetime.WorkItem {
	t.Helper()
	resp := postTool(t, base, role, session, taskID, lifetime.ToolClaim, map[string]any{"queue_kind": q, "lease_ttl_seconds": 120})
	var out struct {
		OK     bool              `json:"ok"`
		Result lifetime.WorkItem `json:"result"`
		Error  string            `json:"error"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("claim decode: %v (%s)", err, resp)
	}
	if !out.OK || out.Result.ItemID == "" {
		t.Fatalf("claim failed: %s", resp)
	}
	return out.Result
}

func postToolOK(t *testing.T, base, role, session, taskID, tool string, body any) error {
	t.Helper()
	resp := postTool(t, base, role, session, taskID, tool, body)
	var out ToolResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		return fmt.Errorf("decode: %w (%s)", err, resp)
	}
	if !out.OK {
		return fmt.Errorf("%s: %s (%s)", tool, out.Error, out.ErrorCode)
	}
	return nil
}

// postAssessOK 触发平台评估端点；成功时返回 nil。
func postAssessOK(t *testing.T, base, role, session, taskID, candidateID string) error {
	t.Helper()
	url := base + "/v1/tasks/" + taskID + "/candidates/" + candidateID + "/assess"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lifetime-Role", role)
	req.Header.Set("X-Lifetime-Session", session)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do assess: %v", err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(res.Body); err != nil {
		t.Fatalf("read assess: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("assess status=%d body=%s", res.StatusCode, buf.String())
	}
	var out ToolResponse
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		return fmt.Errorf("decode assess: %w (%s)", err, buf.String())
	}
	if !out.OK {
		return fmt.Errorf("assess: %s (%s)", out.Error, out.ErrorCode)
	}
	return nil
}

func postTool(t *testing.T, base, role, session, taskID, tool string, body any) []byte {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/v1/tasks/"+taskID+"/tools/"+tool, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lifetime-Role", role)
	req.Header.Set("X-Lifetime-Session", session)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(res.Body); err != nil {
		t.Fatalf("read: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", tool, res.StatusCode, buf.String())
	}
	return buf.Bytes()
}

func cleanupLifetimeTask(t *testing.T, st *store.Store, taskID string) {
	t.Helper()
	tables := []string{
		"lifetime_claim_log", "lifetime_work_queue", "lifetime_assessments",
		"lifetime_observations", "lifetime_experiments", "lifetime_candidates",
		"lifetime_static_support", "lifetime_no_risk", "lifetime_unknowns",
		"lifetime_new_leads", "lifetime_probes", "lifetime_reconciliation_findings",
		"lifetime_reconciliation_runs", "lifetime_coverage_units", "lifetime_work_packages",
		"lifetime_object_families", "lifetime_object_relations", "lifetime_domains",
		"lifetime_domain_pairs", "lifetime_lifetime_points", "lifetime_sync_primitives",
		"lifetime_callbacks", "lifetime_index_unknowns", "lifetime_index_runs", "lifetime_owner_journal",
		"lifetime_task_profiles", "lifetime_lineages",
	}
	for _, table := range tables {
		if _, err := st.DB().ExecContext(context.Background(), "DELETE FROM "+table+" WHERE task_id=$1", taskID); err != nil {
			t.Logf("cleanup %s: %v", table, err)
		}
	}
}
