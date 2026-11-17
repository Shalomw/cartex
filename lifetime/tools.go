package lifetime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// 本文件实现 §16.1–§16.5 的工具名、工具组注册表与请求 schema。
//
// R-5.10/R-16.9：所有角色都必须通过本文件的工具 schema，禁止自由文本绕过；
// 角色工具面校验见 roles.go 的 ValidateToolCall。工具组注册表只收录 §16 定义
// 的工具，不自行发明工具名。

// ---------------------------------------------------------------------------
// §16.1 文件与查询工具
// ---------------------------------------------------------------------------

const (
	ToolReadFile            = "read_file"
	ToolListDir             = "list_dir"
	ToolSearchSource        = "search_source"
	ToolReadSnippet         = "read_snippet"
	ToolReadBuildLog        = "read_build_log"
	ToolReadCompileCommands = "read_compile_commands"
	ToolReadTest            = "read_test"

	ToolLifetimeOf    = "lifetime_of"
	ToolOwnersOf      = "owners_of"
	ToolDomainsOf     = "domains_of"
	ToolDomainPairsOf = "domain_pairs_of"
	ToolXrefs         = "xrefs"
	ToolSlice         = "slice"
	ToolFindNodes     = "find_nodes"
	ToolTypeInfo      = "type_info"
	ToolSnippet       = "snippet"

	ToolCallGraph      = "call_graph"
	ToolOwnershipGraph = "ownership_graph"
	ToolDomainGraph    = "domain_graph"
)

// ---------------------------------------------------------------------------
// §16.2 发现工具
// ---------------------------------------------------------------------------

const (
	ToolRecordStaticSupport = "record_static_support"
	ToolRecordCandidate     = "record_candidate"
	ToolRecordNoRisk        = "record_no_risk"
	ToolRecordUnknown       = "record_unknown"
	ToolRecordNewLead       = "record_new_lead"
	ToolRequestProbe        = "request_probe"
)

// ---------------------------------------------------------------------------
// §16.3 验证工具
// ---------------------------------------------------------------------------

const (
	ToolBuildDriver        = "build_driver"
	ToolSubmitVerification = "submit_verification"
	ToolJobStatus          = "job_status"
	ToolReproduce          = "reproduce"
	ToolAssessImpact       = "assess_impact"
)

// ---------------------------------------------------------------------------
// §16.4 复核与报告工具
// ---------------------------------------------------------------------------

const (
	ToolSubmitReviewEvidence = "submit_review_evidence"
	ToolReviewClaim          = "review_claim"
	ToolRequestNewDriver     = "request_new_driver"
	ToolReportFinding        = "report_finding"
	ToolUpdateFindingReport  = "update_finding_report"
)

// ---------------------------------------------------------------------------
// §16.5 队列与 planner 工具
// ---------------------------------------------------------------------------

const (
	ToolClaim    = "claim"
	ToolRenew    = "renew"
	ToolRelease  = "release"
	ToolComplete = "complete"

	ToolPlannerPolicyUpdate  = "planner_policy_update"
	ToolPlannerPin           = "planner_pin"
	ToolPlannerClose         = "planner_close"
	ToolRequestIndexRevision = "request_index_revision"
)

