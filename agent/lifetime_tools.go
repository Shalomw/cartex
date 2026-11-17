package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/lifetime"
	actool "github.com/Autumn-27/norma/tool"
)

// 本文件把 lifetimed 的工具面（§16.2 发现/查询）注入 ARTEX agent：
//   - LifetimeClient 是 lifetimed HTTP JSON 工具面的轻量客户端；
//   - LifetimeTools 仅在实际配置了 LIFETIME_API_URL/LIFETIME_TASK_ID 时出现，
//     未配置时整个工具组不存在（BuiltinToolSeeds 用 nil client 构造，不入目录）；
//   - 角色与工具面仍由 lifetimed 的 lifetime.ValidateToolCall 强制（R-16.9），
//     客户端只负责携带 X-Lifetime-Role / X-Lifetime-Session。
//
// 领域能力仍属独立服务（R-15.1）：这里只做 HTTP 桥接，不复制 lifetime 语义。

// lifetimeToolPrefix 是注入到 agent 工具列表里的 lifetime 工具公共前缀。
// AugmentTools 据此判断是否需要追加 Lifetime 审计 guidance。
const lifetimeToolPrefix = "lifetime_"

// lifetimeRequestTimeout 是单次 lifetimed 调用的墙钟上限。
const lifetimeRequestTimeout = 30 * time.Second

// lifetimeMaxResponseBytes 防止异常响应把工具输出撑爆（正常响应远小于此）。
const lifetimeMaxResponseBytes = 8 << 20

// LifetimeClient 是 lifetimed HTTP 工具面的客户端。
//
// BaseURL 形如 http://127.0.0.1:8790（末尾斜杠会被忽略）；TaskID 是 lifetimed
// 的任务 id（字符串）；Role/Session 写入每个请求的 X-Lifetime-* 头。
type LifetimeClient struct {
	BaseURL string
	TaskID  string
	Role    string
	Session string
	HTTP    *http.Client
}

// NewLifetimeClientFromEnv 按环境变量构造客户端；LIFETIME_API_URL 或
// LIFETIME_TASK_ID 为空时返回 nil（= 不启用 lifetime 工具）。
//
// LIFETIME_SESSION 是权威会话 ID；为空时按契约使用 role + "-" + pid。
// defaultSession 是调用方提供的稳定会话提示（worker 名/planner）；LIFETIME_SESSION
// 为空时优先用它，再退回 role + "-" + pid。
func NewLifetimeClientFromEnv(role, defaultSession string) *LifetimeClient {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("LIFETIME_API_URL")), "/")
	taskID := strings.TrimSpace(os.Getenv("LIFETIME_TASK_ID"))
	if base == "" || taskID == "" {
		return nil
	}
	session := strings.TrimSpace(os.Getenv("LIFETIME_SESSION"))
	if session == "" {
		ident := strings.TrimSpace(defaultSession)
		if ident == "" {
			ident = role
		}
		if ident == "" {
			ident = "lifetime"
		}
		session = ident + "-" + strconv.Itoa(os.Getpid())
	}
	return &LifetimeClient{
		BaseURL: base,
		TaskID:  taskID,
		Role:    role,
		Session: session,
		HTTP:    &http.Client{Timeout: lifetimeRequestTimeout},
	}
}

// SetLifetimeClient 的实现在 tools.go（ToolSet 字段 lifetime 同处声明）。

func (c *LifetimeClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: lifetimeRequestTimeout}
}

// taskPath 拼出 /v1/tasks/{task}/... 路径（task id 按路径段转义）。
func (c *LifetimeClient) taskPath(suffix string) string {
	return "/v1/tasks/" + url.PathEscape(c.TaskID) + suffix
}

