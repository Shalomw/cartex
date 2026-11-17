package agent

// 真实链路测试：lifetimed 服务 + PostgreSQL + 索引 bootstrap + 复合写工具。
// 需要 LIFETIME_TEST_DSN；未设置时跳过。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/lifetime"
	"github.com/Autumn-27/artex/lifetime/service"
	"github.com/Autumn-27/artex/lifetime/store"
)

const liveFindingSource = `#include <mutex>
#include <thread>

class Foo {
 public:
  Foo() = default;
  ~Foo() = default;
  void Touch() { x_ = 1; }
  int x_ = 0;
  std::mutex m_;
};

Foo* MakeFoo() { return new Foo(); }

void Race(Foo* f) {
  std::thread a([f]() { f->Touch(); });
  std::thread b([f]() { f->Touch(); });
  a.join();
  b.join();
}
`

func TestLifetimeRecordFindingLive(t *testing.T) {
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
	srv, err := service.New(st, service.Options{})
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "foo.cc"), []byte(liveFindingSource), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	taskID := fmt.Sprintf("live-finding-%d", time.Now().UnixNano())

	body, _ := json.Marshal(map[string]any{
		"source_root": dir, "target_files": []string{"foo.cc"}, "max_files": 10,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, hs.URL+"/v1/tasks/"+taskID+"/bootstrap", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lifetime-Role", "planner")
	req.Header.Set("X-Lifetime-Session", "live-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap status = %d", resp.StatusCode)
	}

	client := &LifetimeClient{
		BaseURL: hs.URL, TaskID: taskID, Role: string(lifetime.RoleLifetimeAuditor), Session: "live-test",
		HTTP: &http.Client{Timeout: 10 * time.Second},
	}
	out, err := client.recordFinding(ctx, lifetimeRecordFindingInput{
		File: "foo.cc", Line: 18,
		Summary: "Foo 被两个线程同时访问且没有锁保护（fixture）",
		Q1:      lifetimeOwnerAnswerInput{Value: "MakeFoo 中 new Foo()", File: "foo.cc", Line: 13},
		Q2:      lifetimeOwnerAnswerInput{Value: "裸指针 f 持有", Form: "heap", File: "foo.cc", Line: 16},
		Q3:      lifetimeOwnerAnswerInput{Value: "fixture 未调用 delete", File: "foo.cc", Line: 13},
		Q4:      &lifetimeVisibilityInput{Value: "none", Status: "none"},
	})
	if err != nil {
		t.Fatalf("recordFinding: %v", err)
	}
	var res struct {
		CandidateID string `json:"candidate_id"`
		UnitID      string `json:"unit_id"`
		FamilyID    string `json:"family_id"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode result: %v (%s)", err, out)
	}
	if res.CandidateID == "" || res.FamilyID == "" {
		t.Fatalf("result = %s", out)
	}

	var payload struct {
		Candidates []lifetime.Candidate `json:"candidates"`
	}
	if err := client.doJSON(ctx, http.MethodGet, client.taskPath("/candidates"), nil, nil, &payload); err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	cands := payload.Candidates
	if len(cands) != 1 || cands[0].CandidateID != res.CandidateID {
		t.Fatalf("candidates = %+v", cands)
	}
	if cands[0].UnitID != res.UnitID || cands[0].Family.FamilyID != res.FamilyID {
		t.Errorf("candidate fields = %+v, result = %+v", cands[0], res)
	}
	if err := lifetime.ValidateFourQuestions(&cands[0].CandidatePackage); err != nil {
		t.Errorf("stored candidate four questions: %v", err)
	}
}
