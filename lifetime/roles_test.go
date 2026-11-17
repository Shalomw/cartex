package lifetime

import (
	"reflect"
	"testing"
)

func mustSurface(t *testing.T, r Role) RoleSurface {
	t.Helper()
	s, err := ToolSurfaceFor(r)
	if err != nil {
		t.Fatalf("ToolSurfaceFor(%q): %v", r, err)
	}
	return s
}

func TestRoleSurfaceMatrixPerRole(t *testing.T) {
	want := map[Role]RoleSurface{
		RoleLifetimeAuditor: {
			FileQuery:      true,
			DiscoveryWrite: true,
			QueueClaim:     []QueueKind{QueueUnitReadyDiscover, QueueRepairTask},
			ReadOnly:       false,
		},
		RoleLifetimeVerifier: {
			FileQuery:            true,
			BuildSubmitReproduce: true,
			QueueClaim:           []QueueKind{QueueCandidateReadyVerify, QueueRepairTask},
			ReadOnly:             true,
		},
		RoleLifetimeReviewer: {
			FileQuery:              true,
			BuildSubmitReproduce:   true,
			BuildDriverSandboxOnly: true,
			ReviewEvidence:         true,
			QueueClaim:             []QueueKind{QueueClaimReadyReview, QueueRepairTask},
			ReadOnly:               false,
		},
		RoleReporter: {
			FileQuery:         true,
			FileQueryReadOnly: true,
			Report:            true,
			QueueClaim:        []QueueKind{QueueConfirmedReadyReport, QueueRepairTask},
			ReadOnly:          true,
		},
		RolePlanner: {
			FileQuery:        true,
			QueueClaim:       nil,
			PlannerPolicy:    true,
			OwnerJournalRead: true,
			ReadOnly:         true,
		},
		RoleOwner: {
			FileQuery:        true,
			DiscoveryWrite:   true,
			QueueClaim:       []QueueKind{QueueUnitReadyDiscover, QueueRepairTask},
			OwnerJournal:     true,
			OwnerJournalRead: true,
			ReadOnly:         false,
		},
		RoleReconciler: {
			FileQuery:           true,
			FileQueryReadOnly:   true,
			QueueClaim:          []QueueKind{QueueRepairTask},
			OwnerJournalRead:    true,
			ReconciliationWrite: true,
			ReadOnly:            true,
		},
		// mainagent 不在 §16.6 矩阵：不注入任何 worker 工具组。
		RoleMainAgent: {ReadOnly: true},
	}

	for r, expected := range want {
		got, err := ToolSurfaceFor(r)
		if err != nil {
			t.Fatalf("ToolSurfaceFor(%q): %v", r, err)
		}
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("surface %q mismatch\n got: %+v\nwant: %+v", r, got, expected)
		}
	}

	if _, err := ToolSurfaceFor(Role("bogus_role")); err == nil {
		t.Fatal("未知角色应返回 error")
	}
	if got := mustSurface(t, RolePlanner); got.QueueClaim != nil {
		t.Fatalf("planner 不应有 queue claim，got %v", got.QueueClaim)
	}
	if got := mustSurface(t, RoleReconciler); len(got.QueueClaim) != 1 || got.QueueClaim[0] != QueueRepairTask {
		t.Fatalf("reconciler 只能 claim Q5，got %v", got.QueueClaim)
	}
}

