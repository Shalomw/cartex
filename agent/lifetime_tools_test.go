package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// 本文件只测 lifetime 工具接线，不依赖 PG：lifetimed 用 httptest 模拟。

var lifetimeWantTools = []string{
	"lifetime_families",
	"lifetime_family",
	"lifetime_coverage",
	"lifetime_candidates",
	"lifetime_record_finding",
	"lifetime_record_static_support",
	"lifetime_record_candidate",
	"lifetime_record_no_risk",
	"lifetime_record_unknown",
	"lifetime_request_probe",
	"lifetime_assess",
}

var (
	lifetimeReadToolNames      = lifetimeWantTools[:4]
	lifetimeDiscoveryToolNames = lifetimeWantTools[4:10]
	lifetimePlannerToolNames   = append(append([]string{}, lifetimeReadToolNames...), "lifetime_assess")
	lifetimeWorkerToolNames    = append(append([]string{}, lifetimeReadToolNames...), lifetimeDiscoveryToolNames...)
)

func lifetimeToolByName(tools []actool.CoreTool, name string) actool.CoreTool {
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	return nil
}

func countLifetimeTools(tools []actool.CoreTool) int {
	n := 0
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name(), lifetimeToolPrefix) {
			n++
		}
	}
	return n
}

// TestLifetimeToolsPresence: client 存在时注入固定工具清单；nil client 时完全不出现；
// assess 只给 planner 角色；BuiltinToolSeeds（nil client）不应把 lifetime 工具写进 DB 目录。
func TestLifetimeToolsPresence(t *testing.T) {
	ts := NewToolSet(nil, "worker")
	if got := countLifetimeTools(ts.WorkerTools()); got != 0 {
		t.Fatalf("worker lifetime tools with nil client = %d, want 0", got)
	}
	if got := countLifetimeTools(ts.PlannerTools()); got != 0 {
		t.Fatalf("planner lifetime tools with nil client = %d, want 0", got)
	}
	if got := ts.lifetimeTools(); got != nil {
		t.Fatalf("lifetimeTools() with nil client = %v, want nil", got)
	}
	for _, seed := range BuiltinToolSeeds() {
		if strings.HasPrefix(seed.Key, lifetimeToolPrefix) {
			t.Errorf("BuiltinToolSeeds must not seed runtime tool %q", seed.Key)
		}
	}

	// planner 角色：只读 + assess（§16.6 planner 没有 discovery 写回）。
	ts.SetLifetimeClient(&LifetimeClient{
		BaseURL: "http://127.0.0.1:1", TaskID: "task-1", Role: "planner", Session: "sess-1",
	})
	plannerTools := ts.PlannerTools()
	if got := countLifetimeTools(plannerTools); got != len(lifetimePlannerToolNames) {
		t.Errorf("planner lifetime tools = %d, want %d", got, len(lifetimePlannerToolNames))
	}
	for _, name := range lifetimePlannerToolNames {
		if lifetimeToolByName(plannerTools, name) == nil {
			t.Errorf("planner missing %s", name)
		}
	}
	if lifetimeToolByName(plannerTools, "lifetime_record_candidate") != nil {
		t.Error("planner must not get discovery write tools")
	}

	// worker（lifetime_auditor）角色：只读 + 发现写回，没有 planner 专用的 assess。
	ts.SetLifetimeClient(&LifetimeClient{
		BaseURL: "http://127.0.0.1:1", TaskID: "task-1", Role: "lifetime_auditor", Session: "sess-1",
	})
	workerTools := ts.WorkerTools()
	if got := countLifetimeTools(workerTools); got != len(lifetimeWorkerToolNames) {
		t.Errorf("worker lifetime tools = %d, want %d", got, len(lifetimeWorkerToolNames))
	}
	if lifetimeToolByName(workerTools, "lifetime_assess") != nil {
		t.Error("worker must not get lifetime_assess")
	}
	for _, name := range lifetimeWorkerToolNames {
		if lifetimeToolByName(workerTools, name) == nil {
			t.Errorf("worker missing %s", name)
		}
	}
}