// call 发起一次 lifetimed 请求并把 ToolResponse 外壳映射为工具结果：
//   - 网络/解码错误 → 错误结果（不 panic）；
//   - ok=false → "lifetime <tool>: <error> (<error_code>)"；
//   - ok=true → 原样返回 result 的 JSON 文本（不做长度截断，ToolContext 负责）。
func (c *LifetimeClient) call(ctx context.Context, tool, method, path string, query url.Values, body any) (actool.Result, error) {
	endpoint := strings.TrimRight(c.BaseURL, "/") + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var payload []byte
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return actool.Errorf(fmt.Sprintf("lifetime %s: 编码请求失败: %v", tool, err)), nil
		}
		payload = raw
	}
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return actool.Errorf(fmt.Sprintf("lifetime %s: 构造请求失败: %v", tool, err)), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lifetime-Role", c.Role)
	req.Header.Set("X-Lifetime-Session", c.Session)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return actool.Errorf(fmt.Sprintf("lifetime %s: 请求失败: %v", tool, err)), nil
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, lifetimeMaxResponseBytes))
	if err != nil {
		return actool.Errorf(fmt.Sprintf("lifetime %s: 读取响应失败: %v", tool, err)), nil
	}

	var tr lifetime.ToolResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		snippet := strings.TrimSpace(string(raw))
		if len([]rune(snippet)) > 300 {
			snippet = string([]rune(snippet)[:300]) + "…"
		}
		return actool.Errorf(fmt.Sprintf("lifetime %s: HTTP %d: %s", tool, resp.StatusCode, snippet)), nil
	}
	if !tr.OK {
		// 写工具返回 {ok,error,error_code} 外壳；读端点（families/coverage/
		// candidates/family detail）返回裸 JSON 对象。裸响应没有 ok 字段，
		// 解码后 OK=false 且 error/error_code 为空——此时 2xx 直接作为结果。
		if tr.Error != "" || tr.ErrorCode != "" {
			return actool.Errorf(fmt.Sprintf("lifetime %s: %s (%s)", tool, tr.Error, tr.ErrorCode)), nil
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return actool.Errorf(fmt.Sprintf("lifetime %s: HTTP %d: %.300s", tool, resp.StatusCode, strings.TrimSpace(string(raw)))), nil
		}
		return actool.Text(strings.TrimSpace(string(raw))), nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return actool.Errorf(fmt.Sprintf("lifetime %s: HTTP %d", tool, resp.StatusCode)), nil
	}
	// 保留 result 的原始 JSON 文本（不经过 any 中转，避免大整数/键序被改写）；
	// 上面的 lifetime.ToolResponse 解码只负责 ok/error 外壳。
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Result) == 0 {
		return actool.Text("null"), nil
	}
	return actool.Text(string(envelope.Result)), nil
}

// lifetimeTools 返回本 run 可用的 lifetime 工具；client 未配置时返回 nil，
// 从而完全不改变现有 agent 的工具面与 DB 目录。
func (t *ToolSet) lifetimeTools() []actool.CoreTool {
	if t.lifetime == nil {
		return nil
	}
	tools := []actool.CoreTool{
		t.lifetimeFamiliesTool(),
		t.lifetimeFamilyTool(),
		t.lifetimeCoverageTool(),
		t.lifetimeCandidatesTool(),
	}
	// §16.6 工具面：auditor/owner 才有发现写回；planner 只读 + assess。
	// 工具组按角色裁剪，服务端 ValidateToolCall 仍是最终门禁（R-16.9）。
	switch lifetime.Role(t.lifetime.Role) {
	case lifetime.RoleLifetimeAuditor, lifetime.RoleOwner:
		tools = append(tools,
			// 复合写工具放最前：agent 只需四问，索引侧包由客户端补齐。
			t.lifetimeRecordFindingTool(),
			t.lifetimeRecordStaticSupportTool(),
			t.lifetimeRecordCandidateTool(),
			t.lifetimeRecordNoRiskTool(),
			t.lifetimeRecordUnknownTool(),
			t.lifetimeRequestProbeTool(),
		)
	case lifetime.RolePlanner:
		tools = append(tools, t.lifetimeAssessTool())
	}
	return tools
}

// --- 读工具 ---

