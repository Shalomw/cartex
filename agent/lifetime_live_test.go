package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestLifetimeLiveTools 对真实 lifetimed 服务做一次工具桥烟测。
// 需要：
//
//	LIFETIME_LIVE_URL=http://127.0.0.1:8790
//	LIFETIME_LIVE_TASK=3
//
// 未设置时跳过，不影响常规单测。
func TestLifetimeLiveTools(t *testing.T) {
	base := os.Getenv("LIFETIME_LIVE_URL")
	task := os.Getenv("LIFETIME_LIVE_TASK")
	if base == "" || task == "" {
		t.Skip("LIFETIME_LIVE_URL / LIFETIME_LIVE_TASK not set")
	}
	ts := NewToolSet(nil, "live")
	ts.SetLifetimeClient(&LifetimeClient{
		BaseURL: base, TaskID: task, Role: "lifetime_auditor", Session: "live-test",
	})

	famTool := lifetimeToolByName(ts.WorkerTools(), "lifetime_families")
	if famTool == nil {
		t.Fatal("lifetime_families not injected")
	}
	res, err := famTool.Call(context.Background(), json.RawMessage(`{"file":"js-number-format","limit":5}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	text := res.Flatten()
	if res.IsError || !strings.Contains(text, "JSNumberFormat") {
		t.Fatalf("lifetime_families result: isError=%v body=%.800s", res.IsError, text)
	}

	var fam struct {
		Families []struct {
			FamilyID string `json:"family_id"`
		} `json:"families"`
	}
	if err := json.Unmarshal([]byte(text), &fam); err != nil {
		t.Fatalf("decode families: %v", err)
	}
	if len(fam.Families) == 0 {
		t.Fatal("no families returned")
	}

	detailTool := lifetimeToolByName(ts.WorkerTools(), "lifetime_family")
	if detailTool == nil {
		t.Fatal("lifetime_family not injected")
	}
	args, _ := json.Marshal(map[string]string{"family_id": fam.Families[0].FamilyID})
	res, err = detailTool.Call(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("family detail call: %v", err)
	}
	if res.IsError || !strings.Contains(res.Flatten(), "applicable_forms") {
		t.Fatalf("lifetime_family result: isError=%v body=%.800s", res.IsError, res.Flatten())
	}
}
