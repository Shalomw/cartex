package agent

// lifetime_record_finding：把"agent 已确认的漏洞"一步写成 lifetime 候选。
//
// 背景：lifetime_record_static_support + lifetime_record_candidate 要求完整
// CandidatePackage（域对、holders、competing_accesses、同步原语、四问…）。
// 实测 agent 只读不写（candidates=0 / coverage concluded=0），因为手工拼装
// 这个包的成本高于收益。本工具把索引侧的事实（family、域对、覆盖单位、
// 生命周期点、同步原语）全部由客户端从 lifetimed 查询补齐，agent 只需要
// 提供它已经知道的五件事：位置、摘要、四问中的 q1–q3（可选 q4）。
//
// 该工具仍通过 §16.2 的 record_static_support / record_candidate 落库，
// 不新增 lifetimed 工具面，也不放宽服务端校验；缺字段时由服务端拒绝并
// 把错误原样返回给 agent 修正。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/lifetime"
	actool "github.com/Autumn-27/norma/tool"
)

// errLifetimeSkip 表示"当前条件下不写候选"（如 F2 缺 q4、family 已有候选）。
// 它不是失败：工具应返回 skipped=true 的普通结果，让 agent 继续 PoC，而不是
// 报错后反复重试。实测 q4 重试是纯浪费预算的路径之一。
var errLifetimeSkip = errors.New("lifetime_record_finding skipped")

// lifetimeOwnerAnswerInput 是 q1–q3 的紧凑输入：只要求 value + file + line，
// q2 额外要求 form/domain（与 §12.3 一致）。
type lifetimeOwnerAnswerInput struct {
	Value  string `json:"value"`
	Form   string `json:"form,omitempty"`
	Domain string `json:"domain,omitempty"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

// lifetimeVisibilityInput 是 q4 的可选输入。
type lifetimeVisibilityInput struct {
	Value     string `json:"value,omitempty"`
	Status    string `json:"status,omitempty"` // known|unknown|none
	Reason    string `json:"reason,omitempty"`
	SourceRef string `json:"source_ref,omitempty"`
}

type lifetimeRecordFindingInput struct {
	UnitID      string `json:"unit_id,omitempty"`
	FamilyID    string `json:"family_id,omitempty"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Symbol      string `json:"symbol,omitempty"`
	Summary     string `json:"summary"`
	Evidence    string `json:"evidence,omitempty"`
	PrimaryForm string `json:"primary_form,omitempty"`

	Q1 lifetimeOwnerAnswerInput `json:"q1"`
	Q2 lifetimeOwnerAnswerInput `json:"q2"`
	Q3 lifetimeOwnerAnswerInput `json:"q3"`
	Q4 *lifetimeVisibilityInput `json:"q4,omitempty"`

	HolderNotification string `json:"holder_notification,omitempty"`
	GapWindow          string `json:"gap_window,omitempty"`
	Trigger            string `json:"trigger,omitempty"`
	Domain             string `json:"domain,omitempty"`
}