func (t *ToolSet) lifetimeFamiliesTool() actool.CoreTool {
	return readTool("lifetime_families",
		"【lifetime 审计】列出 lifetimed 索引中的对象族（object family），是 C++ 多线程对象生命周期审计的入口。"+
			"可选按源文件 file、源码子目录 subdir 过滤，limit 限制条数。返回 family_id、类型、存储类别、生命周期形式等；"+
			"拿到 family_id 后用 lifetime_family 查看生命周期点、holder/access、同步原语、域对与覆盖单位。",
		obj(map[string]any{
			"file":   str("可选：按源文件路径过滤对象族（路径语义由 lifetimed 解释）"),
			"subdir": str("可选：只看某个源码子目录下的对象族"),
			"limit":  intp("可选：返回条数上限"),
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				File   string `json:"file"`
				Subdir string `json:"subdir"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("lifetime_families: invalid input: %v", err)), nil
			}
			q := url.Values{}
			if v := strings.TrimSpace(a.File); v != "" {
				q.Set("file", v)
			}
			if v := strings.TrimSpace(a.Subdir); v != "" {
				q.Set("subdir", v)
			}
			if a.Limit > 0 {
				q.Set("limit", strconv.Itoa(a.Limit))
			}
			return t.lifetime.call(ctx, "lifetime_families", http.MethodGet,
				t.lifetime.taskPath("/families"), q, nil)
		})
}

func (t *ToolSet) lifetimeFamilyTool() actool.CoreTool {
	return readTool("lifetime_family",
		"【lifetime 审计】按 family_id 查看一个对象族的完整索引视图：生命周期点、holder 与竞争访问点、"+
			"同步原语、并发域与域对、覆盖单位（unit_id）。family_id 来自 lifetime_families。"+
			"写 static support / candidate 前先读它，确保引用的 file:line 与 unit_id 真实存在。",
		obj(map[string]any{
			"family_id": str("对象族 ID（lifetime_families 返回的 family_id）"),
		}, "family_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				FamilyID string `json:"family_id"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("lifetime_family: invalid input: %v", err)), nil
			}
			familyID := strings.TrimSpace(a.FamilyID)
			if familyID == "" {
				return actool.Errorf("lifetime_family: family_id 必填"), nil
			}
			return t.lifetime.call(ctx, "lifetime_family", http.MethodGet,
				t.lifetime.taskPath("/families/"+url.PathEscape(familyID)), nil, nil)
		})
}

func (t *ToolSet) lifetimeCoverageTool() actool.CoreTool {
	return readTool("lifetime_coverage",
		"【lifetime 审计】查看本任务的覆盖账本汇总：各覆盖单位（unit）的状态（unexamined/no_risk/candidate/...）、"+
			"按 form / 域对的统计。用于确认哪些 unit 还没审、候选或无风险结论是否已落账，避免重复劳动。",
		obj(map[string]any{}),
		func(ctx context.Context, _ json.RawMessage) (actool.Result, error) {
			return t.lifetime.call(ctx, "lifetime_coverage", http.MethodGet,
				t.lifetime.taskPath("/coverage"), nil, nil)
		})
}

func (t *ToolSet) lifetimeCandidatesTool() actool.CoreTool {
	return readTool("lifetime_candidates",
		"【lifetime 审计】列出本任务已记录的候选（candidate）及其关键字段（candidate_id、unit_id、"+
			"primary_form、domain_pair_id、状态/评估等）。用于查重并决定下一步验证；"+
			"单个候选的评估由 planner 调 lifetime_assess，动态问题用 lifetime_request_probe。",
		obj(map[string]any{}),
		func(ctx context.Context, _ json.RawMessage) (actool.Result, error) {
			return t.lifetime.call(ctx, "lifetime_candidates", http.MethodGet,
				t.lifetime.taskPath("/candidates"), nil, nil)
		})
}

// --- 写工具（发现） ---

