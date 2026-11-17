package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/lifetime"
)

// testDSN 优先 LIFETIME_TEST_DSN，其次复用 ARTEX_PG_DSN。
func testDSN() string {
	if v := os.Getenv("LIFETIME_TEST_DSN"); v != "" {
		return v
	}
	return os.Getenv("ARTEX_PG_DSN")
}

// taskScopedTables 是所有带 task_id 的 lifetime 表；集成测试只清理本 task 行。
var taskScopedTables = []string{
	"lifetime_task_profiles",
	"lifetime_lineages",
	"lifetime_object_families",
	"lifetime_object_relations",
	"lifetime_domains",
	"lifetime_domain_pairs",
	"lifetime_lifetime_points",
	"lifetime_sync_primitives",
	"lifetime_callbacks",
	"lifetime_index_unknowns",
	"lifetime_index_runs",
	"lifetime_static_support",
	"lifetime_candidates",
	"lifetime_observations",
	"lifetime_experiments",
	"lifetime_assessments",
	"lifetime_work_packages",
	"lifetime_coverage_units",
	"lifetime_no_risk",
	"lifetime_unknowns",
	"lifetime_new_leads",
	"lifetime_work_queue",
	"lifetime_claim_log",
	"lifetime_owner_journal",
	"lifetime_reconciliation_findings",
	"lifetime_reconciliation_runs",
	"lifetime_probes",
}