func (t *ToolSet) lifetimeRecordFindingTool() actool.CoreTool {
	return writeTool("lifetime_record_finding",
		"【lifetime 审计·写·推荐】把已确认的漏洞一步写成 lifetime 候选：平台按 file:line 自动解析对象族、域对、"+
			"覆盖单位与生命周期点，并组装 static support + CandidatePackage；调用方只需给出位置、摘要和四问 q1–q3。"+
			"这是比 record_static_support + record_candidate 更省事的主路径；有候选后才能 report_finding。"+
			"q1=谁创建、q2=谁持有（form 填 member/heap/static/shared 等，domain 可省略默认取域对第一个域）、q3=谁销毁。"+
			"示例（值替换为真实 file:line，不要编造）："+
			`{"file":"src/objects/foo.cc","line":123,"summary":"holder 在析构后仍访问 foo_",`+
			`"q1":{"value":"Foo::Foo 创建对象","file":"src/objects/foo.cc","line":50},`+
			`"q2":{"value":"成员 foo_ 持有","form":"member","file":"src/objects/foo.cc","line":60},`+
			`"q3":{"value":"Foo::~Foo 销毁","file":"src/objects/foo.cc","line":80}}`+
			"。unit_id/family_id 可省略；省略时按 file 在索引里找族。"+
			"**只在 PoC/漏洞确认后调用一次；每个 family 最多 1 个候选；返回 deduped=true 或 skipped=true 时不要重试。**",
		obj(map[string]any{
			"file":         str("漏洞所在源文件（真实路径，如 src/objects/foo.cc）"),
			"line":         intp("漏洞所在行号（正整数）"),
			"summary":      str("一句话说明生命周期缺口/漏洞机理"),
			"evidence":     str("可选：崩溃/复现证据摘要，写入 facts"),
			"symbol":       str("可选：漏洞所在符号（函数/方法名）"),
			"unit_id":      str("可选：lifetime_family 返回的覆盖单位 ID；省略时按 file 选族内首个单位"),
			"family_id":    str("可选：对象族 ID；省略时按 file 自动匹配"),
			"primary_form": str("可选：F1–F8；省略时用覆盖单位的 form"),
			"q1": obj(map[string]any{
				"value": str("谁创建该对象"), "file": str("创建点 file"), "line": intp("创建点 line"),
			}, "value", "file", "line"),
			"q2": obj(map[string]any{
				"value": str("谁持有该对象"), "form": str("持有形式（member/heap/static/shared 等）"),
				"domain": str("可选：持有域 ID，默认取域对第一个域"), "file": str("持有声明/访问点 file"), "line": intp("持有声明/访问点 line"),
			}, "value", "file", "line"),
			"q3": obj(map[string]any{
				"value": str("谁销毁该对象"), "file": str("销毁点 file"), "line": intp("销毁点 line"),
			}, "value", "file", "line"),
			"q4": obj(map[string]any{
				"value":  str("可选：refcount/callback_unregister/weak_ptr/lock/generation/none"),
				"status": str("可选：known|unknown|none；默认 unknown。若所选形态是 F2/F3/F5，必须传 known 或 none（R-12.9 不接受 unknown）"),
				"reason": str("status=unknown 时用 not_documented/dynamic_dispatch/third_party/insufficient_index/out_of_scope"),
			}),
			"holder_notification": str("可选：F2/F3/F5 必填，持有者如何获知销毁"),
			"gap_window":          str("可选：生命周期缺口窗口描述"),
			"trigger":             str("可选：触发交错的描述"),
			"domain":              str("可选：竞争访问所在域 ID，默认取域对第一个域"),
		}, "file", "line", "summary", "q1", "q2", "q3"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a lifetimeRecordFindingInput
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("lifetime_record_finding: invalid input: %v", err)), nil
			}
			res, err := t.lifetime.recordFinding(ctx, a)
			if err != nil {
				if errors.Is(err, errLifetimeSkip) {
					reason := strings.TrimSpace(strings.TrimPrefix(err.Error(), errLifetimeSkip.Error()+": "))
					out, _ := json.Marshal(map[string]any{
						"skipped": true, "reason": reason,
						"note": "不要重试或换写法；继续完成/收尾 PoC。",
					})
					return actool.Text(string(out)), nil
				}
				return actool.Errorf("lifetime_record_finding: " + err.Error()), nil
			}
			return actool.Text(res), nil
		})
}

