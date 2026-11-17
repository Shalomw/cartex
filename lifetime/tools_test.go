package lifetime

import (
	"encoding/json"
	"testing"
)

func marshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

func TestToolGroupRegistryComplete(t *testing.T) {
	want := map[string]ToolGroup{
		// §16.1 文件与查询工具（7+9+3）
		"read_file":             ToolGroupFileQuery,
		"list_dir":              ToolGroupFileQuery,
		"search_source":         ToolGroupFileQuery,
		"read_snippet":          ToolGroupFileQuery,
		"read_build_log":        ToolGroupFileQuery,
		"read_compile_commands": ToolGroupFileQuery,
		"read_test":             ToolGroupFileQuery,
		"lifetime_of":           ToolGroupFileQuery,
		"owners_of":             ToolGroupFileQuery,
		"domains_of":            ToolGroupFileQuery,
		"domain_pairs_of":       ToolGroupFileQuery,
		"xrefs":                 ToolGroupFileQuery,
		"slice":                 ToolGroupFileQuery,
		"find_nodes":            ToolGroupFileQuery,
		"type_info":             ToolGroupFileQuery,
		"snippet":               ToolGroupFileQuery,
		"call_graph":            ToolGroupFileQuery,
		"ownership_graph":       ToolGroupFileQuery,
		"domain_graph":          ToolGroupFileQuery,
		// §16.2 发现
		"record_static_support": ToolGroupDiscoveryWrite,
		"record_candidate":      ToolGroupDiscoveryWrite,
		"record_no_risk":        ToolGroupDiscoveryWrite,
		"record_unknown":        ToolGroupDiscoveryWrite,
		"record_new_lead":       ToolGroupDiscoveryWrite,
		"request_probe":         ToolGroupDiscoveryWrite,
		// §16.3 验证
		"build_driver":        ToolGroupBuildSubmitReproduce,
		"submit_verification": ToolGroupBuildSubmitReproduce,
		"job_status":          ToolGroupBuildSubmitReproduce,
		"reproduce":           ToolGroupBuildSubmitReproduce,
		"assess_impact":       ToolGroupBuildSubmitReproduce,
		// §16.4 复核/报告
		"submit_review_evidence": ToolGroupReviewEvidence,
		"review_claim":           ToolGroupReviewEvidence,
		"request_new_driver":     ToolGroupReviewEvidence,
		"report_finding":         ToolGroupReport,
		"update_finding_report":  ToolGroupReport,
		// §16.5 队列/planner
		"claim":                  ToolGroupQueueClaim,
		"renew":                  ToolGroupQueueClaim,
		"release":                ToolGroupQueueClaim,
		"complete":               ToolGroupQueueClaim,
		"planner_policy_update":  ToolGroupPlannerPolicy,
		"planner_pin":            ToolGroupPlannerPolicy,
		"planner_close":          ToolGroupPlannerPolicy,
		"request_index_revision": ToolGroupPlannerPolicy,
	}

	for name, group := range want {
		got, ok := ToolGroupOf(name)
		if !ok {
			t.Errorf("ToolGroupOf(%q) 未注册", name)
			continue
		}
		if got != group {
			t.Errorf("ToolGroupOf(%q) = %q, want %q", name, got, group)
		}
	}

	names := AllToolNames()
	if len(names) != len(want) {
		t.Fatalf("AllToolNames 数量 = %d, want %d", len(names), len(want))
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			t.Errorf("AllToolNames 重复: %q", name)
		}
		seen[name] = true
		if _, ok := want[name]; !ok {
			t.Errorf("AllToolNames 含未在 §16 列表中的工具: %q", name)
		}
		if _, ok := ToolGroupOf(name); !ok {
			t.Errorf("AllToolNames 中的 %q 没有工具组", name)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("AllToolNames 缺少 %q", name)
		}
	}

	for _, unknown := range []string{"", "nope", "shell", "memory_event", "record_static_support "} {
		if _, ok := ToolGroupOf(unknown); ok {
			t.Errorf("未知工具 %q 不应注册", unknown)
		}
	}
}