func TestRoleSpecsMatchSpecAndFillSurface(t *testing.T) {
	ordered := []Role{
		RoleLifetimeAuditor, RoleLifetimeVerifier, RoleLifetimeReviewer,
		RoleReporter, RolePlanner, RoleOwner, RoleReconciler, RoleMainAgent,
	}
	if got := AllRoles(); !reflect.DeepEqual(got, ordered) {
		t.Fatalf("AllRoles 顺序/内容错误\n got: %v\nwant: %v", got, ordered)
	}

	duties := map[Role]string{
		RoleLifetimeAuditor:  "静态发现：family、生命周期事实、竞争访问、同步缺口、lifecycle visibility；S1/S2",
		RoleLifetimeVerifier: "DriverSpec、编译、提交作业、搜索、复现、影响",
		RoleLifetimeReviewer: "独立复核：反证、新驱动、独立复现",
		RoleReporter:         "报告",
		RolePlanner:          "预算、全局优先级、跨 unit 依赖、收尾、例外",
		RoleOwner:            "常驻 area 负责人；维护 area journal；在本 area 内优先 claim",
		RoleReconciler:       "对账与修复任务；处理 orphan/stale/queue health",
		RoleMainAgent:        "人在环路：授权、约束、目标、例外",
	}

	specs := RoleSpecs()
	if len(specs) != len(ordered) {
		t.Fatalf("RoleSpecs 数量 = %d, want %d", len(specs), len(ordered))
	}
	for _, r := range ordered {
		spec, ok := RoleSpecFor(r)
		if !ok {
			t.Fatalf("RoleSpecFor(%q) = false", r)
		}
		if spec.Role != r || spec.Name != string(r) {
			t.Errorf("%q spec 身份错误: %+v", r, spec)
		}
		if spec.Duty != duties[r] {
			t.Errorf("%q duty = %q, want %q", r, spec.Duty, duties[r])
		}
		if len(spec.Artifacts) == 0 || len(spec.Forbidden) == 0 {
			t.Errorf("%q artifacts/forbidden 不能为空: %+v", r, spec)
		}
		if !reflect.DeepEqual(spec.Surface, mustSurface(t, r)) {
			t.Errorf("%q spec.Surface 与 ToolSurfaceFor 不一致", r)
		}
		if got := specs[r]; !reflect.DeepEqual(got, spec) {
			t.Errorf("%q RoleSpecs 行与 RoleSpecFor 不一致", r)
		}
	}

	if _, ok := RoleSpecFor(Role("bogus_role")); ok {
		t.Fatal("未知角色 RoleSpecFor 应返回 false")
	}
}