func TestLifetimeClientFromEnv(t *testing.T) {
	t.Setenv("LIFETIME_API_URL", "")
	t.Setenv("LIFETIME_TASK_ID", "")
	t.Setenv("LIFETIME_SESSION", "")
	if c := NewLifetimeClientFromEnv("lifetime_auditor", "w1"); c != nil {
		t.Fatalf("empty URL should disable lifetime tools, got %+v", c)
	}

	t.Setenv("LIFETIME_API_URL", "http://127.0.0.1:8790/")
	if c := NewLifetimeClientFromEnv("lifetime_auditor", "w1"); c != nil {
		t.Fatalf("empty task id should disable lifetime tools, got %+v", c)
	}

	t.Setenv("LIFETIME_TASK_ID", "task-42")
	c := NewLifetimeClientFromEnv("lifetime_auditor", "w1")
	if c == nil {
		t.Fatal("client should be enabled when URL and task id are set")
	}
	if c.BaseURL != "http://127.0.0.1:8790" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", c.BaseURL)
	}
	if c.TaskID != "task-42" || c.Role != "lifetime_auditor" {
		t.Errorf("client fields = %+v", c)
	}
	// 契约：LIFETIME_SESSION 为空时优先 defaultSession + "-" + pid。
	if want := "w1-" + strconv.Itoa(os.Getpid()); c.Session != want {
		t.Errorf("Session = %q, want %q", c.Session, want)
	}
	if c.HTTP == nil || c.HTTP.Timeout != 30*time.Second {
		t.Errorf("HTTP client timeout = %v, want 30s", c.HTTP)
	}

	t.Setenv("LIFETIME_SESSION", "session-42")
	c = NewLifetimeClientFromEnv("planner", "planner")
	if c == nil || c.Session != "session-42" {
		t.Errorf("LIFETIME_SESSION should win: %+v", c)
	}
}

// TestLifetimeFamiliesRequest 校验 GET 路径/query/请求头，以及 result 原样返回。
func TestLifetimeFamiliesRequest(t *testing.T) {
	type captured struct {
		method, path string
		query        url.Values
		header       http.Header
	}
	var got captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = captured{method: r.Method, path: r.URL.Path, query: r.URL.Query(), header: r.Header.Clone()}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"families":[{"family_id":"fam-1","type_canonical":"Foo"}]}}`)
	}))
	defer srv.Close()

	ts := NewToolSet(nil, "worker")
	ts.SetLifetimeClient(&LifetimeClient{
		BaseURL: srv.URL, TaskID: "task-1", Role: "lifetime_auditor", Session: "sess-1",
	})
	tool := lifetimeToolByName(ts.WorkerTools(), "lifetime_families")
	if tool == nil {
		t.Fatal("lifetime_families not injected")
	}
	res, err := tool.Call(context.Background(), json.RawMessage(`{"file":"src/foo.cc","subdir":"src","limit":5}`), nil)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Flatten())
	}

	if got.method != http.MethodGet {
		t.Errorf("method = %s, want GET", got.method)
	}
	if got.path != "/v1/tasks/task-1/families" {
		t.Errorf("path = %s", got.path)
	}
	if got.query.Get("file") != "src/foo.cc" || got.query.Get("subdir") != "src" || got.query.Get("limit") != "5" {
		t.Errorf("query = %v", got.query)
	}
	if got.header.Get("X-Lifetime-Role") != "lifetime_auditor" {
		t.Errorf("X-Lifetime-Role = %q", got.header.Get("X-Lifetime-Role"))
	}
	if got.header.Get("X-Lifetime-Session") != "sess-1" {
		t.Errorf("X-Lifetime-Session = %q", got.header.Get("X-Lifetime-Session"))
	}
	if got.header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", got.header.Get("Content-Type"))
	}

	var out struct {
		Families []struct {
			FamilyID string `json:"family_id"`
		} `json:"families"`
	}
	if err := json.Unmarshal([]byte(res.Flatten()), &out); err != nil {
		t.Fatalf("decode result: %v (%s)", err, res.Flatten())
	}
	if len(out.Families) != 1 || out.Families[0].FamilyID != "fam-1" {
		t.Errorf("result = %s", res.Flatten())
	}
}

// TestLifetimeRecordCandidatePost 校验 POST 路径与请求体字段原样透传（package 不被裁剪）。
func TestLifetimeRecordCandidatePost(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("server decode body: %v (%s)", err, raw)
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":"support-1"}}`)
	}))
	defer srv.Close()

	ts := NewToolSet(nil, "worker")
	ts.SetLifetimeClient(&LifetimeClient{
		BaseURL: srv.URL, TaskID: "task-1", Role: "lifetime_auditor", Session: "sess-1",
	})
	tool := lifetimeToolByName(ts.WorkerTools(), "lifetime_record_candidate")
	if tool == nil {
		t.Fatal("lifetime_record_candidate not injected")
	}
	in := `{"unit_id":"unit-1","support_id":"support-0","package":` +
		`{"family":{"family_id":"fam-1"},"domain_pair_id":"dp-1","primary_form":"F2",` +
		`"competing_accesses":[{"site":"a.cc:10","domain":"thread-1"}]}}`
	res, err := tool.Call(context.Background(), json.RawMessage(in), nil)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Flatten())
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/v1/tasks/task-1/tools/record_candidate" {
		t.Errorf("path = %s", gotPath)
	}
	if gotBody["unit_id"] != "unit-1" || gotBody["support_id"] != "support-0" {
		t.Errorf("body = %v", gotBody)
	}
	pkg, _ := gotBody["package"].(map[string]any)
	if pkg == nil {
		t.Fatalf("package missing from body: %v", gotBody)
	}
	family, _ := pkg["family"].(map[string]any)
	if family == nil || family["family_id"] != "fam-1" {
		t.Errorf("package.family = %v", pkg["family"])
	}
	if pkg["primary_form"] != "F2" || pkg["domain_pair_id"] != "dp-1" {
		t.Errorf("package = %v", pkg)
	}
	if accesses, _ := pkg["competing_accesses"].([]any); len(accesses) != 1 {
		t.Errorf("package.competing_accesses = %v", pkg["competing_accesses"])
	}
	if res.Flatten() != `{"id":"support-1"}` {
		t.Errorf("result = %s", res.Flatten())
	}
}

