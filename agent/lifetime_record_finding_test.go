package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/lifetime"
)

// TestLifetimeRecordFinding: 一条 file:line + 四问 → 自动查询 family/detail，
// 组装 static support + candidate 并落库。lifetimed 用 httptest 模拟。
func TestLifetimeRecordFinding(t *testing.T) {
	var posts []struct {
		path string
		body map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-1/families":
			if got := r.URL.Query().Get("file"); got != "src/objects/foo.cc" {
				t.Errorf("families file query = %q", got)
			}
			_, _ = io.WriteString(w, `{"families":[{"family_id":"fam-1","type_canonical":"Foo","alloc_site":"src/objects/foo.cc:50"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-1/candidates":
			_, _ = io.WriteString(w, `{"candidates":[],"assessments":{}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-1/families/fam-1":
			_, _ = io.WriteString(w, `{
  "family":{"family_id":"fam-1","type_canonical":"Foo","alloc_site":"src/objects/foo.cc:50","storage_class":"heap","lifetime_form":"owned","applicable_forms":["F1"]},
  "lifetime_points":[{"family_id":"fam-1","site":"src/objects/foo.cc:50","role":"lifetime_point","source_ref":"src/objects/foo.cc:50"}],
  "sync_primitives":[{"primitive":"mutex","scope":"foo_","protects":"foo_","file_line":"src/objects/foo.cc:40"}],
  "callbacks":[],
  "relations":[],
  "domains":[
    {"domain_class_id":"dom-a","kind":"thread","instance_site":"src/objects/foo.cc:10","serialization":"none","concurrency_degree":"concurrent"},
    {"domain_class_id":"dom-b","kind":"thread","instance_site":"src/objects/foo.cc:20","serialization":"none","concurrency_degree":"concurrent"}
  ],
  "domain_pairs":[{"pair_class_id":"dp-1","domain_a":"dom-a","domain_b":"dom-b","domain_confidence":"low","pair_basis_options":["logical"]}],
  "coverage":[{"unit_id":"task-1@unit-1","unit_kind":"family","family_id":"fam-1","domain_pair_id":"dp-1","form":"F1","status":"unexamined","review_status":"self_asserted","lineage_id":"lin-1"}],
  "index_hash":"idx-1","lineage_id":"lin-1"
}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tools/record_static_support"):
			var m map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &m)
			posts = append(posts, struct {
				path string
				body map[string]any
			}{r.URL.Path, m})
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":"sup-1"}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tools/record_candidate"):
			var m map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &m)
			posts = append(posts, struct {
				path string
				body map[string]any
			}{r.URL.Path, m})
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":"cand-1"}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ts := NewToolSet(nil, "worker")
	ts.SetLifetimeClient(&LifetimeClient{
		BaseURL: srv.URL, TaskID: "task-1", Role: "lifetime_auditor", Session: "sess-1",
	})
	tool := lifetimeToolByName(ts.WorkerTools(), "lifetime_record_finding")
	if tool == nil {
		t.Fatal("lifetime_record_finding not injected")
	}

	in := `{
  "file":"src/objects/foo.cc","line":123,"summary":"holder 在析构后仍访问 foo_",
  "q1":{"value":"Foo::Foo 创建对象","file":"src/objects/foo.cc","line":50},
  "q2":{"value":"成员 foo_ 持有","form":"member","file":"src/objects/foo.cc","line":60},
  "q3":{"value":"Foo::~Foo 销毁","file":"src/objects/foo.cc","line":80}
}`
	res, err := tool.Call(context.Background(), json.RawMessage(in), nil)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Flatten())
	}

	if len(posts) != 2 {
		t.Fatalf("posts = %d, want 2 (%s)", len(posts), res.Flatten())
	}
	support, candidate := posts[0], posts[1]
	if !strings.HasSuffix(support.path, "/tools/record_static_support") {
		t.Errorf("first post path = %s", support.path)
	}
	if support.body["unit_id"] != "task-1@unit-1" {
		t.Errorf("support unit_id = %v", support.body["unit_id"])
	}
	chain, _ := support.body["chain"].([]any)
	var roles []string
	for _, n := range chain {
		m, _ := n.(map[string]any)
		roles = append(roles, m["role"].(string))
	}
	if !contains(roles, "lifetime_point") || !contains(roles, "access") {
		t.Errorf("chain roles = %v", roles)
	}

	if !strings.HasSuffix(candidate.path, "/tools/record_candidate") {
		t.Errorf("second post path = %s", candidate.path)
	}
	if candidate.body["unit_id"] != "task-1@unit-1" || candidate.body["support_id"] != "sup-1" {
		t.Errorf("candidate ids = %v", candidate.body)
	}
	pkg, _ := candidate.body["package"].(map[string]any)
	if pkg == nil {
		t.Fatalf("package missing: %v", candidate.body)
	}
	if pkg["domain_pair_id"] != "dp-1" || pkg["primary_form"] != "F1" {
		t.Errorf("package = %v", pkg)
	}
	family, _ := pkg["family"].(map[string]any)
	if family == nil || family["family_id"] != "fam-1" {
		t.Errorf("package.family = %v", pkg["family"])
	}
	answers, _ := pkg["ownership_answers"].(map[string]any)
	q1, _ := answers["q1_created_by"].(map[string]any)
	if q1 == nil || q1["value"] != "Foo::Foo 创建对象" || q1["line"] != float64(50) {
		t.Errorf("q1 = %v", answers["q1_created_by"])
	}
	accesses, _ := pkg["competing_accesses"].([]any)
	if len(accesses) != 1 {
		t.Errorf("competing_accesses = %v", pkg["competing_accesses"])
	} else if a, _ := accesses[0].(map[string]any); a["site"] != "src/objects/foo.cc:123" {
		t.Errorf("access site = %v", a["site"])
	}
	syncs, _ := pkg["synchronization"].([]any)
	if len(syncs) != 1 {
		t.Errorf("synchronization = %v", pkg["synchronization"])
	}

	// 关键：组装出的包必须能通过服务端同一套校验（§8.3/R-8.16）。
	rawPkg, _ := json.Marshal(pkg)
	var decoded lifetime.CandidatePackage
	if err := json.Unmarshal(rawPkg, &decoded); err != nil {
		t.Fatalf("decode package: %v", err)
	}
	if err := lifetime.ValidateFourQuestions(&decoded); err != nil {
		t.Errorf("four questions: %v", err)
	}
	if ok, missing := lifetime.FormFactsComplete(&decoded); !ok {
		t.Errorf("form facts incomplete: %v", missing)
	}

	if !strings.Contains(res.Flatten(), `"candidate_id":"cand-1"`) {
		t.Errorf("result = %s", res.Flatten())
	}
}

// TestLifetimeRecordFindingDeduped: 同一 family 已有候选时不再写第二个，
// 返回 deduped=true（实测重复候选会浪费 30 分钟预算）。
func TestLifetimeRecordFindingDeduped(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-1/families":
			_, _ = io.WriteString(w, `{"families":[{"family_id":"fam-1","type_canonical":"Foo","alloc_site":"src/objects/foo.cc:50"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-1/families/fam-1":
			_, _ = io.WriteString(w, detailJSON("F1"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-1/candidates":
			_, _ = io.WriteString(w, `{"candidates":[{"candidate_id":"cand-existing","family":{"family_id":"fam-1"}}],"assessments":{}}`)
		case r.Method == http.MethodPost:
			posts++
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":"unexpected"}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	ts := NewToolSet(nil, "worker")
	ts.SetLifetimeClient(&LifetimeClient{BaseURL: srv.URL, TaskID: "task-1", Role: "lifetime_auditor", Session: "sess-1"})
	tool := lifetimeToolByName(ts.WorkerTools(), "lifetime_record_finding")
	in := `{"file":"src/objects/foo.cc","line":123,"summary":"x",
	  "q1":{"value":"ctor","file":"src/objects/foo.cc","line":50},
	  "q2":{"value":"foo_","form":"member","file":"src/objects/foo.cc","line":60},
	  "q3":{"value":"dtor","file":"src/objects/foo.cc","line":80}}`
	res, err := tool.Call(context.Background(), json.RawMessage(in), nil)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if res.IsError {
		t.Fatalf("dedupe must not be an error: %s", res.Flatten())
	}
	if posts != 0 {
		t.Errorf("posts = %d, want 0 (deduped)", posts)
	}
	if !strings.Contains(res.Flatten(), `"deduped":true`) || !strings.Contains(res.Flatten(), "cand-existing") {
		t.Errorf("result = %s", res.Flatten())
	}
}

// TestLifetimeRecordFindingSkipQ4: F2 缺 q4 时返回 skipped 普通结果，不写库、
// 不报错，让 agent 继续 PoC 而不是重试。
func TestLifetimeRecordFindingSkipQ4(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-1/families":
			_, _ = io.WriteString(w, `{"families":[{"family_id":"fam-1","type_canonical":"Foo","alloc_site":"src/objects/foo.cc:50"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-1/families/fam-1":
			_, _ = io.WriteString(w, detailJSON("F2"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-1/candidates":
			_, _ = io.WriteString(w, `{"candidates":[],"assessments":{}}`)
		case r.Method == http.MethodPost:
			posts++
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":"unexpected"}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	ts := NewToolSet(nil, "worker")
	ts.SetLifetimeClient(&LifetimeClient{BaseURL: srv.URL, TaskID: "task-1", Role: "lifetime_auditor", Session: "sess-1"})
	tool := lifetimeToolByName(ts.WorkerTools(), "lifetime_record_finding")
	in := `{"file":"src/objects/foo.cc","line":123,"summary":"x",
	  "q1":{"value":"ctor","file":"src/objects/foo.cc","line":50},
	  "q2":{"value":"foo_","form":"member","file":"src/objects/foo.cc","line":60},
	  "q3":{"value":"dtor","file":"src/objects/foo.cc","line":80}}`
	res, err := tool.Call(context.Background(), json.RawMessage(in), nil)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if res.IsError {
		t.Fatalf("skip must not be an error: %s", res.Flatten())
	}
	if posts != 0 {
		t.Errorf("posts = %d, want 0 (skipped)", posts)
	}
	if !strings.Contains(res.Flatten(), `"skipped":true`) || !strings.Contains(res.Flatten(), "q4") {
		t.Errorf("result = %s", res.Flatten())
	}
}

func detailJSON(form string) string {
	return `{
  "family":{"family_id":"fam-1","type_canonical":"Foo","alloc_site":"src/objects/foo.cc:50","storage_class":"heap","lifetime_form":"owned","applicable_forms":["` + form + `"]},
  "lifetime_points":[{"family_id":"fam-1","site":"src/objects/foo.cc:50","role":"lifetime_point","source_ref":"src/objects/foo.cc:50"}],
  "sync_primitives":[{"primitive":"mutex","scope":"foo_","protects":"foo_","file_line":"src/objects/foo.cc:40"}],
  "callbacks":[],"relations":[],
  "domains":[
    {"domain_class_id":"dom-a","kind":"thread","instance_site":"src/objects/foo.cc:10","serialization":"none","concurrency_degree":"concurrent"},
    {"domain_class_id":"dom-b","kind":"thread","instance_site":"src/objects/foo.cc:20","serialization":"none","concurrency_degree":"concurrent"}
  ],
  "domain_pairs":[{"pair_class_id":"dp-1","domain_a":"dom-a","domain_b":"dom-b","domain_confidence":"low","pair_basis_options":["logical"]}],
  "coverage":[{"unit_id":"task-1@unit-1","unit_kind":"family","family_id":"fam-1","domain_pair_id":"dp-1","form":"` + form + `","status":"unexamined","review_status":"self_asserted","lineage_id":"lin-1"}],
  "index_hash":"idx-1","lineage_id":"lin-1"
}`
}

func TestLifetimeRecordFindingValidation(t *testing.T) {
	ts := NewToolSet(nil, "worker")
	ts.SetLifetimeClient(&LifetimeClient{
		BaseURL: "http://127.0.0.1:1", TaskID: "task-1", Role: "lifetime_auditor", Session: "sess-1",
	})
	tool := lifetimeToolByName(ts.WorkerTools(), "lifetime_record_finding")
	if tool == nil {
		t.Fatal("lifetime_record_finding not injected")
	}
	res, err := tool.Call(context.Background(), json.RawMessage(`{"file":"a.cc","line":1,"summary":"x","q1":{"value":"v","file":"a.cc","line":1},"q2":{"value":"v","file":"a.cc","line":2},"q3":{"value":"v","file":"a.cc","line":3}}`), nil)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Flatten(), "q2: form 必填") {
		t.Errorf("result = %s (isError=%v)", res.Flatten(), res.IsError)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