func TestRoleValidateToolCallMatrix(t *testing.T) {
	type call struct {
		role Role
		tool string
	}
	allowed := []call{
		{RoleLifetimeAuditor, ToolReadFile},
		{RoleLifetimeAuditor, ToolRecordCandidate},
		{RoleLifetimeAuditor, ToolClaim},
		{RoleLifetimeAuditor, ToolRelease},
		{RoleLifetimeVerifier, ToolReadFile},
		{RoleLifetimeVerifier, ToolBuildDriver},
		{RoleLifetimeVerifier, ToolSubmitVerification},
		{RoleLifetimeVerifier, ToolJobStatus},
		{RoleLifetimeVerifier, ToolReproduce},
		{RoleLifetimeVerifier, ToolAssessImpact},
		{RoleLifetimeVerifier, ToolClaim},
		{RoleLifetimeVerifier, ToolComplete},
		{RoleLifetimeReviewer, ToolReadFile},
		{RoleLifetimeReviewer, ToolBuildDriver},
		{RoleLifetimeReviewer, ToolSubmitVerification},
		{RoleLifetimeReviewer, ToolReproduce},
		{RoleLifetimeReviewer, ToolAssessImpact},
		{RoleLifetimeReviewer, ToolJobStatus},
		{RoleLifetimeReviewer, ToolSubmitReviewEvidence},
		{RoleLifetimeReviewer, ToolReviewClaim},
		{RoleLifetimeReviewer, ToolRequestNewDriver},
		{RoleLifetimeReviewer, ToolClaim},
		{RoleReporter, ToolReadFile},
		{RoleReporter, ToolListDir},
		{RoleReporter, ToolReportFinding},
		{RoleReporter, ToolUpdateFindingReport},
		{RoleReporter, ToolClaim},
		{RolePlanner, ToolReadFile},
		{RolePlanner, ToolPlannerPolicyUpdate},
		{RolePlanner, ToolPlannerPin},
		{RolePlanner, ToolPlannerClose},
		{RolePlanner, ToolRequestIndexRevision},
		{RoleOwner, ToolReadFile},
		{RoleOwner, ToolRecordCandidate},
		{RoleOwner, ToolRecordNoRisk},
		{RoleOwner, ToolRequestProbe},
		{RoleOwner, ToolClaim},
		{RoleOwner, ToolComplete},
		{RoleReconciler, ToolReadFile},
		{RoleReconciler, ToolClaim},
		{RoleReconciler, ToolRenew},
		{RoleReconciler, ToolRelease},
		{RoleReconciler, ToolComplete},
	}
	for _, c := range allowed {
		if err := ValidateToolCall(c.role, c.tool); err != nil {
			t.Errorf("%s 应可调用 %s: %v", c.role, c.tool, err)
		}
	}

	denied := []call{
		// auditor：无 build/run，不能 review/report/planner。
		{RoleLifetimeAuditor, ToolBuildDriver},
		{RoleLifetimeAuditor, ToolSubmitVerification},
		{RoleLifetimeAuditor, ToolReproduce},
		{RoleLifetimeAuditor, ToolSubmitReviewEvidence},
		{RoleLifetimeAuditor, ToolReportFinding},
		{RoleLifetimeAuditor, ToolPlannerPin},
		// verifier：不能写 discovery/review/report/planner。
		{RoleLifetimeVerifier, ToolRecordCandidate},
		{RoleLifetimeVerifier, ToolRecordNoRisk},
		{RoleLifetimeVerifier, ToolSubmitReviewEvidence},
		{RoleLifetimeVerifier, ToolReportFinding},
		{RoleLifetimeVerifier, ToolPlannerPolicyUpdate},
		// reviewer：不能写 discovery/report/planner；build_driver 的 sandbox
		// 注入位置由 RoleSurface.BuildDriverSandboxOnly 表达，不在工具名层拒绝。
		{RoleLifetimeReviewer, ToolRecordCandidate},
		{RoleLifetimeReviewer, ToolReportFinding},
		{RoleLifetimeReviewer, ToolPlannerPin},
		// reporter：不能写 discovery/evidence/build/planner。
		{RoleReporter, ToolRecordCandidate},
		{RoleReporter, ToolRecordUnknown},
		{RoleReporter, ToolBuildDriver},
		{RoleReporter, ToolSubmitReviewEvidence},
		{RoleReporter, ToolPlannerPin},
		// planner：不得 claim，也不执行。
		{RolePlanner, ToolClaim},
		{RolePlanner, ToolRenew},
		{RolePlanner, ToolRelease},
		{RolePlanner, ToolComplete},
		{RolePlanner, ToolRecordCandidate},
		{RolePlanner, ToolBuildDriver},
		{RolePlanner, ToolReportFinding},
		// owner：不做 build/review/report/planner。
		{RoleOwner, ToolBuildDriver},
		{RoleOwner, ToolSubmitVerification},
		{RoleOwner, ToolReportFinding},
		{RoleOwner, ToolSubmitReviewEvidence},
		{RoleOwner, ToolPlannerPin},
		// reconciler：不写 evidence/report/planner，不 build。
		{RoleReconciler, ToolRecordCandidate},
		{RoleReconciler, ToolBuildDriver},
		{RoleReconciler, ToolReportFinding},
		{RoleReconciler, ToolPlannerPin},
		{RoleReconciler, ToolSubmitReviewEvidence},
		// mainagent：不在 §16.6 矩阵，任何 §16 工具均拒绝。
		{RoleMainAgent, ToolReadFile},
		{RoleMainAgent, ToolClaim},
		{RoleMainAgent, ToolPlannerPin},
		{RoleMainAgent, ToolReportFinding},
	}
	for _, c := range denied {
		if err := ValidateToolCall(c.role, c.tool); err == nil {
			t.Errorf("%s 不应可调用 %s", c.role, c.tool)
		}
	}

	if err := ValidateToolCall(Role("bogus_role"), ToolReadFile); err == nil {
		t.Fatal("未知角色应报错")
	}
	if err := ValidateToolCall(RoleLifetimeAuditor, "shell"); err == nil {
		t.Fatal("未知工具应报错")
	}
	if err := ValidateToolCall(RoleLifetimeAuditor, ""); err == nil {
		t.Fatal("空工具名应报错")
	}
}