func (t *ToolSet) lifetimeRecordStaticSupportTool() actool.CoreTool {
	return writeTool("lifetime_record_static_support",
		"【lifetime 审计·写】为某个覆盖单位（unit_id）记录静态证据支持：facts 是支撑事实，"+
			"chain 是生命周期证据链（至少包含 lifetime_point 与 access 节点）。每个证据节点必须带真实的 "+
			"file:line、symbol 与 role（见 EvidenceNode：fact_id/role/file/line/symbol/source），禁止编造。"+
			"返回 support_id，随后交给 lifetime_record_candidate 使用。",
		obj(map[string]any{
			"unit_id": str("覆盖单位 ID（lifetime_family 返回的 unit_id）"),
			"facts": map[string]any{
				"type":        "array",
				"description": "支撑事实证据节点数组（EvidenceNode：fact_id/role/file/line/symbol/source）",
				"items":       map[string]any{"type": "object"},
			},
			"chain": map[string]any{
				"type":        "array",
				"description": "生命周期证据链（EvidenceNode 数组，按生命周期顺序；至少 lifetime_point + access，节点必须有真实 file:line）",
				"items":       map[string]any{"type": "object"},
			},
			"citation_checks": map[string]any{
				"type":        "array",
				"description": "可选：对 chain/facts 节点的机械引用校验（CitationCheck：status/file/line/symbol/role 等）。status=verified 才会被 S1 接受；不传则 S1 会因 citation unresolved 失败。",
				"items":       map[string]any{"type": "object"},
			},
		}, "unit_id", "facts", "chain"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				UnitID         string          `json:"unit_id"`
				Facts          json.RawMessage `json:"facts"`
				Chain          json.RawMessage `json:"chain"`
				CitationChecks json.RawMessage `json:"citation_checks"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("lifetime_record_static_support: invalid input: %v", err)), nil
			}
			body := struct {
				UnitID         string          `json:"unit_id"`
				Facts          json.RawMessage `json:"facts"`
				Chain          json.RawMessage `json:"chain"`
				CitationChecks json.RawMessage `json:"citation_checks,omitempty"`
			}{UnitID: strings.TrimSpace(a.UnitID), Facts: a.Facts, Chain: a.Chain, CitationChecks: a.CitationChecks}
			return t.lifetime.call(ctx, "lifetime_record_static_support", http.MethodPost,
				t.lifetime.taskPath("/tools/record_static_support"), nil, body)
		})
}

func (t *ToolSet) lifetimeRecordCandidateTool() actool.CoreTool {
	return writeTool("lifetime_record_candidate",
		"【lifetime 审计·写】在静态支持（support_id）之上记录一个生命周期候选包（§8.3）。"+
			"unit_id + support_id 来自前序步骤；package 是完整 CandidatePackage（family、domains、domain_pair_id、"+
			"pair_basis、domain_confidence、holders、competing_accesses、evidence_chain、synchronization、"+
			"lifetime_visibility、ownership_answers 四问 q1–q4、gap、trigger_hypothesis、primary_form、"+
			"contributing_forms 等）。evidence_chain 必须引用真实 file:line，不要伪造。",
		obj(map[string]any{
			"unit_id":    str("覆盖单位 ID"),
			"support_id": str("lifetime_record_static_support 返回的 support_id"),
			"package": map[string]any{
				"type": "object",
				"description": "CandidatePackage（§8.3）：family{family_id,type_canonical,alloc_site,storage_class,lifetime_form}、" +
					"domains、domain_pair_id、pair_basis、domain_confidence、holders、competing_accesses、" +
					"evidence_chain（真实 file:line）、synchronization、lifetime_visibility、" +
					"ownership_answers（q1_created_by/q2_held_by/q3_destroyed_by/q4）、gap、trigger_hypothesis、" +
					"primary_form、contributing_forms 等字段；不要伪造证据",
			},
		}, "unit_id", "support_id", "package"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				UnitID    string          `json:"unit_id"`
				SupportID string          `json:"support_id"`
				Package   json.RawMessage `json:"package"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("lifetime_record_candidate: invalid input: %v", err)), nil
			}
			body := struct {
				UnitID    string          `json:"unit_id"`
				SupportID string          `json:"support_id"`
				Package   json.RawMessage `json:"package"`
			}{UnitID: strings.TrimSpace(a.UnitID), SupportID: strings.TrimSpace(a.SupportID), Package: a.Package}
			return t.lifetime.call(ctx, "lifetime_record_candidate", http.MethodPost,
				t.lifetime.taskPath("/tools/record_candidate"), nil, body)
		})
}