// toolGroupRegistry 是工具名 → §16.6 工具组的完整注册表。
//
// ToolGroupOwnerJournal 与 ToolGroupReconciliationWrite 是 §16.6 矩阵行，但
// §16.1–§16.5 没有为它们定义 agent 工具（owner journal 由 owner memory service
// 管理，对账写由 reconciler/platform 服务执行），故注册表中没有对应工具名。
var toolGroupRegistry = map[string]ToolGroup{
	// §16.1 文件与查询工具
	ToolReadFile:            ToolGroupFileQuery,
	ToolListDir:             ToolGroupFileQuery,
	ToolSearchSource:        ToolGroupFileQuery,
	ToolReadSnippet:         ToolGroupFileQuery,
	ToolReadBuildLog:        ToolGroupFileQuery,
	ToolReadCompileCommands: ToolGroupFileQuery,
	ToolReadTest:            ToolGroupFileQuery,
	ToolLifetimeOf:          ToolGroupFileQuery,
	ToolOwnersOf:            ToolGroupFileQuery,
	ToolDomainsOf:           ToolGroupFileQuery,
	ToolDomainPairsOf:       ToolGroupFileQuery,
	ToolXrefs:               ToolGroupFileQuery,
	ToolSlice:               ToolGroupFileQuery,
	ToolFindNodes:           ToolGroupFileQuery,
	ToolTypeInfo:            ToolGroupFileQuery,
	ToolSnippet:             ToolGroupFileQuery,
	ToolCallGraph:           ToolGroupFileQuery,
	ToolOwnershipGraph:      ToolGroupFileQuery,
	ToolDomainGraph:         ToolGroupFileQuery,

	// §16.2 发现工具
	ToolRecordStaticSupport: ToolGroupDiscoveryWrite,
	ToolRecordCandidate:     ToolGroupDiscoveryWrite,
	ToolRecordNoRisk:        ToolGroupDiscoveryWrite,
	ToolRecordUnknown:       ToolGroupDiscoveryWrite,
	ToolRecordNewLead:       ToolGroupDiscoveryWrite,
	ToolRequestProbe:        ToolGroupDiscoveryWrite,

	// §16.3 验证工具
	ToolBuildDriver:        ToolGroupBuildSubmitReproduce,
	ToolSubmitVerification: ToolGroupBuildSubmitReproduce,
	ToolJobStatus:          ToolGroupBuildSubmitReproduce,
	ToolReproduce:          ToolGroupBuildSubmitReproduce,
	ToolAssessImpact:       ToolGroupBuildSubmitReproduce,

	// §16.4 复核与报告工具
	ToolSubmitReviewEvidence: ToolGroupReviewEvidence,
	ToolReviewClaim:          ToolGroupReviewEvidence,
	ToolRequestNewDriver:     ToolGroupReviewEvidence,
	ToolReportFinding:        ToolGroupReport,
	ToolUpdateFindingReport:  ToolGroupReport,

	// §16.5 队列与 planner 工具
	ToolClaim:                ToolGroupQueueClaim,
	ToolRenew:                ToolGroupQueueClaim,
	ToolRelease:              ToolGroupQueueClaim,
	ToolComplete:             ToolGroupQueueClaim,
	ToolPlannerPolicyUpdate:  ToolGroupPlannerPolicy,
	ToolPlannerPin:           ToolGroupPlannerPolicy,
	ToolPlannerClose:         ToolGroupPlannerPolicy,
	ToolRequestIndexRevision: ToolGroupPlannerPolicy,
}

// allToolNames 是 §16.1–§16.5 的固定工具顺序，供注册/审计遍历。
var allToolNames = []string{
	ToolReadFile, ToolListDir, ToolSearchSource, ToolReadSnippet,
	ToolReadBuildLog, ToolReadCompileCommands, ToolReadTest,
	ToolLifetimeOf, ToolOwnersOf, ToolDomainsOf, ToolDomainPairsOf,
	ToolXrefs, ToolSlice, ToolFindNodes, ToolTypeInfo, ToolSnippet,
	ToolCallGraph, ToolOwnershipGraph, ToolDomainGraph,
	ToolRecordStaticSupport, ToolRecordCandidate, ToolRecordNoRisk,
	ToolRecordUnknown, ToolRecordNewLead, ToolRequestProbe,
	ToolBuildDriver, ToolSubmitVerification, ToolJobStatus, ToolReproduce, ToolAssessImpact,
	ToolSubmitReviewEvidence, ToolReviewClaim, ToolRequestNewDriver,
	ToolReportFinding, ToolUpdateFindingReport,
	ToolClaim, ToolRenew, ToolRelease, ToolComplete,
	ToolPlannerPolicyUpdate, ToolPlannerPin, ToolPlannerClose, ToolRequestIndexRevision,
}