func TestClaimQueueMatrixAllRoles(t *testing.T) {
	want := map[Role]map[QueueKind]bool{
		RoleLifetimeAuditor: {
			QueueUnitReadyDiscover:    true,
			QueueRepairTask:           true,
			QueueCandidateReadyVerify: false,
			QueueClaimReadyReview:     false,
			QueueConfirmedReadyReport: false,
		},
		RoleLifetimeVerifier: {
			QueueUnitReadyDiscover:    false,
			QueueCandidateReadyVerify: true,
			QueueRepairTask:           true,
			QueueClaimReadyReview:     false,
			QueueConfirmedReadyReport: false,
		},
		RoleLifetimeReviewer: {
			QueueUnitReadyDiscover:    false,
			QueueCandidateReadyVerify: false,
			QueueClaimReadyReview:     true,
			QueueRepairTask:           true,
			QueueConfirmedReadyReport: false,
		},
		RoleReporter: {
			QueueUnitReadyDiscover:    false,
			QueueCandidateReadyVerify: false,
			QueueClaimReadyReview:     false,
			QueueConfirmedReadyReport: true,
			QueueRepairTask:           true,
		},
		RolePlanner: {
			QueueUnitReadyDiscover:    false,
			QueueCandidateReadyVerify: false,
			QueueClaimReadyReview:     false,
			QueueConfirmedReadyReport: false,
			QueueRepairTask:           false,
		},
		RoleOwner: {
			QueueUnitReadyDiscover:    true,
			QueueRepairTask:           true,
			QueueCandidateReadyVerify: false,
			QueueClaimReadyReview:     false,
			QueueConfirmedReadyReport: false,
		},
		RoleReconciler: {
			QueueUnitReadyDiscover:    false,
			QueueCandidateReadyVerify: false,
			QueueClaimReadyReview:     false,
			QueueConfirmedReadyReport: false,
			QueueRepairTask:           true,
		},
		RoleMainAgent: {
			QueueUnitReadyDiscover:    false,
			QueueCandidateReadyVerify: false,
			QueueClaimReadyReview:     false,
			QueueConfirmedReadyReport: false,
			QueueRepairTask:           false,
		},
	}

	queues := []QueueKind{
		QueueUnitReadyDiscover, QueueCandidateReadyVerify,
		QueueClaimReadyReview, QueueConfirmedReadyReport, QueueRepairTask,
	}
	for _, r := range AllRoles() {
		expect, ok := want[r]
		if !ok {
			t.Fatalf("测试缺少角色 %q 的期望值", r)
		}
		for _, q := range queues {
			if got := CanClaimQueue(r, q); got != expect[q] {
				t.Errorf("CanClaimQueue(%q, %q) = %v, want %v", r, q, got, expect[q])
			}
		}
	}
	if CanClaimQueue(RoleLifetimeAuditor, QueueKind("Q9")) {
		t.Fatal("非法队列应返回 false")
	}
	if CanClaimQueue(Role("bogus_role"), QueueUnitReadyDiscover) {
		t.Fatal("非法角色应返回 false")
	}
}

func TestRoleAgentKeys(t *testing.T) {
	want := map[Role]string{
		RoleLifetimeAuditor:  "lt_auditor",
		RoleLifetimeVerifier: "lt_verifier",
		RoleLifetimeReviewer: "lt_reviewer",
		RoleReporter:         "lt_reporter",
		RolePlanner:          "lt_planner",
		RoleOwner:            "lt_owner",
		RoleReconciler:       "lt_reconciler",
		RoleMainAgent:        "lt_mainagent",
	}
	for r, key := range want {
		if got := AgentKeyForRole(r); got != key {
			t.Errorf("AgentKeyForRole(%q) = %q, want %q", r, got, key)
		}
	}
	if got := AgentKeyForRole(Role("bogus_role")); got != "" {
		t.Errorf("非法角色 key = %q, want empty", got)
	}
}