func cleanupTask(t *testing.T, st *Store, taskID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, table := range taskScopedTables {
		if _, err := st.DB().ExecContext(ctx, `DELETE FROM `+table+` WHERE task_id = $1`, taskID); err != nil {
			t.Logf("cleanup %s for %s: %v", table, taskID, err)
		}
	}
	// area lease 没有 task_id；测试用 taskID 前缀命名 area。
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM lifetime_area_leases WHERE area_id LIKE $1`, taskID+"%"); err != nil {
		t.Logf("cleanup area_leases for %s: %v", taskID, err)
	}
}

// TestStoreIntegration 需要真实 PostgreSQL：
//
//	LIFETIME_TEST_DSN=postgres://user:pass@127.0.0.1:5432/db?sslmode=disable go test ./lifetime/store/
func TestStoreIntegration(t *testing.T) {
	dsn := testDSN()
	if dsn == "" {
		t.Skip("set LIFETIME_TEST_DSN or ARTEX_PG_DSN to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	st, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	base := time.Now().UnixNano()
	taskID := fmt.Sprintf("test-%d", base)
	taskStats := fmt.Sprintf("test-%d-stats", base)
	defer cleanupTask(t, st, taskID)
	defer cleanupTask(t, st, taskStats)

	// --- schema 应用（表 + claim 视图） -----------------------------------
	t.Run("Schema", func(t *testing.T) {
		var table, view *string
		if err := st.DB().QueryRowContext(ctx, `SELECT to_regclass('public.lifetime_candidates')::text`).Scan(&table); err != nil {
			t.Fatalf("to_regclass(table): %v", err)
		}
		if table == nil || *table == "" {
			t.Fatal("lifetime_candidates was not created")
		}
		if err := st.DB().QueryRowContext(ctx, `SELECT to_regclass('public.lifetime_lifetime_claims')::text`).Scan(&view); err != nil {
			t.Fatalf("to_regclass(view): %v", err)
		}
		if view == nil || *view == "" {
			t.Fatal("lifetime_lifetime_claims view was not created")
		}
	})

	// --- lineage ----------------------------------------------------------
	lineageIn := lifetime.LineageInput{
		SourceManifestHash:         "src-" + taskID,
		IndexHash:                  "idx-" + taskID,
		BuildID:                    "build-1",
		DriverSpecHash:             "driver-1",
		PolicyHash:                 "policy-1",
		ProfileVersion:             "1.8",
		HarnessVersion:             "h-1",
		InstrumentationProfileHash: "instr-1",
	}
	var lineageID string
	t.Run("Lineage", func(t *testing.T) {
		id, err := st.CreateLineage(ctx, taskID, lineageIn)
		if err != nil {
			t.Fatalf("CreateLineage: %v", err)
		}
		if id == "" {
			t.Fatal("empty lineage id")
		}
		lineageID = id

		// 幂等：重复创建返回同一个 id。
		again, err := st.CreateLineage(ctx, taskID, lineageIn)
		if err != nil || again != id {
			t.Fatalf("CreateLineage not idempotent: id=%q again=%q err=%v", id, again, err)
		}

		got, gotTask, err := st.GetLineage(ctx, id)
		if err != nil {
			t.Fatalf("GetLineage: %v", err)
		}
		if gotTask != taskID {
			t.Fatalf("GetLineage task_id = %q, want %q", gotTask, taskID)
		}
		if *got != lineageIn {
			t.Fatalf("GetLineage mismatch:\n got %+v\nwant %+v", *got, lineageIn)
		}
	})

	// --- TaskProfile ------------------------------------------------------
	t.Run("TaskProfile", func(t *testing.T) {
		p := lifetime.DefaultTaskProfile()
		p.Name = "integration"
		p.Version = "1.8"
		hash, err := st.RegisterTaskProfile(ctx, taskID, p)
		if err != nil {
			t.Fatalf("RegisterTaskProfile: %v", err)
		}
		if hash == "" || hash != p.PolicyHash {
			t.Fatalf("policy hash = %q, profile.PolicyHash = %q", hash, p.PolicyHash)
		}
	})

	// --- candidate --------------------------------------------------------
	var candidateID string
	t.Run("Candidate", func(t *testing.T) {
		cand := completeCandidate()
		id, err := st.RecordCandidate(ctx, taskID, cand)
		if err != nil {
			t.Fatalf("RecordCandidate: %v", err)
		}
		candidateID = id

		got, err := st.GetCandidate(ctx, id)
		if err != nil {
			t.Fatalf("GetCandidate: %v", err)
		}
		if got.CandidateKey == "" || got.MechanismKey == "" {
			t.Fatalf("candidate keys not computed: key=%q mech=%q", got.CandidateKey, got.MechanismKey)
		}
		if got.DomainPairID != cand.DomainPairID {
			t.Fatalf("domain_pair_id = %q, want %q", got.DomainPairID, cand.DomainPairID)
		}
		if got.PrimaryForm != lifetime.FormDanglingThis {
			t.Fatalf("primary_form = %q", got.PrimaryForm)
		}
		if got.Budget.Remaining != 3 || got.Budget.Used != 1 {
			t.Fatalf("budget = %+v", got.Budget)
		}

		// R-2.1：domain_pair_id 必填。
		bad := completeCandidate()
		bad.DomainPairID = ""
		if _, err := st.RecordCandidate(ctx, taskID, bad); err == nil {
			t.Fatal("RecordCandidate accepted empty domain_pair_id")
		}

		// candidate 写入应把对应覆盖行推进为 candidate_open。
		cu, err := st.GetCoverageForTask(ctx, taskID, got.UnitID)
		if err != nil {
			t.Fatalf("GetCoverage: %v", err)
		}
		if cu.Status != lifetime.CoverageCandidateOpen {
			t.Fatalf("coverage status = %q, want candidate_open", cu.Status)
		}

		// 平台可以更新预算。
		if err := st.UpdateCandidateBudget(ctx, id, 2, 2); err != nil {
			t.Fatalf("UpdateCandidateBudget: %v", err)
		}
		got, err = st.GetCandidate(ctx, id)
		if err != nil {
			t.Fatalf("GetCandidate after budget: %v", err)
		}
		if got.Budget.Remaining != 2 || got.Budget.Used != 2 {
			t.Fatalf("budget after update = %+v", got.Budget)
		}
	})

	// --- claim 原子性 / claim_log ----------------------------------------
	var winner *lifetime.WorkItem
	t.Run("ClaimAtomicity", func(t *testing.T) {
		itemID, err := st.ProjectWorkItem(ctx, taskID, lifetime.WorkItem{
			QueueKind: lifetime.QueueUnitReadyDiscover,
			ItemRef:   "pkg-race-1",
			DedupKey:  "q1:pkg-race-1",
		})
		if err != nil {
			t.Fatalf("ProjectWorkItem: %v", err)
		}
		// 幂等：同 dedup_key 返回同一 item。
		again, err := st.ProjectWorkItem(ctx, taskID, lifetime.WorkItem{
			QueueKind: lifetime.QueueUnitReadyDiscover,
			ItemRef:   "pkg-race-1",
			DedupKey:  "q1:pkg-race-1",
		})
		if err != nil || again != itemID {
			t.Fatalf("ProjectWorkItem not idempotent: id=%q again=%q err=%v", itemID, again, err)
		}

		const workers = 2
		var (
			wg        sync.WaitGroup
			mu        sync.Mutex
			winners   []*lifetime.WorkItem
			claimErrs []error
		)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				it, err := st.Claim(ctx, taskID, lifetime.QueueUnitReadyDiscover,
					fmt.Sprintf("sess-%d", i), time.Minute)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					claimErrs = append(claimErrs, err)
					return
				}
				if it != nil {
					winners = append(winners, it)
				}
			}(i)
		}
		wg.Wait()
		if len(claimErrs) != 0 {
			t.Fatalf("Claim errors: %v", claimErrs)
		}
		if len(winners) != 1 {
			t.Fatalf("concurrent claim winners = %d, want 1", len(winners))
		}
		winner = winners[0]

		if winner.State != lifetime.WorkItemClaimed || winner.ClaimID == "" ||
			winner.ClaimedBySession == "" {
			t.Fatalf("claimed item fields = %+v", winner)
		}
		if winner.LeaseExpiresAt.IsZero() || !winner.LeaseExpiresAt.After(time.Now()) {
			t.Fatalf("lease_expires_at = %v", winner.LeaseExpiresAt)
		}

		// 队列空后 claim 返回 nil,nil。
		empty, err := st.Claim(ctx, taskID, lifetime.QueueUnitReadyDiscover, "sess-x", time.Minute)
		if err != nil || empty != nil {
			t.Fatalf("empty claim = %+v, %v", empty, err)
		}

		logs, err := st.ClaimLog(ctx, taskID, 50)
		if err != nil {
			t.Fatalf("ClaimLog: %v", err)
		}
		found := false
		for _, l := range logs {
			if l.Action == "claim" && l.ClaimID == winner.ClaimID &&
				l.ItemID == winner.ItemID && l.SessionID == winner.ClaimedBySession {
				found = true
			}
		}
		if !found {
			t.Fatalf("claim_log missing claim entry for %+v; logs=%+v", winner, logs)
		}

		// renew 成功并写日志。
		renewed, err := st.Renew(ctx, winner.ItemID, winner.ClaimID, winner.ClaimedBySession, 2*time.Minute)
		if err != nil {
			t.Fatalf("Renew: %v", err)
		}
		if renewed == nil || !renewed.LeaseExpiresAt.After(winner.LeaseExpiresAt) {
			t.Fatalf("renew did not extend lease: before=%v after=%+v", winner.LeaseExpiresAt, renewed)
		}

		// 错误 claim 不能续租。
		if _, err := st.Renew(ctx, winner.ItemID, "wrong-claim", winner.ClaimedBySession, time.Minute); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("Renew wrong claim err = %v, want ErrLeaseLost", err)
		}

		// complete 幂等（同 claim 重复调用返回 nil）。
		if err := st.Complete(ctx, winner.ItemID, winner.ClaimID, winner.ClaimedBySession, "outcome-1"); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if err := st.Complete(ctx, winner.ItemID, winner.ClaimID, winner.ClaimedBySession, "outcome-1"); err != nil {
			t.Fatalf("Complete not idempotent: %v", err)
		}

		logs, err = st.ClaimLog(ctx, taskID, 50)
		if err != nil {
			t.Fatalf("ClaimLog after complete: %v", err)
		}
		actions := map[string]bool{}
		for _, l := range logs {
			if l.ClaimID == winner.ClaimID {
				actions[l.Action] = true
			}
		}
		for _, want := range []string{"claim", "renew", "complete"} {
			if !actions[want] {
				t.Fatalf("claim_log missing action %q; actions=%v", want, actions)
			}
		}

		// 过期租约：ApplyExpire → attempts++ 回 ready；attempts>=3 → blocked。
		expID, err := st.ProjectWorkItem(ctx, taskID, lifetime.WorkItem{
			QueueKind: lifetime.QueueClaimReadyReview, ItemRef: "assessment-exp", DedupKey: "q3:expire",
		})
		if err != nil {
			t.Fatalf("ProjectWorkItem(expire): %v", err)
		}
		for attempt := 1; attempt <= 3; attempt++ {
			it, err := st.Claim(ctx, taskID, lifetime.QueueClaimReadyReview, "sess-exp", time.Minute)
			if err != nil || it == nil || it.ItemID != expID {
				t.Fatalf("expire cycle %d claim = %+v, %v", attempt, it, err)
			}
			if _, err := st.DB().ExecContext(ctx,
				`UPDATE lifetime_work_queue SET lease_expires_at = now() - interval '1 second' WHERE item_id = $1`,
				expID); err != nil {
				t.Fatalf("force lease expiry: %v", err)
			}
			n, err := st.ExpireLeases(ctx, taskID)
			if err != nil || n != 1 {
				t.Fatalf("ExpireLeases cycle %d = %d, %v", attempt, n, err)
			}
		}
		var (
			expState    string
			expAttempts int
		)
		if err := st.DB().QueryRowContext(ctx,
			`SELECT state, attempts FROM lifetime_work_queue WHERE item_id = $1`, expID).
			Scan(&expState, &expAttempts); err != nil {
			t.Fatalf("read expired item: %v", err)
		}
		if expState != string(lifetime.WorkItemBlocked) || expAttempts != 3 {
			t.Fatalf("expired item state=%q attempts=%d, want blocked/3", expState, expAttempts)
		}

		// release：claim 失配拒绝；正确 release 后回 ready 且 attempts 不变。
		relID, err := st.ProjectWorkItem(ctx, taskID, lifetime.WorkItem{
			QueueKind: lifetime.QueueConfirmedReadyReport, ItemRef: "finding-rel", DedupKey: "q4:release",
		})
		if err != nil {
			t.Fatalf("ProjectWorkItem(release): %v", err)
		}
		relIt, err := st.Claim(ctx, taskID, lifetime.QueueConfirmedReadyReport, "sess-rel", time.Minute)
		if err != nil || relIt == nil || relIt.ItemID != relID {
			t.Fatalf("release claim = %+v, %v", relIt, err)
		}
		if err := st.Release(ctx, relID, "wrong-claim", "sess-rel", "oops"); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("Release wrong claim err = %v, want ErrLeaseLost", err)
		}
		if err := st.Release(ctx, relID, relIt.ClaimID, "sess-rel", "preempted"); err != nil {
			t.Fatalf("Release: %v", err)
		}
		relIt2, err := st.Claim(ctx, taskID, lifetime.QueueConfirmedReadyReport, "sess-rel", time.Minute)
		if err != nil || relIt2 == nil || relIt2.ItemID != relID || relIt2.Attempts != 0 {
			t.Fatalf("re-claim after release = %+v, %v", relIt2, err)
		}
		if err := st.Complete(ctx, relID, relIt2.ClaimID, "sess-rel", "released-outcome"); err != nil {
			t.Fatalf("Complete after release: %v", err)
		}
	})

	// --- coverage stats ---------------------------------------------------
	t.Run("CoverageStats", func(t *testing.T) {
		statuses := []lifetime.CoverageStatus{
			lifetime.CoverageNoRisk, lifetime.CoverageNoRisk,
			lifetime.CoverageVerified,
			lifetime.CoverageRefuted, lifetime.CoverageRefuted, lifetime.CoverageRefuted,
			lifetime.CoverageUnexamined, lifetime.CoverageUnexamined,
			lifetime.CoverageUnexamined, lifetime.CoverageUnexamined,
		}
		units := make([]lifetime.CoverageUnit, 0, len(statuses))
		for i := range statuses {
			units = append(units, lifetime.NewCoverageUnit(
				lifetime.UnitKindFamily,
				fmt.Sprintf("stat-fam-%d", i), "", "stat-pair", lifetime.FormRefcountRace,
				"stat-lineage",
			))
		}
		if err := st.InitCoverage(ctx, taskStats, "stat-lineage", units, nil); err != nil {
			t.Fatalf("InitCoverage: %v", err)
		}
		// 前 6 行更新为已结论状态；后 4 行保持 unexamined。
		for i := 0; i < 6; i++ {
			if err := st.UpdateCoverageStatusForTask(ctx, taskStats, units[i].UnitID, statuses[i],
				"test reason", "evidence-"+units[i].UnitID, "", "self_asserted", "stat-lineage"); err != nil {
				t.Fatalf("UpdateCoverageStatus %s: %v", units[i].UnitID, err)
			}
		}

		stats, err := st.CoverageStats(ctx, taskStats, 0)
		if err != nil {
			t.Fatalf("CoverageStats: %v", err)
		}
		if stats.EnumeratedUnits != 10 || stats.ConcludedUnits != 6 ||
			stats.RiskClosedUnits != 5 || stats.FindingUnits != 1 {
			t.Fatalf("unexpected counts: %+v", stats)
		}
		// lifetime.ComputeCoverageStats 以百分数（0–100）返回 *_pct 字段。
		if math.Abs(stats.CoveragePct-60) > 1e-9 {
			t.Fatalf("coverage_pct = %v, want 60", stats.CoveragePct)
		}
		if math.Abs(stats.RiskClosedPctAll-50) > 1e-9 {
			t.Fatalf("risk_closed_pct_all = %v, want 50", stats.RiskClosedPctAll)
		}

		// 账本自然键唯一：同一 unit 重复初始化不应新增行。
		if err := st.InitCoverage(ctx, taskStats, "stat-lineage", units, nil); err != nil {
			t.Fatalf("InitCoverage re-run: %v", err)
		}
		var count int
		if err := st.DB().QueryRowContext(ctx,
			`SELECT count(*) FROM lifetime_coverage_units WHERE task_id = $1`, taskStats).Scan(&count); err != nil {
			t.Fatalf("count coverage: %v", err)
		}
		if count != 10 {
			t.Fatalf("coverage rows = %d after re-init, want 10", count)
		}
	})

	// --- 其余写路径（索引 / 发现 / evidence / owner / 对账） -----------------
	t.Run("WritePaths", func(t *testing.T) {
		// IngestIndex：每类元素一条；重复 ingest 幂等（unknowns 按内容去重）。
		idx := lifetime.IndexOutput{
			IndexHash:      "idx-" + taskID,
			IndexQuality:   "full",
			TypeResolution: "exact",
			ObjectFamilies: []lifetime.ObjectFamily{{
				FamilyID: "fam-indexed", TypeCanonical: "Bar", AllocSite: "bar.cc:1",
				StorageClass: lifetime.StorageHeap, LifetimeForm: lifetime.LifetimeRaw,
				ApplicableForms: []lifetime.Form{lifetime.FormRefcountRace},
			}},
			ObjectRelations: []lifetime.ObjectRelation{{
				RelationID: "rel-1", Type: "owns", SubjectFamilyID: "fam-indexed",
				ObjectFamilyID: "fam-test", Site: "r.cc:2",
				Confidence: lifetime.ConfidenceHigh, IndexHash: "idx-" + taskID,
			}},
			Domains: []lifetime.Domain{{
				DomainClassID: "dc-idx", Kind: lifetime.DomainThread, InstanceSite: "t.cc:3",
				Serialization: lifetime.SerializationConcurrent, ConcurrencyDegree: lifetime.ConcurrencyN,
			}},
			DomainPairs: []lifetime.DomainPair{{
				PairClassID: "dp-idx", DomainA: "dc-idx", DomainB: "dc-idx",
				DomainConfidence: lifetime.DomainConfidenceHigh,
				PairBasisOptions: []lifetime.PairBasis{lifetime.PairBasisExecution},
			}},
			LifetimePoints: []lifetime.LifetimePoint{{
				FamilyID: "fam-indexed", Site: "bar.cc:1",
				Role: lifetime.NodeLifetimePoint, SourceRef: "bar.cc",
			}},
			SyncPrimitives: []lifetime.SyncPrimitive{{
				Primitive: "mutex", Scope: "obj", Protects: "Bar::x", FileLine: "bar.cc:5",
			}},
			Callbacks: []lifetime.CallbackSpec{{
				RegisterSite: "reg.cc:1", Capture: "raw_this", SourceRef: "reg.cc",
			}},
			Unknowns: []lifetime.IndexUnknown{{
				Scope: "mod:thirdparty", Reason: lifetime.UnknownThirdParty, Detail: "vendored",
			}},
		}
		for i := 0; i < 2; i++ {
			if err := st.IngestIndex(ctx, taskID, lineageID, idx); err != nil {
				t.Fatalf("IngestIndex run %d: %v", i, err)
			}
		}
		fams, err := st.ListFamilies(ctx, taskID)
		if err != nil {
			t.Fatalf("ListFamilies: %v", err)
		}
		if len(fams) != 1 {
			t.Fatalf("families = %d, want 1", len(fams))
		}
		var unknownCount int
		if err := st.DB().QueryRowContext(ctx,
			`SELECT count(*) FROM lifetime_index_unknowns WHERE task_id=$1`, taskID).Scan(&unknownCount); err != nil {
			t.Fatalf("count index unknowns: %v", err)
		}
		if unknownCount != 1 {
			t.Fatalf("index unknowns = %d after duplicate ingest, want 1", unknownCount)
		}

		// 静态支持（R-8.11 chain 必须含 lifetime_point + access）。
		supID, err := st.RecordStaticSupport(ctx, taskID, lifetime.StaticSupport{
			UnitID:    "unit-static",
			IndexHash: "idx-" + taskID,
			CreatedBy: "auditor",
			Facts: []lifetime.EvidenceNode{{
				FactID: "f1", Role: lifetime.NodeLifetimePoint, File: "a.cc", Line: 10, Symbol: "Foo",
			}},
			Chain: []lifetime.EvidenceNode{
				{FactID: "f1", Role: lifetime.NodeLifetimePoint, File: "a.cc", Line: 10, Symbol: "Foo"},
				{FactID: "f2", Role: lifetime.NodeAccess, File: "acc.cc", Line: 4, Symbol: "use"},
			},
		})
		if err != nil || supID == "" {
			t.Fatalf("RecordStaticSupport: id=%q err=%v", supID, err)
		}
		if _, err := st.RecordStaticSupport(ctx, taskID, lifetime.StaticSupport{
			Chain: []lifetime.EvidenceNode{{Role: lifetime.NodeLifetimePoint, File: "a.cc", Line: 1}},
		}); err == nil {
			t.Fatal("RecordStaticSupport accepted chain without access node")
		}

		// no_risk：serialized + sync + verified citation，写入后覆盖行闭合。
		nrID, err := st.RecordNoRisk(ctx, taskID, lifetime.NoRisk{
			FamilyID:     "fam-nr",
			DomainPairID: "dp-nr",
			Form:         lifetime.FormRefcountRace,
			Arguments: []lifetime.NoRiskArgument{{
				ArgumentType: lifetime.NoRiskSerialized,
				CoversForms:  []lifetime.Form{lifetime.FormRefcountRace},
				Sync: []lifetime.SyncPrimitive{{
					Primitive: "mutex", Scope: "obj", Protects: "Foo::x", FileLine: "s.cc:1",
				}},
				Proof: lifetime.Proof{Kind: "source", Refs: []string{"s.cc:1"}},
				CitationCheck: []lifetime.CitationCheck{{
					Status: lifetime.CitationVerified, File: "s.cc", Line: 1, Role: lifetime.NodeSyncGuard,
				}},
			}},
			CoversCurrentForm: true,
			ReviewStatus:      "self_asserted",
			CreatedBy:         "auditor",
		})
		if err != nil {
			t.Fatalf("RecordNoRisk: %v", err)
		}
		nrUnitID := lifetime.UnitID(lifetime.UnitKindFamily, "fam-nr", "", "dp-nr", lifetime.FormRefcountRace)
		nrUnit, err := st.GetCoverageForTask(ctx, taskID, nrUnitID)
		if err != nil {
			t.Fatalf("GetCoverage(no_risk): %v", err)
		}
		if nrUnit.Status != lifetime.CoverageNoRisk || nrUnit.EvidenceRef != nrID {
			t.Fatalf("no_risk coverage = %+v", nrUnit)
		}

		// record_no_risk(unit_id, ...)：只给 unit_id，平台从覆盖账本解析
		// family_id / domain_pair_id / form（§16.2）。
		resolveUnitID := lifetime.UnitID(lifetime.UnitKindFamily, "fam-resolve", "", "dp-resolve", lifetime.FormRefcountRace)
		resolveUnit := lifetime.NewCoverageUnit(lifetime.UnitKindFamily, "fam-resolve", "", "dp-resolve", lifetime.FormRefcountRace, "resolve-lineage")
		resolveUnit.UnitID = resolveUnitID
		if err := st.InitCoverage(ctx, taskID, "resolve-lineage", []lifetime.CoverageUnit{resolveUnit}, nil); err != nil {
			t.Fatalf("InitCoverage(resolve): %v", err)
		}
		nrResolveID, err := st.RecordNoRisk(ctx, taskID, lifetime.NoRisk{
			UnitID: resolveUnitID,
			Form:   lifetime.FormRefcountRace,
			Arguments: []lifetime.NoRiskArgument{{
				ArgumentType: lifetime.NoRiskSerialized,
				CoversForms:  []lifetime.Form{lifetime.FormRefcountRace},
				Sync: []lifetime.SyncPrimitive{{
					Primitive: "mutex", Scope: "obj", Protects: "Foo::refs", FileLine: "s.cc:1",
				}},
				Proof: lifetime.Proof{Kind: "source", Refs: []string{"s.cc:1"}},
				CitationCheck: []lifetime.CitationCheck{{
					Status: lifetime.CitationVerified, File: "s.cc", Line: 1, Role: lifetime.NodeSyncGuard,
				}},
			}},
			CoversCurrentForm: true,
			CreatedBy:         "auditor",
		})
		if err != nil {
			t.Fatalf("RecordNoRisk by unit_id: %v", err)
		}
		resolved, err := st.GetCoverageForTask(ctx, taskID, resolveUnitID)
		if err != nil {
			t.Fatalf("GetCoverage(resolve): %v", err)
		}
		if resolved.Status != lifetime.CoverageNoRisk || resolved.EvidenceRef != nrResolveID ||
			resolved.FamilyID != "fam-resolve" || resolved.DomainPairID != "dp-resolve" {
			t.Fatalf("resolved unit = %+v", resolved)
		}
		// 未知 unit_id 必须拒绝。
		if _, err := st.RecordNoRisk(ctx, taskID, lifetime.NoRisk{
			UnitID: "unit-missing", Form: lifetime.FormRefcountRace,
			Arguments:         []lifetime.NoRiskArgument{{ArgumentType: lifetime.NoRiskSerialized}},
			CoversCurrentForm: true,
		}); err == nil {
			t.Fatal("RecordNoRisk accepted unknown unit_id")
		}

		// 缺 sync 的 serialized 论证必须被拒绝。
		if _, err := st.RecordNoRisk(ctx, taskID, lifetime.NoRisk{
			FamilyID: "fam-nr2", DomainPairID: "dp-nr2", Form: lifetime.FormRefcountRace,
			Arguments:         []lifetime.NoRiskArgument{{ArgumentType: lifetime.NoRiskSerialized}},
			CoversCurrentForm: true,
		}); err == nil {
			t.Fatal("RecordNoRisk accepted serialized without sync")
		}

		// unknown / new_lead。
		if _, err := st.RecordUnknown(ctx, taskID, lifetime.UnknownRecord{
			Scope: "module:x", Reason: lifetime.UnknownThirdParty, Detail: "closed source",
		}); err != nil {
			t.Fatalf("RecordUnknown: %v", err)
		}
		if _, err := st.RecordUnknown(ctx, taskID, lifetime.UnknownRecord{
			Scope: "module:x", Reason: "bogus",
		}); err == nil {
			t.Fatal("RecordUnknown accepted invalid reason")
		}
		if _, err := st.RecordNewLead(ctx, taskID, lifetime.NewLead{
			SourceEvent: "race_lead", FamilyID: "fam-indexed", Reason: "suspicious unlock",
		}); err != nil {
			t.Fatalf("RecordNewLead: %v", err)
		}

		// request_probe 投影 Q2(mode=probe)。
		if _, err := st.RequestProbe(ctx, taskID, candidateID, "callback vs destroy?", "TSan UAF"); err != nil {
			t.Fatalf("RequestProbe: %v", err)
		}
		readyQ2, err := st.ListReady(ctx, taskID, lifetime.QueueCandidateReadyVerify, 10)
		if err != nil {
			t.Fatalf("ListReady(Q2): %v", err)
		}
		probeFound := false
		for _, it := range readyQ2 {
			if it.DedupKey == "Q2:probe:"+candidateID && it.ItemRef == candidateID {
				probeFound = true
			}
		}
		if !probeFound {
			t.Fatalf("probe Q2 item not projected: %+v", readyQ2)
		}

		// evidence：append-only，重复主键报错；状态列可单独更新。
		now := time.Now().UTC()
		obs := lifetime.Observation{
			ObservationID: "obs-" + taskID, ExperimentID: "exp-" + taskID,
			CandidateID: candidateID, EventType: lifetime.EventMemory, Tool: lifetime.ToolASan,
			Platform: lifetime.PlatformNative, Fidelity: lifetime.FidelityComponent, CreatedAt: now,
		}
		if err := st.AppendObservation(ctx, taskID, obs); err != nil {
			t.Fatalf("AppendObservation: %v", err)
		}
		if err := st.AppendObservation(ctx, taskID, obs); err == nil {
			t.Fatal("AppendObservation accepted duplicate primary key")
		}
		exp := lifetime.Experiment{
			ExperimentID: "exp-" + taskID, CandidateID: candidateID, LineageID: lineageID,
			LineageRole: lifetime.LineageRoleClaim, Kind: lifetime.ExperimentKindCandidate,
			State: lifetime.ExperimentCompleted, StartedAt: now, FinishedAt: now,
		}
		if err := st.AppendExperiment(ctx, taskID, exp); err != nil {
			t.Fatalf("AppendExperiment: %v", err)
		}
		if err := st.AppendExperiment(ctx, taskID, exp); err == nil {
			t.Fatal("AppendExperiment accepted duplicate primary key")
		}
		asm := lifetime.Assessment{
			AssessmentID: "asm-" + taskID, CandidateID: candidateID, CandidateKey: "ck-test",
			LineageID: lineageID, LineageRole: lifetime.LineageRoleClaim,
			Level: lifetime.LevelS2, Confidence: lifetime.ConfidenceHigh,
			Status: lifetime.AssessmentOpen, DerivedAt: now,
		}
		if err := st.MaterializeAssessment(ctx, taskID, asm); err != nil {
			t.Fatalf("MaterializeAssessment: %v", err)
		}
		if err := st.MaterializeAssessment(ctx, taskID, asm); err == nil {
			t.Fatal("MaterializeAssessment accepted duplicate assessment_id (must stay append-only)")
		}
		observed, err := st.ListObservations(ctx, candidateID)
		if err != nil || len(observed) == 0 {
			t.Fatalf("ListObservations = %d, err=%v", len(observed), err)
		}
		exps, err := st.ListExperiments(ctx, candidateID)
		if err != nil || len(exps) == 0 {
			t.Fatalf("ListExperiments = %d, err=%v", len(exps), err)
		}
		assessments, err := st.ListAssessments(ctx, taskID)
		if err != nil || len(assessments) == 0 {
			t.Fatalf("ListAssessments = %d, err=%v", len(assessments), err)
		}
		if err := st.MarkAssessmentSuperseded(ctx, asm.AssessmentID); err != nil {
			t.Fatalf("MarkAssessmentSuperseded: %v", err)
		}
		assessments, err = st.ListAssessments(ctx, taskID)
		if err != nil {
			t.Fatalf("ListAssessments after supersede: %v", err)
		}
		if assessments[0].Status != lifetime.AssessmentSuperseded {
			t.Fatalf("assessment status = %q, want superseded", assessments[0].Status)
		}
		if err := st.MarkAssessmentStale(ctx, asm.AssessmentID, true); err != nil {
			t.Fatalf("MarkAssessmentStale: %v", err)
		}

		// owner area lease + journal。
		areaID := taskID + "-area"
		if err := st.AcquireArea(ctx, areaID, "owner-a", time.Minute); err != nil {
			t.Fatalf("AcquireArea: %v", err)
		}
		if err := st.AcquireArea(ctx, areaID, "owner-b", time.Minute); !errors.Is(err, ErrAreaOwned) {
			t.Fatalf("AcquireArea other session err = %v, want ErrAreaOwned", err)
		}
		if err := st.AcquireArea(ctx, areaID, "owner-a", time.Minute); err != nil {
			t.Fatalf("AcquireArea renew: %v", err)
		}
		if _, err := st.AppendJournal(ctx, taskID, areaID, lifetime.OwnerJournalEntry{
			OwnerID: "owner-a", Kind: lifetime.JournalDecision, Text: "investigated fam-indexed",
			Refs: []string{"fam-indexed"}, Confidence: lifetime.ConfidenceHigh,
			SourceHash: "src-1", IndexHash: "idx-1",
		}); err != nil {
			t.Fatalf("AppendJournal: %v", err)
		}
		entries, err := st.ListJournal(ctx, areaID, false)
		if err != nil || len(entries) != 1 {
			t.Fatalf("ListJournal = %d, err=%v", len(entries), err)
		}
		staleN, err := st.MarkJournalStale(ctx, areaID, "src-2", "idx-2")
		if err != nil || staleN != 1 {
			t.Fatalf("MarkJournalStale = %d, err=%v", staleN, err)
		}
		entries, err = st.ListJournal(ctx, areaID, false)
		if err != nil || len(entries) != 0 {
			t.Fatalf("ListJournal(fresh) = %d, err=%v", len(entries), err)
		}
		all, err := st.ListJournal(ctx, areaID, true)
		if err != nil || len(all) != 1 {
			t.Fatalf("ListJournal(includeStale) = %d, err=%v", len(all), err)
		}
		if err := st.ReleaseArea(ctx, areaID, "owner-a"); err != nil {
			t.Fatalf("ReleaseArea: %v", err)
		}
		if err := st.ReleaseArea(ctx, areaID, "owner-a"); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("ReleaseArea twice err = %v, want ErrLeaseLost", err)
		}

		// 对账：snapshot 装载 + open dedup_key 去重。
		snap, err := st.SnapshotForReconcile(ctx, taskID)
		if err != nil {
			t.Fatalf("SnapshotForReconcile: %v", err)
		}
		if snap.TaskID != taskID || snap.LineageID != lineageID || !snap.IndexAvailable {
			t.Fatalf("snapshot header = %+v", snap)
		}
		if len(snap.Candidates) != 1 || len(snap.EnumeratedUnits) < 2 || snap.FindingExists == nil {
			t.Fatalf("snapshot content: candidates=%d units=%d", len(snap.Candidates), len(snap.EnumeratedUnits))
		}
		run := lifetime.ReconciliationRun{
			SweepID: "sweep-" + taskID, Mode: "incremental", Checks: 12,
			Findings: 2, Repairs: 1, StartedAt: now, FinishedAt: now,
		}
		if err := st.SaveReconciliation(ctx, taskID, run, []lifetime.ReconciliationFinding{
			{
				ReconID: "recon-a-" + taskID, CheckID: "C1", Severity: lifetime.SeverityMedium,
				SubjectKind: "unit", SubjectRef: "u1", Action: lifetime.ActionCreateRepairTask,
				Status: lifetime.ReconOpen, DedupKey: "dedup-shared",
			},
			{
				ReconID: "recon-b-" + taskID, CheckID: "C7", Severity: lifetime.SeverityHigh,
				SubjectKind: "queue", SubjectRef: "i1", Action: lifetime.ActionRequeue,
				Status: lifetime.ReconOpen, DedupKey: "dedup-shared",
			},
		}); err != nil {
			t.Fatalf("SaveReconciliation: %v", err)
		}
		openFindings, err := st.ListReconciliationFindings(ctx, taskID, "open")
		if err != nil {
			t.Fatalf("ListReconciliationFindings: %v", err)
		}
		if len(openFindings) != 1 {
			t.Fatalf("open findings = %d, want 1 (dedup_key must suppress the second)", len(openFindings))
		}
		snap, err = st.SnapshotForReconcile(ctx, taskID)
		if err != nil {
			t.Fatalf("SnapshotForReconcile after save: %v", err)
		}
		if len(snap.ExistingDedupKeys) != 1 || snap.ExistingDedupKeys[0] != "dedup-shared" {
			t.Fatalf("ExistingDedupKeys = %v", snap.ExistingDedupKeys)
		}
	})
}

// completeCandidate 构造一个满足 R-8.12/R-8.16 的 F2 候选。
func completeCandidate() lifetime.Candidate {
	return lifetime.Candidate{
		SupportID: "sup-test",
		Budget:    lifetime.CandidateBudget{Remaining: 3, Used: 1},
		CandidatePackage: lifetime.CandidatePackage{
			Family: lifetime.ObjectFamily{
				FamilyID:           "fam-test",
				TypeCanonical:      "Foo",
				AllocSite:          "a.cc:1",
				StorageClass:       lifetime.StorageHeap,
				LifetimeForm:       lifetime.LifetimeRaw,
				ApplicableForms:    []lifetime.Form{lifetime.FormDanglingThis},
				Confidence:         lifetime.ConfidenceHigh,
				IdentityConfidence: lifetime.ConfidenceHigh,
			},
			Domains: []lifetime.Domain{{
				DomainClassID:     "dc-test",
				Kind:              lifetime.DomainThread,
				InstanceSite:      "b.cc:2",
				Serialization:     lifetime.SerializationConcurrent,
				ConcurrencyDegree: lifetime.ConcurrencyN,
			}},
			DomainPairID:     "dp-test",
			PairBasis:        lifetime.PairBasisExecution,
			DomainConfidence: lifetime.DomainConfidenceHigh,
			Holders: []lifetime.Holder{{
				Site: "h.cc:3", Form: "raw", Domain: "d1",
			}},
			CompetingAccesses: []lifetime.Access{{
				Site: "acc.cc:4", Domain: "d1",
			}},
			EvidenceChain: []lifetime.EvidenceNode{
				{FactID: "f1", Role: lifetime.NodeLifetimePoint, File: "a.cc", Line: 10, Symbol: "Foo"},
				{FactID: "f2", Role: lifetime.NodeAccess, File: "acc.cc", Line: 4, Symbol: "use"},
				{FactID: "f3", Role: lifetime.NodeHolder, File: "h.cc", Line: 3, Symbol: "holder"},
			},
			Synchronization: []lifetime.SyncPrimitive{{
				Primitive: "mutex", Scope: "obj", Protects: "Foo::x", FileLine: "sync.cc:5",
			}},
			LifetimeVisibility: lifetime.LifetimeVisibility{
				HolderNotification: "none",
				Q4Answer:           lifetime.VisibilityAnswer{Value: "refcount", Status: "known"},
			},
			OwnershipAnswers: lifetime.OwnershipAnswers{
				Q1CreatedBy:   lifetime.OwnerAnswer{Value: "factory", File: "a.cc", Line: 1},
				Q2HeldBy:      lifetime.OwnerAnswer{Value: "holder", Form: "raw", Domain: "d1", File: "h.cc", Line: 3},
				Q3DestroyedBy: lifetime.OwnerAnswer{Value: "dtor", Domain: "d1", File: "d.cc", Line: 9},
				Q4:            lifetime.VisibilityAnswer{Value: "refcount", Status: "known"},
			},
			Gap: lifetime.Gap{
				Description: "callback may outlive holder",
				Window:      "teardown",
			},
			TriggerHypothesis: lifetime.TriggerHypothesis{
				Domains:      []string{"d1"},
				Interleaving: "destroy||callback",
			},
			PrimaryForm:    lifetime.FormDanglingThis,
			ContractStatus: lifetime.ContractKnown,
			Version:        1,
		},
	}
}

// TestStoreGetFamilyDetail 需要真实 PostgreSQL；覆盖 GetFamilyDetail 对
// family/points/domains/pairs/sync/callbacks/relations/coverage 的聚合读取。
func TestStoreGetFamilyDetail(t *testing.T) {
	dsn := testDSN()
	if dsn == "" {
		t.Skip("set LIFETIME_TEST_DSN or ARTEX_PG_DSN to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	st, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	base := time.Now().UnixNano()
	taskID := fmt.Sprintf("test-famdetail-%d", base)
	defer cleanupTask(t, st, taskID)

	lineageID := fmt.Sprintf("lin-%d", base)
	indexHash := fmt.Sprintf("idx-%d", base)
	familyID := "fam-detail"
	pairClassID := lifetime.PairClassID("dom-1", "dom-2")

	idx := lifetime.IndexOutput{
		IndexHash:      indexHash,
		IndexQuality:   "heuristic",
		TypeResolution: "partial",
		ObjectFamilies: []lifetime.ObjectFamily{{
			FamilyID:        familyID,
			TypeCanonical:   "v8::internal::Foo",
			AllocSite:       "src/foo.cc:10",
			StorageClass:    lifetime.StorageHeap,
			LifetimeForm:    lifetime.LifetimeRaw,
			ApplicableForms: []lifetime.Form{lifetime.FormDanglingThis},
		}},
		ObjectRelations: []lifetime.ObjectRelation{
			{RelationID: "rel-subject", Type: "registers", SubjectFamilyID: familyID, ObjectFamilyID: "other", Site: "s.cc:1", SourceRef: "s", Confidence: lifetime.ConfidenceHigh, IndexHash: indexHash},
			{RelationID: "rel-object", Type: "refers", SubjectFamilyID: "other", ObjectFamilyID: familyID, Site: "o.cc:2", SourceRef: "o", Confidence: lifetime.ConfidenceMedium, IndexHash: indexHash},
			{RelationID: "rel-unrelated", Type: "owns", SubjectFamilyID: "other", ObjectFamilyID: "third", Site: "u.cc:3", SourceRef: "u", Confidence: lifetime.ConfidenceLow, IndexHash: indexHash},
		},
		Domains: []lifetime.Domain{
			{DomainClassID: "dom-1", Kind: lifetime.DomainThread, InstanceSite: "a.cc:1", Serialization: lifetime.SerializationConcurrent, ConcurrencyDegree: lifetime.ConcurrencyN},
			{DomainClassID: "dom-2", Kind: lifetime.DomainThread, InstanceSite: "b.cc:2", Serialization: lifetime.SerializationConcurrent, ConcurrencyDegree: lifetime.ConcurrencyN},
		},
		DomainPairs: []lifetime.DomainPair{{
			PairClassID:      pairClassID,
			DomainA:          "dom-1",
			DomainB:          "dom-2",
			DomainConfidence: lifetime.DomainConfidenceHigh,
		}},
		LifetimePoints: []lifetime.LifetimePoint{{
			FamilyID: familyID, Site: "src/foo.cc:10", Role: lifetime.NodeLifetimePoint, SourceRef: "foo",
		}},
		SyncPrimitives: []lifetime.SyncPrimitive{
			{Primitive: "mutex", Scope: "Foo", Protects: "Foo::x", FileLine: "src/foo.cc:20"},
			{Primitive: "mutex", Scope: "Other", Protects: "Other::y", FileLine: "sync-other.cc:1"},
		},
		Callbacks: []lifetime.CallbackSpec{
			{RegisterSite: "src/foo.cc:30", Capture: "raw_this", SourceRef: "src/foo.cc"},
			{RegisterSite: "cb-other.cc:1", Capture: "weak", SourceRef: "cb-other.cc"},
		},
	}
	if err := st.IngestIndex(ctx, taskID, lineageID, idx); err != nil {
		t.Fatalf("IngestIndex: %v", err)
	}
	unit := lifetime.NewCoverageUnit(lifetime.UnitKindFamily, familyID, "", pairClassID, lifetime.FormDanglingThis, lineageID)
	if err := st.InitCoverage(ctx, taskID, lineageID, []lifetime.CoverageUnit{unit}, nil); err != nil {
		t.Fatalf("InitCoverage: %v", err)
	}

	detail, err := st.GetFamilyDetail(ctx, taskID, familyID)
	if err != nil {
		t.Fatalf("GetFamilyDetail: %v", err)
	}
	if detail.Family.FamilyID != familyID || detail.Family.TypeCanonical != "v8::internal::Foo" {
		t.Fatalf("family = %+v", detail.Family)
	}
	if detail.LineageID != lineageID || detail.IndexHash != indexHash {
		t.Fatalf("lineage=%q index_hash=%q", detail.LineageID, detail.IndexHash)
	}
	if len(detail.LifetimePoints) != 1 || detail.LifetimePoints[0].Role != lifetime.NodeLifetimePoint {
		t.Fatalf("lifetime_points = %+v", detail.LifetimePoints)
	}
	if len(detail.SyncPrimitives) != 1 || detail.SyncPrimitives[0].Protects != "Foo::x" {
		t.Fatalf("sync_primitives = %+v", detail.SyncPrimitives)
	}
	if len(detail.Callbacks) != 1 || detail.Callbacks[0].Capture != "raw_this" {
		t.Fatalf("callbacks = %+v", detail.Callbacks)
	}
	if len(detail.Relations) != 2 {
		t.Fatalf("relations = %+v", detail.Relations)
	}
	if len(detail.Domains) != 2 || len(detail.DomainPairs) != 1 {
		t.Fatalf("domains=%+v pairs=%+v", detail.Domains, detail.DomainPairs)
	}
	if detail.DomainPairs[0].PairClassID != pairClassID {
		t.Fatalf("pair = %+v", detail.DomainPairs[0])
	}
	if len(detail.Coverage) != 1 || detail.Coverage[0].FamilyID != familyID ||
		detail.Coverage[0].UnitID != scopeID(taskID, unit.UnitID) {
		t.Fatalf("coverage = %+v", detail.Coverage)
	}

	if _, err := st.GetFamilyDetail(ctx, taskID, "no-such-family"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing family err = %v, want ErrNotFound", err)
	}
}