// ToolGroupOf 返回工具所属的 §16.6 工具组；未注册的工具返回 false。
func ToolGroupOf(name string) (ToolGroup, bool) {
	group, ok := toolGroupRegistry[name]
	return group, ok
}

// AllToolNames 返回 §16.1–§16.5 定义的全部工具名，顺序固定。
func AllToolNames() []string {
	out := make([]string, len(allToolNames))
	copy(out, allToolNames)
	return out
}

// ---------------------------------------------------------------------------
// §16.2/§16.5 请求/响应类型
// ---------------------------------------------------------------------------

// RecordStaticSupportRequest 是 record_static_support(unit_id, facts[], chain[])
// 的请求（§16.2）。
type RecordStaticSupportRequest struct {
	UnitID         string          `json:"unit_id"`
	Facts          []EvidenceNode  `json:"facts"`
	Chain          []EvidenceNode  `json:"chain"`
	CitationChecks []CitationCheck `json:"citation_checks,omitempty"`
}

// RecordCandidateRequest 是 record_candidate(unit_id, support_id, package) 的请求。
type RecordCandidateRequest struct {
	UnitID    string           `json:"unit_id"`
	SupportID string           `json:"support_id"`
	Package   CandidatePackage `json:"package"`
}

// RecordNoRiskRequest 是 record_no_risk(unit_id, form, arguments[]) 的请求。
type RecordNoRiskRequest struct {
	UnitID    string           `json:"unit_id"`
	Form      string           `json:"form"`
	Arguments []NoRiskArgument `json:"arguments"`
}

// RecordUnknownRequest 是 record_unknown(scope, reason, detail) 的请求。
type RecordUnknownRequest struct {
	Scope  string        `json:"scope"`
	Reason UnknownReason `json:"reason"`
	Detail string        `json:"detail"`
}

// RecordNewLeadRequest 是 record_new_lead(lead) 的请求。
type RecordNewLeadRequest struct {
	Lead NewLead `json:"lead"`
}

// RequestProbeRequest 是 request_probe(candidate_id, question, expected_signal)
// 的请求（§16.2；auditor 不能执行 probe，只能请求）。
type RequestProbeRequest struct {
	CandidateID    string `json:"candidate_id"`
	Question       string `json:"question"`
	ExpectedSignal string `json:"expected_signal"`
}

// ClaimRequest 是 claim(queue_kind, lease_ttl, filter) 的请求（§16.5）。
// lease_ttl 以秒为单位；session_id 由工具上下文提供，不是显式参数（§6.4）。
type ClaimRequest struct {
	QueueKind       QueueKind         `json:"queue_kind"`
	LeaseTTLSeconds int               `json:"lease_ttl_seconds"`
	Filter          map[string]string `json:"filter,omitempty"`
}

// RenewRequest 是 renew(item_id, claim_id, lease_ttl) 的请求。
type RenewRequest struct {
	ItemID          string `json:"item_id"`
	ClaimID         string `json:"claim_id"`
	LeaseTTLSeconds int    `json:"lease_ttl_seconds"`
}

// ReleaseRequest 是 release(item_id, claim_id, reason) 的请求。
type ReleaseRequest struct {
	ItemID  string `json:"item_id"`
	ClaimID string `json:"claim_id"`
	Reason  string `json:"reason"`
}

// CompleteRequest 是 complete(item_id, claim_id, outcome_ref) 的请求。
type CompleteRequest struct {
	ItemID     string `json:"item_id"`
	ClaimID    string `json:"claim_id"`
	OutcomeRef string `json:"outcome_ref"`
}