// recordFinding 查询索引、组装并落库；返回给 agent 的紧凑 JSON。
func (c *LifetimeClient) recordFinding(ctx context.Context, in lifetimeRecordFindingInput) (string, error) {
	file := strings.TrimSpace(in.File)
	line := in.Line
	// 容忍把行号塞进 file（"a.cc:123"）的写法。
	if f, l := splitSite(file); l > 0 {
		file = f
		if line <= 0 {
			line = l
		}
	}
	if file == "" || line <= 0 {
		return "", fmt.Errorf("file 和 line 必填（真实源位置）")
	}
	if strings.TrimSpace(in.Summary) == "" {
		return "", fmt.Errorf("summary 必填")
	}
	if err := validateOwnerAnswer("q1", in.Q1, false); err != nil {
		return "", err
	}
	if err := validateOwnerAnswer("q2", in.Q2, true); err != nil {
		return "", err
	}
	if err := validateOwnerAnswer("q3", in.Q3, false); err != nil {
		return "", err
	}

	familyID := strings.TrimSpace(in.FamilyID)
	if familyID == "" {
		id, err := c.findFamilyForFile(ctx, file)
		if err != nil {
			return "", err
		}
		familyID = id
	}
	detail, err := c.familyDetail(ctx, familyID)
	if err != nil {
		return "", err
	}

	// 每 family 最多 1 个候选：实测同一 family 会在 30 分钟内写出重复候选，
	// 纯浪费预算。已有候选时直接返回 deduped，让 agent 继续 PoC。
	if id, ok := c.existingCandidateForFamily(ctx, familyID); ok {
		out, _ := json.Marshal(map[string]any{
			"candidate_id": id, "family_id": familyID, "deduped": true,
			"note": "该 family 已有候选；不要重试或换写法，继续收尾。",
		})
		return string(out), nil
	}

	unit, pair, form, err := chooseUnit(detail, in)
	if err != nil {
		return "", err
	}
	// R-12.9：F2/F3/F5 的 q4 不能 unknown。这里不报错逼 agent 重试，而是
	// 返回 skipped；agent 若知道 q4 可以主动补，不知道就跳过候选继续 PoC。
	if lifetime.FormVisibilityRequired(form) {
		status := "unknown"
		if in.Q4 != nil && strings.TrimSpace(in.Q4.Status) != "" {
			status = strings.TrimSpace(in.Q4.Status)
		}
		if status == "unknown" {
			return "", fmt.Errorf("%w: primary_form=%s 要求 q4 析构可见性（known/none）；无法回答就跳过候选，不要重试", errLifetimeSkip, form)
		}
	}

	// 域：优先 pair 的两个域；索引缺域时退回 family.Domains。
	domainA, domainB := pair.DomainA, pair.DomainB
	if domainA == "" && len(detail.Domains) > 0 {
		domainA = detail.Domains[0].DomainClassID
	}
	if domainB == "" {
		domainB = domainA
	}
	accessDomain := strings.TrimSpace(in.Domain)
	if accessDomain == "" {
		accessDomain = domainA
	}
	heldDomain := strings.TrimSpace(in.Q2.Domain)
	if heldDomain == "" {
		heldDomain = domainA
	}

	allocFile, allocLine := splitSite(detail.Family.AllocSite)
	if allocLine <= 0 && len(detail.LifetimePoints) > 0 {
		allocFile, allocLine = splitSite(detail.LifetimePoints[0].Site)
	}
	if allocFile == "" || allocLine <= 0 {
		return "", fmt.Errorf("family %s 没有可用的生命周期点（alloc_site 为空），请先用 lifetime_family 确认索引", familyID)
	}

	allocNode := evidenceNode(lifetime.NodeLifetimePoint, allocFile, allocLine, detail.Family.TypeCanonical, "lifetime_index")
	holderNode := evidenceNode(lifetime.NodeHolder, in.Q2.File, in.Q2.Line, in.Q2.Value, "lifetime_record_finding")
	accessNode := evidenceNode(lifetime.NodeAccess, file, line, firstNonEmpty(in.Symbol, detail.Family.TypeCanonical), "lifetime_record_finding")

	chain := []lifetime.EvidenceNode{allocNode, holderNode, accessNode}
	facts := []lifetime.EvidenceNode{accessNode}
	if allocNode.File != accessNode.File || allocNode.Line != accessNode.Line {
		facts = append(facts, allocNode)
	}

	// 引用校验：索引里有据可查的站点标 verified；agent 提供的站点保持
	// unresolved，由 S1/S2 谓词决定证据强度（不伪造 verified）。
	indexed := map[string]bool{}
	addSite := func(site string) {
		f, l := splitSite(site)
		if f != "" && l > 0 {
			indexed[f+":"+strconv.Itoa(l)] = true
		}
	}
	addSite(detail.Family.AllocSite)
	for _, p := range detail.LifetimePoints {
		addSite(p.Site)
	}
	for _, s := range detail.SyncPrimitives {
		addSite(s.FileLine)
	}
	checks := make([]lifetime.CitationCheck, 0, len(chain))
	for _, n := range chain {
		status := lifetime.CitationUnresolved
		if indexed[n.File+":"+strconv.Itoa(n.Line)] {
			status = lifetime.CitationVerified
		}
		checks = append(checks, lifetime.CitationCheck{
			Status: status, File: n.File, Line: n.Line, Symbol: n.Symbol, Role: n.Role,
		})
	}

	supportID, err := c.recordStaticSupport(ctx, lifetime.StaticSupport{
		UnitID: unit.UnitID, Facts: facts, Chain: chain, CitationChecks: checks,
	})
	if err != nil {
		return "", fmt.Errorf("record_static_support: %w", err)
	}

	pkg := lifetime.CandidatePackage{
		Family:             detail.Family,
		Domains:            pickDomains(detail.Domains, domainA, domainB),
		DomainPairID:       pair.PairClassID,
		PairBasis:          pairBasis(pair),
		DomainConfidence:   pair.DomainConfidence,
		Holders:            []lifetime.Holder{{Site: site(in.Q2.File, in.Q2.Line), Form: firstNonEmpty(in.Q2.Form, string(detail.Family.StorageClass)), Domain: heldDomain}},
		CompetingAccesses:  []lifetime.Access{{Site: site(file, line), Domain: accessDomain}},
		EvidenceChain:      chain,
		Synchronization:    usefulSync(detail.SyncPrimitives),
		LifetimeVisibility: buildVisibility(in, domainA),
		OwnershipAnswers: func() lifetime.OwnershipAnswers {
			q2 := ownerAnswer(in.Q2)
			if q2.Domain == "" {
				q2.Domain = heldDomain
			}
			return lifetime.OwnershipAnswers{
				Q1CreatedBy:   ownerAnswer(in.Q1),
				Q2HeldBy:      q2,
				Q3DestroyedBy: ownerAnswer(in.Q3),
				Q4:            visibilityAnswer(in.Q4),
			}
		}(),
		Gap:               lifetime.Gap{Description: in.Summary, Window: firstNonEmpty(in.GapWindow, "unknown")},
		TriggerHypothesis: lifetime.TriggerHypothesis{Domains: []string{domainA, domainB}, Interleaving: firstNonEmpty(in.Trigger, in.Summary)},
		PrimaryForm:       form,
		FamilySet:         []string{detail.Family.FamilyID},
		ContractStatus:    lifetime.ContractUnknown,
		Version:           1,
	}
	pkg.ContributingForms = contributingForms(detail.Family.ApplicableForms, form)
	if pkg.Family.FamilyID == "" {
		pkg.Family.FamilyID = familyID
	}
	if pkg.DomainConfidence == "" {
		pkg.DomainConfidence = lifetime.DomainConfidenceUnknown
	}

	candidateID, err := c.recordCandidate(ctx, lifetime.Candidate{
		UnitID: unit.UnitID, SupportID: supportID, CandidatePackage: pkg,
	})
	if err != nil {
		return "", fmt.Errorf("record_candidate: %w", err)
	}

	out, _ := json.Marshal(map[string]any{
		"candidate_id": candidateID,
		"support_id":   supportID,
		"unit_id":      unit.UnitID,
		"family_id":    pkg.Family.FamilyID,
		"primary_form": string(form),
		"domain_pair":  pair.PairClassID,
		"evidence_chain": []map[string]any{
			{"role": "lifetime_point", "site": site(allocNode.File, allocNode.Line)},
			{"role": "access", "site": site(file, line)},
		},
	})
	return string(out), nil
}