// TestLifetimeToolError: 403 + ToolResponse{ok:false} → 普通错误结果（非 Go error/panic）。
func TestLifetimeToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"ok":false,"error":"forbidden by role","error_code":"forbidden"}`)
	}))
	defer srv.Close()

	ts := NewToolSet(nil, "worker")
	ts.SetLifetimeClient(&LifetimeClient{
		BaseURL: srv.URL, TaskID: "task-1", Role: "lifetime_auditor", Session: "sess-1",
	})
	tool := lifetimeToolByName(ts.WorkerTools(), "lifetime_families")
	res, err := tool.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("tool error must be returned as a result, got err=%v", err)
	}
	if !res.IsError {
		t.Fatalf("want error result, got %s", res.Flatten())
	}
	want := "lifetime lifetime_families: forbidden by role (forbidden)"
	if res.Flatten() != want {
		t.Errorf("error text = %q, want %q", res.Flatten(), want)
	}
}

// TestLifetimeNetworkError: 连接失败也返回 error result，不 panic。
func TestLifetimeNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // 关闭后连接被拒

	ts := NewToolSet(nil, "worker")
	ts.SetLifetimeClient(&LifetimeClient{
		BaseURL: base, TaskID: "task-1", Role: "lifetime_auditor", Session: "sess-1",
	})
	tool := lifetimeToolByName(ts.WorkerTools(), "lifetime_families")
	res, err := tool.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("network error must be a result, got err=%v", err)
	}
	if !res.IsError || !strings.Contains(res.Flatten(), "lifetime lifetime_families:") {
		t.Errorf("result = %q (isError=%v)", res.Flatten(), res.IsError)
	}
}

// TestLifetimeGuidance: 只有最终工具列表含 lifetime_* 时才注入 guidance。
func TestLifetimeGuidance(t *testing.T) {
	if _, g := lifetimeWorkflow(nil); g != "" {
		t.Fatalf("guidance without lifetime tools = %q, want empty", g)
	}
	ts := NewToolSet(nil, "planner")
	plain := ts.PlannerTools() // nil client → 无 lifetime 工具
	if hasLifetimeTool(plain) {
		t.Fatal("planner tools without a client must not contain lifetime_*")
	}
	if _, g := lifetimeWorkflow(plain); g != "" {
		t.Fatalf("guidance with plain tools = %q, want empty", g)
	}

	ts.SetLifetimeClient(&LifetimeClient{
		BaseURL: "http://127.0.0.1:1", TaskID: "task-1", Role: "planner", Session: "sess-1",
	})
	tools, guide := lifetimeWorkflow(ts.PlannerTools())
	if guide == "" || !hasLifetimeTool(tools) {
		t.Fatalf("guidance missing with lifetime tools (guide=%q)", guide)
	}
	for _, want := range []string{"## Lifetime 审计", "PoC", "deduped", "不要伪造 file:line"} {
		if !strings.Contains(guide, want) {
			t.Errorf("guidance missing %q", want)
		}
	}

	// deferredSystem 会把 guidance 追加进系统提示。
	system, _ := deferredSystem("SYS", DeferredInfo{LifetimeGuidance: guide})
	if len(system) == 0 || !strings.Contains(system[0], "## Lifetime 审计") {
		t.Fatalf("deferredSystem did not append lifetime guidance: %v", system)
	}

	// AugmentTools 端到端：最终列表含 lifetime 工具 → DeferredInfo 带 guidance。
	oldAugment, oldResolve := ToolAugment, ToolResolve
	ToolAugment, ToolResolve = nil, nil
	defer func() { ToolAugment, ToolResolve = oldAugment, oldResolve }()
	base := []actool.CoreTool{lifetimeToolByName(ts.PlannerTools(), "lifetime_families")}
	_, def, cleanup := AugmentTools(context.Background(), "planner", base)
	defer cleanup()
	if def.LifetimeGuidance == "" {
		t.Error("AugmentTools did not set LifetimeGuidance for a lifetime_* tool list")
	}

	_, def, cleanup = AugmentTools(context.Background(), "planner", []actool.CoreTool{ts.listGoals()})
	defer cleanup()
	if def.LifetimeGuidance != "" {
		t.Errorf("AugmentTools must not set LifetimeGuidance without lifetime tools: %q", def.LifetimeGuidance)
	}
}