// PlannerPolicyUpdateRequest 是 planner_policy_update(policy_patch) 的请求。
// Reason 是平台审计要求（R-6.18/R-16.7 的同类理由记录）。
type PlannerPolicyUpdateRequest struct {
	Patch  map[string]any `json:"patch"`
	Reason string         `json:"reason"`
}

// PlannerPinRequest 是 planner_pin(item_id, priority, reason) 的请求。
type PlannerPinRequest struct {
	ItemID   string  `json:"item_id"`
	Priority float64 `json:"priority"`
	Reason   string  `json:"reason"`
}

// PlannerCloseRequest 是 planner_close(plan) 的请求；平台校验 §7.6 收尾规则。
type PlannerCloseRequest struct {
	Plan map[string]any `json:"plan"`
}

// RequestIndexRevisionRequest 是 request_index_revision(scope, reason) 的请求。
type RequestIndexRevisionRequest struct {
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
}

// ToolResponse 是所有 lifetime 工具的统一响应外壳。
type ToolResponse struct {
	OK        bool   `json:"ok"`
	Result    any    `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

var errToolInputInvalid = errors.New("lifetime: 工具输入非法")

// ---------------------------------------------------------------------------
// 轻量校验
// ---------------------------------------------------------------------------

func trimEmpty(s string) bool { return strings.TrimSpace(s) == "" }

func invalidInputf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errToolInputInvalid, fmt.Sprintf(format, args...))
}

func validateEvidenceNode(n EvidenceNode) error {
	if !n.Role.Valid() {
		return invalidInputf("evidence node role 非法: %q", n.Role)
	}
	if trimEmpty(n.File) {
		return invalidInputf("evidence node file 必填")
	}
	if n.Line <= 0 {
		return invalidInputf("evidence node line 必须为正")
	}
	if trimEmpty(n.Symbol) {
		return invalidInputf("evidence node symbol 必填")
	}
	return nil
}

func validateEvidenceNodes(field string, nodes []EvidenceNode) error {
	for i, n := range nodes {
		if err := validateEvidenceNode(n); err != nil {
			return fmt.Errorf("%s[%d]: %w", field, i, err)
		}
	}
	return nil
}

func (r RecordStaticSupportRequest) validate() error {
	if trimEmpty(r.UnitID) {
		return invalidInputf("unit_id 必填")
	}
	if len(r.Facts) == 0 && len(r.Chain) == 0 {
		return invalidInputf("facts 与 chain 至少一项非空")
	}
	if err := validateEvidenceNodes("facts", r.Facts); err != nil {
		return err
	}
	return validateEvidenceNodes("chain", r.Chain)
}

func (r RecordCandidateRequest) validate() error {
	if trimEmpty(r.UnitID) {
		return invalidInputf("unit_id 必填")
	}
	if trimEmpty(r.SupportID) {
		return invalidInputf("support_id 必填")
	}
	pkg := r.Package
	if trimEmpty(pkg.Family.FamilyID) {
		return invalidInputf("package.family.family_id 必填")
	}
	if !pkg.Family.StorageClass.Valid() {
		return invalidInputf("package.family.storage_class 非法: %q", pkg.Family.StorageClass)
	}
	if !pkg.Family.LifetimeForm.Valid() {
		return invalidInputf("package.family.lifetime_form 非法: %q", pkg.Family.LifetimeForm)
	}
	if trimEmpty(pkg.DomainPairID) {
		return invalidInputf("package.domain_pair_id 必填（never null）")
	}
	if !pkg.PairBasis.Valid() {
		return invalidInputf("package.pair_basis 非法: %q", pkg.PairBasis)
	}
	if !pkg.DomainConfidence.Valid() {
		return invalidInputf("package.domain_confidence 非法: %q", pkg.DomainConfidence)
	}
	if !pkg.PrimaryForm.Valid() {
		return invalidInputf("package.primary_form 非法: %q", pkg.PrimaryForm)
	}
	for i, form := range pkg.ContributingForms {
		if !form.Valid() {
			return invalidInputf("package.contributing_forms[%d] 非法: %q", i, form)
		}
	}
	return nil
}

func (r RecordNoRiskRequest) validate() error {
	if trimEmpty(r.UnitID) {
		return invalidInputf("unit_id 必填")
	}
	if !Form(r.Form).Valid() {
		return invalidInputf("form 非法: %q", r.Form)
	}
	if len(r.Arguments) == 0 {
		return invalidInputf("arguments 至少一条")
	}
	for i, arg := range r.Arguments {
		if !arg.ArgumentType.Valid() {
			return invalidInputf("arguments[%d].argument_type 非法: %q", i, arg.ArgumentType)
		}
		if len(arg.CoversForms) == 0 {
			return invalidInputf("arguments[%d].covers_forms 至少一条", i)
		}
		for j, form := range arg.CoversForms {
			if !form.Valid() {
				return invalidInputf("arguments[%d].covers_forms[%d] 非法: %q", i, j, form)
			}
		}
	}
	return nil
}

func (r RecordUnknownRequest) validate() error {
	if trimEmpty(r.Scope) {
		return invalidInputf("scope 必填")
	}
	if !r.Reason.Valid() {
		return invalidInputf("reason 非法: %q", r.Reason)
	}
	if trimEmpty(r.Detail) {
		return invalidInputf("detail 必填")
	}
	return nil
}

func (r RecordNewLeadRequest) validate() error {
	if trimEmpty(r.Lead.SourceEvent) {
		return invalidInputf("lead.source_event 必填")
	}
	if trimEmpty(r.Lead.Reason) {
		return invalidInputf("lead.reason 必填")
	}
	switch r.Lead.Status {
	case "", "open", "triaged", "dismissed":
	default:
		return invalidInputf("lead.status 非法: %q", r.Lead.Status)
	}
	return nil
}

func (r RequestProbeRequest) validate() error {
	if trimEmpty(r.CandidateID) {
		return invalidInputf("candidate_id 必填")
	}
	if trimEmpty(r.Question) {
		return invalidInputf("question 必填")
	}
	if trimEmpty(r.ExpectedSignal) {
		return invalidInputf("expected_signal 必填")
	}
	return nil
}

func (r ClaimRequest) validate() error {
	if !r.QueueKind.Valid() {
		return invalidInputf("queue_kind 非法: %q", r.QueueKind)
	}
	if r.LeaseTTLSeconds <= 0 {
		return invalidInputf("lease_ttl_seconds 必须为正")
	}
	if err := validateClaimFilterRequest(r.QueueKind, r.Filter); err != nil {
		return err
	}
	return nil
}

func (r RenewRequest) validate() error {
	if trimEmpty(r.ItemID) {
		return invalidInputf("item_id 必填")
	}
	if trimEmpty(r.ClaimID) {
		return invalidInputf("claim_id 必填")
	}
	if r.LeaseTTLSeconds <= 0 {
		return invalidInputf("lease_ttl_seconds 必须为正")
	}
	return nil
}

func (r ReleaseRequest) validate() error {
	if trimEmpty(r.ItemID) {
		return invalidInputf("item_id 必填")
	}
	if trimEmpty(r.ClaimID) {
		return invalidInputf("claim_id 必填")
	}
	if trimEmpty(r.Reason) {
		return invalidInputf("reason 必填")
	}
	return nil
}

func (r CompleteRequest) validate() error {
	if trimEmpty(r.ItemID) {
		return invalidInputf("item_id 必填")
	}
	if trimEmpty(r.ClaimID) {
		return invalidInputf("claim_id 必填")
	}
	if trimEmpty(r.OutcomeRef) {
		return invalidInputf("outcome_ref 必填")
	}
	return nil
}

func (r PlannerPolicyUpdateRequest) validate() error {
	if len(r.Patch) == 0 {
		return invalidInputf("patch 不能为空")
	}
	if trimEmpty(r.Reason) {
		return invalidInputf("reason 必填")
	}
	return nil
}

func (r PlannerPinRequest) validate() error {
	if trimEmpty(r.ItemID) {
		return invalidInputf("item_id 必填")
	}
	if trimEmpty(r.Reason) {
		return invalidInputf("reason 必填（R-16.7）")
	}
	return nil
}

func (r PlannerCloseRequest) validate() error {
	if len(r.Plan) == 0 {
		return invalidInputf("plan 不能为空")
	}
	return nil
}

func (r RequestIndexRevisionRequest) validate() error {
	if trimEmpty(r.Scope) {
		return invalidInputf("scope 必填")
	}
	if trimEmpty(r.Reason) {
		return invalidInputf("reason 必填")
	}
	return nil
}

// validateClaimFilterRequest 以 queue.go 的 ValidateClaimFilter 为权威校验
// （R-16.6：允许键集合与未知 queue kind 由队列语义决定），并补充键/值非空的
// 轻量检查。
func validateClaimFilterRequest(k QueueKind, filter map[string]string) error {
	if err := ValidateClaimFilter(k, filter); err != nil {
		return fmt.Errorf("%w: %v", errToolInputInvalid, err)
	}
	for key, value := range filter {
		if trimEmpty(key) {
			return invalidInputf("filter 键不能为空")
		}
		if trimEmpty(value) {
			return invalidInputf("filter[%q] 值不能为空", key)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 入口
// ---------------------------------------------------------------------------

// decodeToolJSON 严格解码：拒绝未知字段与尾随内容，防止自由文本绕过 schema。
func decodeToolJSON(name string, raw []byte, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return invalidInputf("%s 缺少 JSON 输入", name)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: %s: %v", errToolInputInvalid, name, err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return invalidInputf("%s: 只允许单个 JSON 对象", name)
	}
	return nil
}

// ValidateToolInput 按工具名把 raw JSON 解码为对应的请求类型并做轻量校验。
//
// 已定义请求 schema 的工具（§16.2 发现工具、§16.5 队列/planner 工具）返回具体
// 请求类型；其余 §16 工具在本包尚无独立请求类型，按 JSON 对象解码为
// map[string]any 返回（仍拒绝畸形 JSON；参数语义由对应 service 层校验）。
func ValidateToolInput(name string, raw []byte) (any, error) {
	if _, ok := ToolGroupOf(name); !ok {
		return nil, fmt.Errorf("%w: %q", errToolSurfaceUnknownTool, name)
	}

	switch name {
	case ToolRecordStaticSupport:
		var req RecordStaticSupportRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	case ToolRecordCandidate:
		var req RecordCandidateRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	case ToolRecordNoRisk:
		var req RecordNoRiskRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	case ToolRecordUnknown:
		var req RecordUnknownRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	case ToolRecordNewLead:
		var req RecordNewLeadRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	case ToolRequestProbe:
		var req RequestProbeRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	case ToolClaim:
		var req ClaimRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	case ToolRenew:
		var req RenewRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	case ToolRelease:
		var req ReleaseRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	case ToolComplete:
		var req CompleteRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	case ToolPlannerPolicyUpdate:
		var req PlannerPolicyUpdateRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	case ToolPlannerPin:
		var req PlannerPinRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	case ToolPlannerClose:
		var req PlannerCloseRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	case ToolRequestIndexRevision:
		var req RequestIndexRevisionRequest
		if err := decodeToolJSON(name, raw, &req); err != nil {
			return nil, err
		}
		if err := req.validate(); err != nil {
			return nil, err
		}
		return req, nil
	default:
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			return map[string]any{}, nil
		}
		var m map[string]any
		if err := json.Unmarshal(trimmed, &m); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", errToolInputInvalid, name, err)
		}
		if m == nil {
			m = map[string]any{}
		}
		return m, nil
	}
}