func (t *ToolSet) lifetimeRecordNoRiskTool() actool.CoreTool {
	return writeTool("lifetime_record_no_risk",
		"【lifetime 审计·写】为覆盖单位（unit_id）记录『无风险』结论：form 是生命周期漏洞形态 F1–F8，"+
			"arguments 是论证数组（每条含 argument_type、covers_forms、access_set、proof、sync 等）。"+
			"可选 family_id / domain_pair_id 可由调用方显式附带，通常由 lifetimed 从 unit_id 解析。"+
			"只在确实排除了该 unit 的风险时使用；risky 或证据不足请改用 candidate/unknown。",
		obj(map[string]any{
			"unit_id": str("覆盖单位 ID"),
			"form":    str("生命周期漏洞形态：F1 引用计数竞争 / F2 悬垂 this / F3 注册未反注册 / F4 容器失效 / F5 静态析构顺序 / F6 shared_ptr 本体并发 / F7 惰性初始化竞争 / F8 对象池 ABA"),
			"arguments": map[string]any{
				"type":        "array",
				"description": "无风险论证数组（NoRiskArgument：argument_type/covers_forms/access_set/proof/sync/relation_ref 等）",
				"items":       map[string]any{"type": "object"},
			},
			"family_id":      str("可选：对象族 ID（平台可从 unit_id 解析）"),
			"domain_pair_id": str("可选：域对 ID（平台可从 unit_id 解析）"),
		}, "unit_id", "form", "arguments"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				UnitID       string          `json:"unit_id"`
				Form         string          `json:"form"`
				Arguments    json.RawMessage `json:"arguments"`
				FamilyID     string          `json:"family_id"`
				DomainPairID string          `json:"domain_pair_id"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("lifetime_record_no_risk: invalid input: %v", err)), nil
			}
			body := struct {
				UnitID       string          `json:"unit_id"`
				Form         string          `json:"form"`
				Arguments    json.RawMessage `json:"arguments"`
				FamilyID     string          `json:"family_id,omitempty"`
				DomainPairID string          `json:"domain_pair_id,omitempty"`
			}{
				UnitID: strings.TrimSpace(a.UnitID), Form: strings.TrimSpace(a.Form), Arguments: a.Arguments,
				FamilyID: strings.TrimSpace(a.FamilyID), DomainPairID: strings.TrimSpace(a.DomainPairID),
			}
			return t.lifetime.call(ctx, "lifetime_record_no_risk", http.MethodPost,
				t.lifetime.taskPath("/tools/record_no_risk"), nil, body)
		})
}

func (t *ToolSet) lifetimeRecordUnknownTool() actool.CoreTool {
	return writeTool("lifetime_record_unknown",
		"【lifetime 审计·写】如实登记无法判定的范围：scope 说明未知的 unit/范围，reason 是枚举化的未知原因，"+
			"detail 说明缺什么证据、为什么无法继续。用于把『不知道』交回平台，而不是猜一个结论；"+
			"不要在证据不足时用它替代 candidate，也不要用它掩盖失败。",
		obj(map[string]any{
			"scope":  str("未知的范围（unit_id / family_id / 文件范围等）"),
			"reason": str("未知原因，必须是枚举之一：index_failed | indirect_call_unresolved | third_party | generated_code | out_of_scope（R-8.18）"),
			"detail": str("为什么无法判定、还缺哪些证据"),
		}, "scope", "reason", "detail"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Scope  string `json:"scope"`
				Reason string `json:"reason"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("lifetime_record_unknown: invalid input: %v", err)), nil
			}
			body := struct {
				Scope  string `json:"scope"`
				Reason string `json:"reason"`
				Detail string `json:"detail"`
			}{Scope: strings.TrimSpace(a.Scope), Reason: strings.TrimSpace(a.Reason), Detail: a.Detail}
			return t.lifetime.call(ctx, "lifetime_record_unknown", http.MethodPost,
				t.lifetime.taskPath("/tools/record_unknown"), nil, body)
		})
}