func TestToolValidateToolCallDenied(t *testing.T) {
	denied := []struct {
		role Role
		tool string
	}{
		{RoleLifetimeAuditor, ToolBuildDriver},
		{RoleLifetimeAuditor, ToolSubmitVerification},
		{RoleLifetimeVerifier, ToolRecordCandidate},
		{RoleLifetimeReviewer, ToolRecordCandidate},
		{RoleLifetimeReviewer, ToolReportFinding},
		{RoleReporter, ToolRecordStaticSupport},
		{RolePlanner, ToolClaim},
		{RoleReconciler, ToolReportFinding},
		{RoleOwner, ToolBuildDriver},
		{RoleMainAgent, ToolReadFile},
	}
	for _, c := range denied {
		if err := ValidateToolCall(c.role, c.tool); err == nil {
			t.Errorf("%s 调用 %s 应被拒绝", c.role, c.tool)
		}
	}
	if err := ValidateToolCall(RoleLifetimeAuditor, "shell"); err == nil {
		t.Fatal("未知工具应返回 error")
	}
}

func TestToolInputClaim(t *testing.T) {
	valid := []byte(`{"queue_kind":"Q1","lease_ttl_seconds":1800,"filter":{"family_id":"fam-1"}}`)
	got, err := ValidateToolInput(ToolClaim, valid)
	if err != nil {
		t.Fatalf("合法 claim 被拒绝: %v", err)
	}
	req, ok := got.(ClaimRequest)
	if !ok {
		t.Fatalf("返回类型 = %T, want ClaimRequest", got)
	}
	if req.QueueKind != QueueUnitReadyDiscover || req.LeaseTTLSeconds != 1800 || req.Filter["family_id"] != "fam-1" {
		t.Fatalf("claim 解析错误: %+v", req)
	}

	bad := []struct {
		name string
		raw  string
	}{
		{"缺 queue_kind", `{"lease_ttl_seconds":1800}`},
		{"缺 lease_ttl", `{"queue_kind":"Q1"}`},
		{"负 lease_ttl", `{"queue_kind":"Q1","lease_ttl_seconds":-1}`},
		{"非法 queue_kind", `{"queue_kind":"Q9","lease_ttl_seconds":60}`},
		{"空 filter 值", `{"queue_kind":"Q1","lease_ttl_seconds":60,"filter":{"family_id":""}}`},
		{"空 filter 键", `{"queue_kind":"Q1","lease_ttl_seconds":60,"filter":{"":"x"}}`},
		{"未知字段", `{"queue_kind":"Q1","lease_ttl_seconds":60,"nope":1}`},
		{"尾随内容", `{"queue_kind":"Q1","lease_ttl_seconds":60} {}`},
		{"畸形 JSON", `{"queue_kind":`},
	}
	for _, c := range bad {
		if _, err := ValidateToolInput(ToolClaim, []byte(c.raw)); err == nil {
			t.Errorf("%s: 应返回 error", c.name)
		}
	}
	if _, err := ValidateToolInput(ToolClaim, nil); err == nil {
		t.Fatal("空输入应返回 error")
	}
}