func validateOwnerAnswer(name string, a lifetimeOwnerAnswerInput, needForm bool) error {
	if strings.TrimSpace(a.Value) == "" || strings.TrimSpace(a.File) == "" || a.Line <= 0 {
		return fmt.Errorf("%s: value、file、line 必填", name)
	}
	if needForm && strings.TrimSpace(a.Form) == "" {
		return fmt.Errorf("q2: form 必填（member/heap/static/shared 等）")
	}
	return nil
}

// findFamilyForFile 用 file 过滤索引族，优先取 alloc_site 同文件的族。
func (c *LifetimeClient) findFamilyForFile(ctx context.Context, file string) (string, error) {
	q := url.Values{}
	q.Set("file", file)
	q.Set("limit", "50")
	var payload struct {
		Families []lifetime.ObjectFamily `json:"families"`
	}
	if err := c.doJSON(ctx, "GET", c.taskPath("/families"), q, nil, &payload); err != nil {
		return "", fmt.Errorf("查询 families 失败: %w", err)
	}
	fams := payload.Families
	// 绝对路径（/src/v8/src/...）或带 ./ 的写法可能匹配不到索引里的相对
	// alloc_site；用 basename 再查一次，仍由下面按 basename 选中同文件的族。
	if len(fams) == 0 {
		q.Set("file", baseName(file))
		if err := c.doJSON(ctx, "GET", c.taskPath("/families"), q, nil, &payload); err != nil {
			return "", fmt.Errorf("查询 families 失败: %w", err)
		}
		fams = payload.Families
	}
	if len(fams) == 0 {
		return "", fmt.Errorf("索引中没有匹配 %q 的对象族；先用 lifetime_families 查看可用族", file)
	}
	want := baseName(file)
	for _, f := range fams {
		if baseName(splitSiteFile(f.AllocSite)) == want {
			return f.FamilyID, nil
		}
	}
	return fams[0].FamilyID, nil
}