func (t *ToolSet) lifetimeRequestProbeTool() actool.CoreTool {
	return writeTool("lifetime_request_probe",
		"【lifetime 审计·写】auditor 不能执行动态 probe，只能请求：针对 candidate_id 提出一个"+
			"可由动态实验回答的问题 question，并给出期望信号 expected_signal（观察到什么算支持/否定）。"+
			"返回 probe 请求 id，由验证角色执行；不要在这里伪造实验结果。",
		obj(map[string]any{
			"candidate_id":    str("候选 ID"),
			"question":        str("要动态实验回答的问题（尽量可判定）"),
			"expected_signal": str("期望信号：观察到什么现象算支持/否定该候选"),
		}, "candidate_id", "question", "expected_signal"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				CandidateID    string `json:"candidate_id"`
				Question       string `json:"question"`
				ExpectedSignal string `json:"expected_signal"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("lifetime_request_probe: invalid input: %v", err)), nil
			}
			body := struct {
				CandidateID    string `json:"candidate_id"`
				Question       string `json:"question"`
				ExpectedSignal string `json:"expected_signal"`
			}{CandidateID: strings.TrimSpace(a.CandidateID), Question: a.Question, ExpectedSignal: a.ExpectedSignal}
			return t.lifetime.call(ctx, "lifetime_request_probe", http.MethodPost,
				t.lifetime.taskPath("/tools/request_probe"), nil, body)
		})
}

// --- planner 专用 ---

func (t *ToolSet) lifetimeAssessTool() actool.CoreTool {
	return writeTool("lifetime_assess",
		"【lifetime 审计·planner】对已记录的候选 candidate_id 触发平台评估：lifetimed 从索引/静态支持/"+
			"候选/观测/实验派生 S1–S4，物化 Assessment 并按状态投影队列。只有 planner/reconciler 可调用；"+
			"agent 不能自行写 level/confidence/reportable。返回评估结果与是否新建 assessment。",
		obj(map[string]any{
			"candidate_id": str("候选 ID（lifetime_candidates 返回的 candidate_id）"),
		}, "candidate_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				CandidateID string `json:"candidate_id"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("lifetime_assess: invalid input: %v", err)), nil
			}
			candidateID := strings.TrimSpace(a.CandidateID)
			if candidateID == "" {
				return actool.Errorf("lifetime_assess: candidate_id 必填"), nil
			}
			return t.lifetime.call(ctx, "lifetime_assess", http.MethodPost,
				t.lifetime.taskPath("/candidates/"+url.PathEscape(candidateID)+"/assess"), nil, nil)
		})
}

// --- guidance ---

// lifetimeGuidance 是注入系统提示的 lifetime 审计流程指引。它只在本次 run 的
// 最终工具列表真的包含 lifetime_* 工具时出现（见 lifetimeWorkflow）。
//
// 2026-11-11 晚改为"PoC 后、最多一次"的可选流程：实测强制在利用循环里写回
// 会把 90 分钟预算耗尽（4 个候选、0 PoC，最后一个候选离 timeout 只剩 5 分钟）。
// 候选对 SEC-bench 官方判分没有贡献，只服务定位指标；必须先保证 PoC/flag。
const lifetimeGuidance = "\n\n## Lifetime 审计（可选，放在 PoC 之后）\n" +
	"你有 lifetime_* 工具，用于 C++ 多线程对象生命周期审计。**先完成 PoC/flag 和验证；写回是 PoC 确认后的可选动作，不能挤占利用时间。**\n" +
	"1) 确认漏洞（PoC 跑通 / flag 拿到 / finding 可上报）后，如涉及 C++ 生命周期，调用**一次** `lifetime_record_finding`：给出 file、line、summary 和 q1（谁创建）/q2（谁持有，需 form）/q3（谁销毁），每个回答带真实 file:line；\n" +
	"2) 不要先查 lifetime_families/lifetime_family：复合工具会按 file 自动解析对象族、域对、覆盖单位；unit_id/family_id 可省略；\n" +
	"3) 每个 family 最多 1 个候选；工具返回 `deduped:true` 或 `skipped:true` 时**不要重试、不要换写法**，继续收尾；\n" +
	"4) PoC 还没跑通时不要写候选；完全不是生命周期问题时无需写回，在总结里说明即可；\n" +
	"5) 不要伪造 file:line；写入失败时不要反复重试，记录错误并在总结里说明。\n" +
	"判分以 PoC/flag 为准，候选只是附加定位信息。"

// hasLifetimeTool 报告工具列表里是否有 lifetime_* 工具。
func hasLifetimeTool(tools []actool.CoreTool) bool {
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name(), lifetimeToolPrefix) {
			return true
		}
	}
	return false
}

// lifetimeWorkflow 依据最终工具列表决定 Lifetime guidance 是否出现；工具列表
// 原样返回（工具本身在 ToolSet 装配阶段已加好，这里只做提示词接线）。
func lifetimeWorkflow(tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if hasLifetimeTool(tools) {
		return tools, lifetimeGuidance
	}
	return tools, ""
}