func TestToolInputRecordCandidate(t *testing.T) {
	pkg := CandidatePackage{
		Family: ObjectFamily{
			FamilyID:      "fam-1",
			TypeCanonical: "Widget",
			AllocSite:     "src/a.cc:10",
			StorageClass:  StorageHeap,
			LifetimeForm:  LifetimeRaw,
		},
		DomainPairID:     "dp-1",
		PairBasis:        PairBasisLogical,
		DomainConfidence: DomainConfidenceHigh,
		PrimaryForm:      FormRefcountRace,
	}
	base := RecordCandidateRequest{UnitID: "u1", SupportID: "s1", Package: pkg}

	got, err := ValidateToolInput(ToolRecordCandidate, marshalJSON(t, base))
	if err != nil {
		t.Fatalf("合法 record_candidate 被拒绝: %v", err)
	}
	req, ok := got.(RecordCandidateRequest)
	if !ok {
		t.Fatalf("返回类型 = %T, want RecordCandidateRequest", got)
	}
	if req.UnitID != "u1" || req.SupportID != "s1" || req.Package.PrimaryForm != FormRefcountRace {
		t.Fatalf("record_candidate 解析错误: %+v", req)
	}

	missingUnit := base
	missingUnit.UnitID = ""
	if _, err := ValidateToolInput(ToolRecordCandidate, marshalJSON(t, missingUnit)); err == nil {
		t.Error("缺 unit_id 应返回 error")
	}
	missingSupport := base
	missingSupport.SupportID = ""
	if _, err := ValidateToolInput(ToolRecordCandidate, marshalJSON(t, missingSupport)); err == nil {
		t.Error("缺 support_id 应返回 error")
	}
	badForm := base
	badForm.Package.PrimaryForm = Form("FX")
	if _, err := ValidateToolInput(ToolRecordCandidate, marshalJSON(t, badForm)); err == nil {
		t.Error("非法 primary_form 应返回 error")
	}
	missingFamily := base
	missingFamily.Package.Family.FamilyID = ""
	if _, err := ValidateToolInput(ToolRecordCandidate, marshalJSON(t, missingFamily)); err == nil {
		t.Error("缺 family_id 应返回 error")
	}
	missingPair := base
	missingPair.Package.DomainPairID = ""
	if _, err := ValidateToolInput(ToolRecordCandidate, marshalJSON(t, missingPair)); err == nil {
		t.Error("缺 domain_pair_id 应返回 error")
	}
	badBasis := base
	badBasis.Package.PairBasis = PairBasis("maybe")
	if _, err := ValidateToolInput(ToolRecordCandidate, marshalJSON(t, badBasis)); err == nil {
		t.Error("非法 pair_basis 应返回 error")
	}
}

func TestToolInputSchemas(t *testing.T) {
	cases := []struct {
		name    string
		tool    string
		raw     string
		wantErr bool
	}{
		{
			name: "static_support 合法",
			tool: ToolRecordStaticSupport,
			raw:  `{"unit_id":"u1","facts":[{"fact_id":"f1","role":"holder","file":"a.cc","line":10,"symbol":"H::f","source":"code"}],"chain":[]}`,
		},
		{"static_support 缺 unit", ToolRecordStaticSupport, `{"facts":[],"chain":[]}`, true},
		{"static_support 空证据", ToolRecordStaticSupport, `{"unit_id":"u1","facts":[],"chain":[]}`, true},
		{
			name:    "static_support 非法节点 role",
			tool:    ToolRecordStaticSupport,
			raw:     `{"unit_id":"u1","facts":[{"role":"nope","file":"a.cc","line":1,"symbol":"x"}]}`,
			wantErr: true,
		},
		{
			name: "no_risk 合法",
			tool: ToolRecordNoRisk,
			raw:  `{"unit_id":"u1","form":"F1","arguments":[{"argument_type":"serialized","covers_forms":["F1"],"proof":{"kind":"lock","refs":["a.cc:10"]}}]}`,
		},
		{"no_risk 非法 form", ToolRecordNoRisk, `{"unit_id":"u1","form":"F9","arguments":[{"argument_type":"serialized","covers_forms":["F1"]}]}`, true},
		{"no_risk 无 arguments", ToolRecordNoRisk, `{"unit_id":"u1","form":"F1","arguments":[]}`, true},
		{"no_risk 非法 argument_type", ToolRecordNoRisk, `{"unit_id":"u1","form":"F1","arguments":[{"argument_type":"vibes","covers_forms":["F1"]}]}`, true},
		{"unknown 合法", ToolRecordUnknown, `{"scope":"src/a.cc","reason":"index_failed","detail":"parse error"}`, false},
		{"unknown 非法 reason", ToolRecordUnknown, `{"scope":"s","reason":"because","detail":"d"}`, true},
		{"unknown 缺 detail", ToolRecordUnknown, `{"scope":"s","reason":"index_failed"}`, true},
		{"new_lead 合法", ToolRecordNewLead, `{"lead":{"source_event":"memory","reason":"r","status":"open"}}`, false},
		{"new_lead 缺 source_event", ToolRecordNewLead, `{"lead":{"reason":"r"}}`, true},
		{"new_lead 非法 status", ToolRecordNewLead, `{"lead":{"source_event":"memory","reason":"r","status":"zombie"}}`, true},
		{"request_probe 合法", ToolRequestProbe, `{"candidate_id":"c1","question":"q","expected_signal":"s"}`, false},
		{"request_probe 缺 question", ToolRequestProbe, `{"candidate_id":"c1","expected_signal":"s"}`, true},
		{"renew 合法", ToolRenew, `{"item_id":"i1","claim_id":"cl1","lease_ttl_seconds":60}`, false},
		{"renew 零 ttl", ToolRenew, `{"item_id":"i1","claim_id":"cl1","lease_ttl_seconds":0}`, true},
		{"release 合法", ToolRelease, `{"item_id":"i1","claim_id":"cl1","reason":"give up"}`, false},
		{"release 缺 reason", ToolRelease, `{"item_id":"i1","claim_id":"cl1"}`, true},
		{"complete 合法", ToolComplete, `{"item_id":"i1","claim_id":"cl1","outcome_ref":"o1"}`, false},
		{"complete 缺 outcome", ToolComplete, `{"item_id":"i1","claim_id":"cl1"}`, true},
		{"policy_update 合法", ToolPlannerPolicyUpdate, `{"patch":{"priority_weights":{"risk":1}},"reason":"tune"}`, false},
		{"policy_update 空 patch", ToolPlannerPolicyUpdate, `{"patch":{},"reason":"tune"}`, true},
		{"policy_update 缺 reason", ToolPlannerPolicyUpdate, `{"patch":{"a":1}}`, true},
		{"pin 合法", ToolPlannerPin, `{"item_id":"i1","priority":2.5,"reason":"hot area"}`, false},
		{"pin 缺 reason", ToolPlannerPin, `{"item_id":"i1","priority":2.5}`, true},
		{"close 合法", ToolPlannerClose, `{"plan":{"close":true}}`, false},
		{"close 空 plan", ToolPlannerClose, `{"plan":{}}`, true},
		{"index_revision 合法", ToolRequestIndexRevision, `{"scope":"src/","reason":"stale"}`, false},
		{"index_revision 缺 scope", ToolRequestIndexRevision, `{"reason":"stale"}`, true},
	}
	for _, c := range cases {
		got, err := ValidateToolInput(c.tool, []byte(c.raw))
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: 应返回 error，got %#v", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: 不应报错: %v", c.name, err)
		}
	}
}