func (c *LifetimeClient) familyDetail(ctx context.Context, familyID string) (*lifetime.FamilyDetail, error) {
	var d lifetime.FamilyDetail
	if err := c.doJSON(ctx, "GET", c.taskPath("/families/"+url.PathEscape(familyID)), nil, nil, &d); err != nil {
		return nil, fmt.Errorf("查询 family %s 失败: %w", familyID, err)
	}
	return &d, nil
}

// existingCandidateForFamily 查询该 family 是否已有候选。查询失败不阻塞写入
// （宁可多一个候选，也不因 dedupe 查询故障丢掉候选）。
func (c *LifetimeClient) existingCandidateForFamily(ctx context.Context, familyID string) (string, bool) {
	var payload struct {
		Candidates []lifetime.Candidate `json:"candidates"`
	}
	if err := c.doJSON(ctx, "GET", c.taskPath("/candidates"), nil, nil, &payload); err != nil {
		return "", false
	}
	for _, cand := range payload.Candidates {
		if cand.Family.FamilyID == familyID {
			return cand.CandidateID, true
		}
	}
	return "", false
}

func (c *LifetimeClient) recordStaticSupport(ctx context.Context, s lifetime.StaticSupport) (string, error) {
	body := struct {
		UnitID         string                   `json:"unit_id"`
		Facts          []lifetime.EvidenceNode  `json:"facts"`
		Chain          []lifetime.EvidenceNode  `json:"chain"`
		CitationChecks []lifetime.CitationCheck `json:"citation_checks,omitempty"`
	}{UnitID: s.UnitID, Facts: s.Facts, Chain: s.Chain, CitationChecks: s.CitationChecks}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.doJSON(ctx, "POST", c.taskPath("/tools/record_static_support"), nil, body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("响应缺少 support id")
	}
	return out.ID, nil
}

func (c *LifetimeClient) recordCandidate(ctx context.Context, cand lifetime.Candidate) (string, error) {
	body := struct {
		UnitID    string                    `json:"unit_id"`
		SupportID string                    `json:"support_id"`
		Package   lifetime.CandidatePackage `json:"package"`
	}{UnitID: cand.UnitID, SupportID: cand.SupportID, Package: cand.CandidatePackage}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.doJSON(ctx, "POST", c.taskPath("/tools/record_candidate"), nil, body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("响应缺少 candidate id")
	}
	return out.ID, nil
}

// doJSON 发起请求并把结果解码到 dst：
//   - 写端点返回 {"ok":true,"result":<id>}，解 result；
//   - 读端点返回裸 JSON，直接解 dst；
//   - ok=false 时返回 error。
func (c *LifetimeClient) doJSON(ctx context.Context, method, path string, query url.Values, body any, dst any) error {
	endpoint := strings.TrimRight(c.BaseURL, "/") + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var payload []byte
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("编码请求失败: %w", err)
		}
		payload = raw
	}
	var bodyReader io.Reader
	if payload != nil {
		bodyReader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lifetime-Role", c.Role)
	req.Header.Set("X-Lifetime-Session", c.Session)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, lifetimeMaxResponseBytes))
	if err != nil {
		return err
	}
	var envelope struct {
		OK        bool            `json:"ok"`
		Result    json.RawMessage `json:"result"`
		Error     string          `json:"error"`
		ErrorCode string          `json:"error_code"`
	}
	if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &envelope) == nil && (envelope.OK || envelope.Error != "") {
		if !envelope.OK {
			return fmt.Errorf("%s (%s)", envelope.Error, envelope.ErrorCode)
		}
		if dst == nil {
			return nil
		}
		if len(envelope.Result) == 0 {
			return fmt.Errorf("响应缺少 result")
		}
		return json.Unmarshal(envelope.Result, dst)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d: %.300s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if dst == nil {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func chooseUnit(d *lifetime.FamilyDetail, in lifetimeRecordFindingInput) (lifetime.CoverageUnit, lifetime.DomainPair, lifetime.Form, error) {
	if len(d.Coverage) == 0 {
		return lifetime.CoverageUnit{}, lifetime.DomainPair{}, "", fmt.Errorf("family %s 没有覆盖单位，无法写候选", d.Family.FamilyID)
	}
	var unit *lifetime.CoverageUnit
	if id := strings.TrimSpace(in.UnitID); id != "" {
		for i := range d.Coverage {
			if d.Coverage[i].UnitID == id || strings.HasSuffix(d.Coverage[i].UnitID, "@"+id) {
				unit = &d.Coverage[i]
				break
			}
		}
		if unit == nil {
			return lifetime.CoverageUnit{}, lifetime.DomainPair{}, "", fmt.Errorf("unit_id %q 不在 family %s 的覆盖单位中", id, d.Family.FamilyID)
		}
	}
	if unit == nil {
		form := lifetime.Form(strings.TrimSpace(in.PrimaryForm))
		for i := range d.Coverage {
			if form != "" && d.Coverage[i].Form == form {
				unit = &d.Coverage[i]
				break
			}
		}
	}
	if unit == nil {
		unit = &d.Coverage[0]
	}
	form := lifetime.Form(strings.TrimSpace(in.PrimaryForm))
	if form == "" {
		form = unit.Form
	}
	if !form.Valid() {
		return lifetime.CoverageUnit{}, lifetime.DomainPair{}, "", fmt.Errorf("无法确定 primary_form（索引单位为 %q）", unit.Form)
	}
	var pair *lifetime.DomainPair
	for i := range d.DomainPairs {
		if d.DomainPairs[i].PairClassID == unit.DomainPairID {
			pair = &d.DomainPairs[i]
			break
		}
	}
	if pair == nil && len(d.DomainPairs) > 0 {
		pair = &d.DomainPairs[0]
	}
	if pair == nil {
		return lifetime.CoverageUnit{}, lifetime.DomainPair{}, "", fmt.Errorf("family %s 没有域对，无法写候选（domain_pair_id 不能为空）", d.Family.FamilyID)
	}
	return *unit, *pair, form, nil
}

func pairBasis(p lifetime.DomainPair) lifetime.PairBasis {
	if len(p.PairBasisOptions) > 0 && p.PairBasisOptions[0].Valid() {
		return p.PairBasisOptions[0]
	}
	return lifetime.PairBasisLogical
}

func pickDomains(all []lifetime.Domain, a, b string) []lifetime.Domain {
	out := make([]lifetime.Domain, 0, 2)
	seen := map[string]bool{}
	for _, d := range all {
		if (d.DomainClassID == a || d.DomainClassID == b) && !seen[d.DomainClassID] {
			out = append(out, d)
			seen[d.DomainClassID] = true
		}
	}
	if len(out) == 0 {
		return all
	}
	return out
}

func usefulSync(all []lifetime.SyncPrimitive) []lifetime.SyncPrimitive {
	out := make([]lifetime.SyncPrimitive, 0, len(all))
	for _, s := range all {
		if strings.TrimSpace(s.Protects) == "" {
			continue
		}
		out = append(out, s)
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func contributingForms(all []lifetime.Form, primary lifetime.Form) []lifetime.Form {
	out := make([]lifetime.Form, 0, len(all))
	for _, f := range all {
		if f != primary && f.Valid() {
			out = append(out, f)
		}
	}
	return out
}

func buildVisibility(in lifetimeRecordFindingInput, domain string) lifetime.LifetimeVisibility {
	q4 := visibilityAnswer(in.Q4)
	return lifetime.LifetimeVisibility{
		HolderNotification: firstNonEmpty(in.HolderNotification, "unknown"),
		NotificationSite:   site(in.Q2.File, in.Q2.Line),
		Window:             firstNonEmpty(in.GapWindow, "unknown"),
		Q4Answer:           q4,
		VisibilityUnknown:  q4.Status != "known" && q4.Status != "none",
	}
}

func visibilityAnswer(in *lifetimeVisibilityInput) lifetime.VisibilityAnswer {
	if in == nil {
		return lifetime.VisibilityAnswer{Value: "unknown", Status: "unknown", Reason: "dynamic_dispatch"}
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = "unknown"
	}
	ans := lifetime.VisibilityAnswer{Value: firstNonEmpty(in.Value, "unknown"), Status: status, Reason: strings.TrimSpace(in.Reason), SourceRef: strings.TrimSpace(in.SourceRef)}
	if status == "unknown" && ans.Reason == "" {
		ans.Reason = "dynamic_dispatch"
	}
	return ans
}

func ownerAnswer(a lifetimeOwnerAnswerInput) lifetime.OwnerAnswer {
	return lifetime.OwnerAnswer{Value: strings.TrimSpace(a.Value), Form: strings.TrimSpace(a.Form), Domain: strings.TrimSpace(a.Domain), File: strings.TrimSpace(a.File), Line: a.Line}
}

func evidenceNode(role lifetime.NodeRole, file string, line int, symbol, source string) lifetime.EvidenceNode {
	return lifetime.EvidenceNode{
		FactID: fmt.Sprintf("%s-%s-%d", role, baseName(file), line),
		Role:   role, File: strings.TrimSpace(file), Line: line,
		Symbol: strings.TrimSpace(symbol), Source: source,
	}
}

// splitSite 解析 "path:line[:func]"；行号非法时返回 0。
func splitSite(s string) (string, int) {
	s = strings.TrimSpace(s)
	i := strings.Index(s, ":")
	if i <= 0 {
		return s, 0
	}
	file := s[:i]
	rest := s[i+1:]
	if j := strings.Index(rest, ":"); j >= 0 {
		rest = rest[:j]
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil {
		return s, 0
	}
	return file, n
}

func splitSiteFile(s string) string {
	f, _ := splitSite(s)
	return f
}

func baseName(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func site(file string, line int) string {
	if strings.TrimSpace(file) == "" || line <= 0 {
		return ""
	}
	return strings.TrimSpace(file) + ":" + strconv.Itoa(line)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