func TestToolInputGenericAndUnknown(t *testing.T) {
	got, err := ValidateToolInput(ToolReadFile, []byte(`{"path":"src/a.cc","line":3}`))
	if err != nil {
		t.Fatalf("read_file 通用解析失败: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("read_file 返回类型 = %T, want map[string]any", got)
	}
	if m["path"] != "src/a.cc" {
		t.Fatalf("read_file 参数错误: %#v", m)
	}

	empty, err := ValidateToolInput(ToolReadCompileCommands, nil)
	if err != nil {
		t.Fatalf("无参工具空输入应允许: %v", err)
	}
	if em, ok := empty.(map[string]any); !ok || len(em) != 0 {
		t.Fatalf("无参工具应返回空 map，got %#v", empty)
	}

	if _, err := ValidateToolInput(ToolReadFile, []byte(`{`)); err == nil {
		t.Fatal("畸形 JSON 应报错")
	}
	if _, err := ValidateToolInput(ToolReadFile, []byte(`[1,2]`)); err == nil {
		t.Fatal("数组输入应报错")
	}
	if _, err := ValidateToolInput("shell", []byte(`{}`)); err == nil {
		t.Fatal("未知工具应报错")
	}
}

func TestToolResponseJSONTags(t *testing.T) {
	raw := marshalJSON(t, ToolResponse{OK: false, Error: "bad", ErrorCode: "invalid_input"})
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("ToolResponse unmarshal: %v", err)
	}
	for _, key := range []string{"ok", "error", "error_code"} {
		if _, ok := m[key]; !ok {
			t.Errorf("ToolResponse 缺少 JSON 键 %q: %s", key, raw)
		}
	}
	if _, ok := m["ErrorCode"]; ok {
		t.Errorf("ToolResponse 不应使用 Go 字段名: %s", raw)
	}
}
