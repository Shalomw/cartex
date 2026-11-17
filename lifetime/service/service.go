// Package service 暴露 lifetime 领域的工具面（§16.2/§16.5）为独立 HTTP 服务。
//
// 该服务是 R-15.1 的“独立服务”：不把领域能力塞进 ARTEX 的 planner/agent 循环。
// 角色与工具面由 lifetime.ValidateToolCall 强制（§16.6/R-16.9）；M0 阶段信任
// X-Lifetime-Role / X-Lifetime-Session 头（部署在受信网络或反向代理之后），
// 与 ARTEX agent 会话的签名绑定是后续集成项。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/lifetime"
)

// Backend 是服务依赖的持久化接口，由 lifetime/store 实现。
type Backend interface {
	RecordStaticSupport(ctx context.Context, taskID string, s lifetime.StaticSupport) (string, error)
	RecordCandidate(ctx context.Context, taskID string, c lifetime.Candidate) (string, error)
	RecordNoRisk(ctx context.Context, taskID string, n lifetime.NoRisk) (string, error)
	RecordUnknown(ctx context.Context, taskID string, u lifetime.UnknownRecord) (string, error)
	RecordNewLead(ctx context.Context, taskID string, l lifetime.NewLead) (string, error)
	RequestProbe(ctx context.Context, taskID, candidateID, question, expectedSignal string) (string, error)

	Claim(ctx context.Context, taskID string, q lifetime.QueueKind, sessionID string, ttl time.Duration) (*lifetime.WorkItem, error)
	Renew(ctx context.Context, itemID, claimID, sessionID string, ttl time.Duration) (*lifetime.WorkItem, error)
	Release(ctx context.Context, itemID, claimID, sessionID, reason string) error
	Complete(ctx context.Context, itemID, claimID, sessionID, outcomeRef string) error

	GetCandidate(ctx context.Context, candidateID string) (*lifetime.Candidate, error)
	GetStaticSupport(ctx context.Context, supportID string) (*lifetime.StaticSupport, error)
	GetCoverage(ctx context.Context, unitID string) (*lifetime.CoverageUnit, error)
	GetCoverageForTask(ctx context.Context, taskID, unitID string) (*lifetime.CoverageUnit, error)
	GetIndexOutput(ctx context.Context, taskID, lineageID string) (*lifetime.IndexOutput, error)
	ListObservations(ctx context.Context, candidateID string) ([]lifetime.Observation, error)
	ListExperiments(ctx context.Context, candidateID string) ([]lifetime.Experiment, error)
	MaterializeAssessment(ctx context.Context, taskID string, a lifetime.Assessment) error

	SnapshotForReconcile(ctx context.Context, taskID string) (*lifetime.ReconcileSnapshot, error)
	SaveReconciliation(ctx context.Context, taskID string, run lifetime.ReconciliationRun, findings []lifetime.ReconciliationFinding) error

	// 索引/bootstrap 与查询面（§8.1/§16.1）。
	CreateLineage(ctx context.Context, taskID string, in lifetime.LineageInput) (string, error)
	RegisterTaskProfile(ctx context.Context, taskID string, p *lifetime.TaskProfile) (string, error)
	IngestIndex(ctx context.Context, taskID, lineageID string, idx lifetime.IndexOutput) error
	InitCoverage(ctx context.Context, taskID, lineageID string, units []lifetime.CoverageUnit, packages []lifetime.WorkPackage) error
	ListFamilies(ctx context.Context, taskID string) ([]lifetime.ObjectFamily, error)
	GetFamilyDetail(ctx context.Context, taskID, familyID string) (*lifetime.FamilyDetail, error)
	ListCoverage(ctx context.Context, taskID string) ([]lifetime.CoverageUnit, error)
	ListCandidates(ctx context.Context, taskID string) ([]lifetime.Candidate, error)
	ListAssessments(ctx context.Context, taskID string) ([]lifetime.Assessment, error)
}

// Options 是服务配置。
type Options struct {
	// DefaultLeaseTTL 在请求未指定 lease 时使用；缺省按 §14.6。
	DefaultLeaseTTL map[lifetime.QueueKind]time.Duration
	// SchedulerSeed 写入 reconciliation run 的调度回放元数据。
	SchedulerSeed int64
	// MaxAttempts 租约过期上限，缺省 3（§14.6）。
	MaxAttempts int
	// Profile 是当前任务 profile（可选）；非 nil 时通过 GET /v1/profile 暴露。
	Profile *lifetime.TaskProfile
	// PlatformPolicy 是 S3/S4 的平台策略；nil 时使用 native/emulated + medium。
	PlatformPolicy *lifetime.PlatformPolicy
}

// Server 是 lifetime 工具面 HTTP 服务。
type Server struct {
	backend Backend
	opts    Options
}

// New 构造服务。backend 不能为 nil。
func New(backend Backend, opts Options) (*Server, error) {
	if backend == nil {
		return nil, errors.New("lifetime service: nil backend")
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}
	if len(opts.DefaultLeaseTTL) == 0 {
		opts.DefaultLeaseTTL = map[lifetime.QueueKind]time.Duration{
			lifetime.QueueUnitReadyDiscover:    30 * time.Minute,
			lifetime.QueueCandidateReadyVerify: 60 * time.Minute,
			lifetime.QueueClaimReadyReview:     60 * time.Minute,
			lifetime.QueueConfirmedReadyReport: 15 * time.Minute,
			lifetime.QueueRepairTask:           30 * time.Minute,
		}
	}
	return &Server{backend: backend, opts: opts}, nil
}

// Handler 返回 HTTP 路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/profile", s.profile)
	mux.HandleFunc("GET /v1/roles", s.roles)
	mux.HandleFunc("POST /v1/tasks/{taskID}/tools/{tool}", s.tool)
	mux.HandleFunc("POST /v1/tasks/{taskID}/candidates/{candidateID}/assess", s.assess)
	mux.HandleFunc("POST /v1/tasks/{taskID}/reconcile", s.reconcile)
	// 索引/bootstrap 与发现查询面（§8.1/§16.1）。
	mux.HandleFunc("POST /v1/tasks/{taskID}/bootstrap", s.bootstrap)
	mux.HandleFunc("GET /v1/tasks/{taskID}/families", s.families)
	mux.HandleFunc("GET /v1/tasks/{taskID}/families/{familyID}", s.familyDetail)
	mux.HandleFunc("GET /v1/tasks/{taskID}/coverage", s.coverage)
	mux.HandleFunc("GET /v1/tasks/{taskID}/candidates", s.candidates)
	return mux
}

// ToolResponse 是统一的工具返回（与 lifetime/tools.go 的 ToolResponse 对齐）。
type ToolResponse = lifetime.ToolResponse

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "lifetimed"})
}

func (s *Server) profile(w http.ResponseWriter, _ *http.Request) {
	if s.opts.Profile == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profile": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profile": s.opts.Profile})
}

func (s *Server) roles(w http.ResponseWriter, _ *http.Request) {
	type item struct {
		Role    lifetime.Role        `json:"role"`
		Agent   string               `json:"agent_key"`
		Surface lifetime.RoleSurface `json:"surface"`
	}
	var out []item
	for _, r := range lifetime.AllRoles() {
		spec, _ := lifetime.RoleSpecFor(r)
		out = append(out, item{Role: r, Agent: lifetime.AgentKeyForRole(r), Surface: spec.Surface})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "roles": out})
}

func (s *Server) tool(w http.ResponseWriter, r *http.Request) {
	role, session, err := envelope(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "bad_envelope", err)
		return
	}
	tool := r.PathValue("tool")
	if err := lifetime.ValidateToolCall(role, tool); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", err)
		return
	}
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err)
		return
	}
	taskID := r.PathValue("taskID")
	ctx := r.Context()

	switch tool {
	case lifetime.ToolRecordStaticSupport:
		req, err := decode[lifetime.RecordStaticSupportRequest](body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err)
			return
		}
		now := time.Now().UTC()
		sup := lifetime.StaticSupport{
			UnitID: req.UnitID, Facts: req.Facts, Chain: req.Chain,
			CitationChecks: req.CitationChecks,
			CreatedBy:      session, CreatedAt: now,
		}
		id, err := s.backend.RecordStaticSupport(ctx, taskID, sup)
		s.respond(w, id, err)

	case lifetime.ToolRecordCandidate:
		req, err := decode[lifetime.RecordCandidateRequest](body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err)
			return
		}
		now := time.Now().UTC()
		c := lifetime.Candidate{
			UnitID: req.UnitID, SupportID: req.SupportID,
			CandidatePackage: req.Package,
			CreatedAt:        now, UpdatedAt: now,
		}
		id, err := s.backend.RecordCandidate(ctx, taskID, c)
		s.respond(w, id, err)

	case lifetime.ToolRecordNoRisk:
		// 文档签名是 record_no_risk(unit_id, form, arguments[])。family_id /
		// domain_pair_id 由平台从 unit_id 解析；M0 允许调用方显式附带，
		// 便于 store 在解析器落地前先落库。
		var req struct {
			UnitID       string                    `json:"unit_id"`
			Form         string                    `json:"form"`
			Arguments    []lifetime.NoRiskArgument `json:"arguments"`
			FamilyID     string                    `json:"family_id"`
			DomainPairID string                    `json:"domain_pair_id"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err)
			return
		}
		if !lifetime.Form(req.Form).Valid() {
			writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("invalid form %q", req.Form))
			return
		}
		n := lifetime.NoRisk{
			UnitID:   req.UnitID,
			FamilyID: req.FamilyID, DomainPairID: req.DomainPairID, Form: lifetime.Form(req.Form),
			Arguments: req.Arguments, CreatedBy: session, CreatedAt: time.Now().UTC(),
			ReviewStatus: "self_asserted",
		}
		id, err := s.backend.RecordNoRisk(ctx, taskID, n)
		s.respond(w, id, err)

	case lifetime.ToolRecordUnknown:
		req, err := decode[lifetime.RecordUnknownRequest](body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err)
			return
		}
		if !req.Reason.Valid() {
			writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("invalid unknown reason %q", req.Reason))
			return
		}
		id, err := s.backend.RecordUnknown(ctx, taskID, lifetime.UnknownRecord{
			Scope: req.Scope, Reason: req.Reason, Detail: req.Detail,
			CreatedAt: time.Now().UTC(),
		})
		s.respond(w, id, err)

	case lifetime.ToolRecordNewLead:
		req, err := decode[lifetime.RecordNewLeadRequest](body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err)
			return
		}
		req.Lead.Status = "open"
		req.Lead.CreatedAt = time.Now().UTC()
		id, err := s.backend.RecordNewLead(ctx, taskID, req.Lead)
		s.respond(w, id, err)

	case lifetime.ToolRequestProbe:
		req, err := decode[lifetime.RequestProbeRequest](body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err)
			return
		}
		id, err := s.backend.RequestProbe(ctx, taskID, req.CandidateID, req.Question, req.ExpectedSignal)
		s.respond(w, id, err)

	case lifetime.ToolClaim:
		req, err := decode[lifetime.ClaimRequest](body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err)
			return
		}
		if !lifetime.CanClaimQueue(role, req.QueueKind) {
			writeError(w, http.StatusForbidden, "forbidden", fmt.Errorf("role %s cannot claim %s", role, req.QueueKind))
			return
		}
		if err := lifetime.ValidateClaimFilter(req.QueueKind, req.Filter); err != nil {
			writeError(w, http.StatusBadRequest, "bad_filter", err)
			return
		}
		ttl := s.ttlFor(req.QueueKind, req.LeaseTTLSeconds)
		item, err := s.backend.Claim(ctx, taskID, req.QueueKind, session, ttl)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "claim_failed", err)
			return
		}
		writeJSON(w, http.StatusOK, ToolResponse{OK: true, Result: item})

	case lifetime.ToolRenew:
		req, err := decode[lifetime.RenewRequest](body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err)
			return
		}
		ttl := s.ttlFor(lifetime.QueueCandidateReadyVerify, req.LeaseTTLSeconds)
		item, err := s.backend.Renew(ctx, req.ItemID, req.ClaimID, session, ttl)
		if err != nil {
			writeError(w, http.StatusConflict, "renew_failed", err)
			return
		}
		writeJSON(w, http.StatusOK, ToolResponse{OK: true, Result: item})

	case lifetime.ToolRelease:
		req, err := decode[lifetime.ReleaseRequest](body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err)
			return
		}
		if err := s.backend.Release(ctx, req.ItemID, req.ClaimID, session, req.Reason); err != nil {
			writeError(w, http.StatusConflict, "release_failed", err)
			return
		}
		writeJSON(w, http.StatusOK, ToolResponse{OK: true})

	case lifetime.ToolComplete:
		req, err := decode[lifetime.CompleteRequest](body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err)
			return
		}
		if err := s.backend.Complete(ctx, req.ItemID, req.ClaimID, session, req.OutcomeRef); err != nil {
			writeError(w, http.StatusConflict, "complete_failed", err)
			return
		}
		writeJSON(w, http.StatusOK, ToolResponse{OK: true})

	case lifetime.ToolPlannerPolicyUpdate, lifetime.ToolPlannerPin,
		lifetime.ToolPlannerClose, lifetime.ToolRequestIndexRevision,
		lifetime.ToolBuildDriver, lifetime.ToolSubmitVerification,
		lifetime.ToolJobStatus, lifetime.ToolReproduce, lifetime.ToolAssessImpact,
		lifetime.ToolSubmitReviewEvidence, lifetime.ToolReviewClaim,
		lifetime.ToolRequestNewDriver, lifetime.ToolReportFinding,
		lifetime.ToolUpdateFindingReport:
		// P3/P4：Driver harness、复核与报告服务尚未在本里程碑落地。
		writeError(w, http.StatusNotImplemented, "not_implemented",
			fmt.Errorf("%s 属于 P3/P4 组件，M0 只提供发现/覆盖/队列/对账竖切", tool))

	default:
		writeError(w, http.StatusBadRequest, "unsupported_tool", fmt.Errorf("tool %q 不属于服务端可执行工具（文件/查询工具由 agent runtime 注入）", tool))
	}
}

// assess 是平台评估入口：从已落库的 index / static support / candidate /
// evidence 派生 S1–S4，物化 Assessment，并自动投影 Q2/Q3/Q4（R-6.9）。
// 只有 planner / reconciler 可以触发；agent 不能自行写 level（I-2）。
func (s *Server) assess(w http.ResponseWriter, r *http.Request) {
	role, _, err := envelope(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "bad_envelope", err)
		return
	}
	if role != lifetime.RolePlanner && role != lifetime.RoleReconciler {
		writeError(w, http.StatusForbidden, "forbidden", fmt.Errorf("role %s cannot materialize assessments", role))
		return
	}
	taskID := r.PathValue("taskID")
	candidateID := r.PathValue("candidateID")
	ctx := r.Context()

	// 可选 body：{"lineage_id": "..."}；缺省从候选的覆盖单位解析。
	var req struct {
		LineageID string `json:"lineage_id"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
		r.Body.Close()
	}

	cand, err := s.backend.GetCandidate(ctx, candidateID)
	if err != nil {
		writeError(w, http.StatusNotFound, "candidate_not_found", err)
		return
	}
	if cand.SupportID == "" {
		writeError(w, http.StatusBadRequest, "missing_support", errors.New("candidate has no static support"))
		return
	}
	support, err := s.backend.GetStaticSupport(ctx, cand.SupportID)
	if err != nil {
		writeError(w, http.StatusNotFound, "support_not_found", err)
		return
	}
	lineageID := req.LineageID
	if lineageID == "" && cand.UnitID != "" {
		if unit, err := s.backend.GetCoverageForTask(ctx, taskID, cand.UnitID); err == nil {
			lineageID = unit.LineageID
		}
	}
	if lineageID == "" {
		writeError(w, http.StatusBadRequest, "missing_lineage",
			errors.New("cannot resolve lineage_id; pass {\"lineage_id\":...} or record the candidate against a coverage unit"))
		return
	}
	idx, err := s.backend.GetIndexOutput(ctx, taskID, lineageID)
	if err != nil {
		writeError(w, http.StatusNotFound, "index_not_found", err)
		return
	}
	exps, err := s.backend.ListExperiments(ctx, candidateID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_experiments_failed", err)
		return
	}
	obs, err := s.backend.ListObservations(ctx, candidateID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_observations_failed", err)
		return
	}
	profile := s.opts.Profile
	if profile == nil {
		profile = lifetime.DefaultTaskProfile()
	}
	policy := lifetime.PlatformPolicy{
		S3Allowed:  []lifetime.Platform{lifetime.PlatformNative, lifetime.PlatformEmulated},
		MinQuality: lifetime.PlatformQualityMedium,
	}
	if s.opts.PlatformPolicy != nil {
		policy = *s.opts.PlatformPolicy
	}

	ev := lifetime.EvaluateAssessment(cand, support, idx, exps, obs, policy, profile, lifetime.Review{})
	if ev.Level == "" && !ev.Provisional {
		writeJSON(w, http.StatusOK, ToolResponse{OK: true, Result: map[string]any{
			"evaluation": ev,
			"assessment": nil,
			"created":    false,
		}})
		return
	}
	level := ev.Level
	if level == "" {
		level = lifetime.LevelS1 // provisional 不是 claim 等级，单独用 Provisional 表达
	}
	a := lifetime.Assessment{
		AssessmentID:       lifetime.NewID("asm"),
		CandidateID:        cand.CandidateID,
		CandidateKey:       cand.CandidateKey,
		LineageID:          lineageID,
		LineageRole:        lifetime.LineageRoleClaim,
		Level:              level,
		Provisional:        ev.Provisional,
		Confidence:         ev.Confidence,
		ReportableInternal: ev.ReportableInternal,
		ReportableExternal: ev.ReportableExternal,
		Status:             lifetime.AssessmentOpen,
		DerivedAt:          time.Now().UTC(),
	}
	if err := s.backend.MaterializeAssessment(ctx, taskID, a); err != nil {
		writeError(w, http.StatusInternalServerError, "materialize_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, ToolResponse{OK: true, Result: map[string]any{
		"evaluation": ev,
		"assessment": a,
		"created":    true,
	}})
}

func (s *Server) reconcile(w http.ResponseWriter, r *http.Request) {
	role, session, err := envelope(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "bad_envelope", err)
		return
	}
	if role != lifetime.RoleReconciler && role != lifetime.RolePlanner {
		writeError(w, http.StatusForbidden, "forbidden", fmt.Errorf("role %s cannot run reconciliation", role))
		return
	}
	taskID := r.PathValue("taskID")
	ctx := r.Context()
	snap, err := s.backend.SnapshotForReconcile(ctx, taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "snapshot_failed", err)
		return
	}
	if snap.Now.IsZero() {
		snap.Now = time.Now().UTC()
	}
	start := snap.Now
	findings := lifetime.RunChecks(*snap, s.opts.MaxAttempts)
	run := lifetime.ReconciliationRun{
		SweepID: lifetime.NewID("sweep"), Mode: "incremental",
		Checks: 12, Findings: len(findings), Repairs: countAutoRepairs(findings),
		StartedAt: start, FinishedAt: time.Now().UTC(),
	}
	if err := s.backend.SaveReconciliation(ctx, taskID, run, findings); err != nil {
		writeError(w, http.StatusInternalServerError, "save_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, ToolResponse{OK: true, Result: map[string]any{
		"sweep_id": run.SweepID,
		"sweep":    run,
		"findings": findings,
		"acted_by": session,
	}})
}

func countAutoRepairs(fs []lifetime.ReconciliationFinding) int {
	n := 0
	for _, f := range fs {
		switch f.Action {
		case lifetime.ActionRequeue, lifetime.ActionMarkStale, lifetime.ActionCreateRepairTask, lifetime.ActionMerge:
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// 发现查询面（§16.1）：对象族 / 单族详情 / 覆盖 / 候选
//
// 读端点只要求合法 envelope（任意角色）；角色矩阵中的“读”列对全部角色开放。
// ---------------------------------------------------------------------------

// families 列出 task 的对象族，支持按 file/subdir 过滤与 limit 截断。
//
//	GET /v1/tasks/{taskID}/families?file=&subdir=&limit=
//
// file 对 alloc_site 与 type_canonical 做子串匹配；subdir 对 alloc_site 做
// 路径前缀匹配；limit 缺省 100，上限 500。
func (s *Server) families(w http.ResponseWriter, r *http.Request) {
	if _, _, err := envelope(r); err != nil {
		writeError(w, http.StatusUnauthorized, "bad_envelope", err)
		return
	}
	taskID := r.PathValue("taskID")
	all, err := s.backend.ListFamilies(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_families_failed", err)
		return
	}

	q := r.URL.Query()
	file := q.Get("file")
	subdir := q.Get("subdir")
	limit := 100
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("limit must be a positive integer, got %q", raw))
			return
		}
		if n > 500 {
			n = 500
		}
		limit = n
	}

	out := make([]lifetime.ObjectFamily, 0, limit)
	for _, fam := range all {
		if file != "" && !strings.Contains(fam.AllocSite, file) && !strings.Contains(fam.TypeCanonical, file) {
			continue
		}
		if subdir != "" && !allocSiteInSubdir(fam.AllocSite, subdir) {
			continue
		}
		out = append(out, fam)
		if len(out) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"families": out})
}

// allocSiteInSubdir 判断 alloc_site 的文件部分是否位于 subdir 下。
// alloc_site 形如 "src/objects/foo.cc:42 Function()"；比较前去掉 "./" 与前导 "/"，
// 既接受相对 SourceRoot 的路径，也接受带绝对前缀的路径（边界对齐的 contains）。
func allocSiteInSubdir(allocSite, subdir string) bool {
	site := strings.TrimPrefix(strings.TrimPrefix(allocSite, "./"), "/")
	sub := strings.Trim(strings.TrimPrefix(subdir, "./"), "/")
	if sub == "" {
		return true
	}
	if site == sub || strings.HasPrefix(site, sub+"/") || strings.HasPrefix(site, sub+":") {
		return true
	}
	return strings.Contains(site, "/"+sub+"/")
}

// familyDetail 返回单族视图；族不存在时 404。
//
//	GET /v1/tasks/{taskID}/families/{familyID}
func (s *Server) familyDetail(w http.ResponseWriter, r *http.Request) {
	if _, _, err := envelope(r); err != nil {
		writeError(w, http.StatusUnauthorized, "bad_envelope", err)
		return
	}
	detail, err := s.backend.GetFamilyDetail(r.Context(), r.PathValue("taskID"), r.PathValue("familyID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "family_not_found", err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// coverage 返回覆盖统计与账本行。
//
//	GET /v1/tasks/{taskID}/coverage
func (s *Server) coverage(w http.ResponseWriter, r *http.Request) {
	if _, _, err := envelope(r); err != nil {
		writeError(w, http.StatusUnauthorized, "bad_envelope", err)
		return
	}
	units, err := s.backend.ListCoverage(r.Context(), r.PathValue("taskID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_coverage_failed", err)
		return
	}
	if units == nil {
		units = []lifetime.CoverageUnit{}
	}
	total := len(units)
	stats := lifetime.ComputeCoverageStats(units, 0)
	// 账本可能有数万行；默认只回前 200 行，stats 始终按全量计算。
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if n > 5000 {
				n = 5000
			}
			limit = n
		}
	}
	if len(units) > limit {
		units = units[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stats": stats, "units": units, "total_units": total, "limit": limit,
	})
}

// candidates 列出 task 的候选。
//
//	GET /v1/tasks/{taskID}/candidates
func (s *Server) candidates(w http.ResponseWriter, r *http.Request) {
	if _, _, err := envelope(r); err != nil {
		writeError(w, http.StatusUnauthorized, "bad_envelope", err)
		return
	}
	cands, err := s.backend.ListCandidates(r.Context(), r.PathValue("taskID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_candidates_failed", err)
		return
	}
	if cands == nil {
		cands = []lifetime.Candidate{}
	}
	// 附带每个 candidate 的最新 assessment（按 DerivedAt 取最新），
	// 供 agent/评分器直接看 S1–S4 与 review 状态，而不用再查一次。
	latest := map[string]lifetime.Assessment{}
	if assessments, err := s.backend.ListAssessments(r.Context(), r.PathValue("taskID")); err == nil {
		for _, a := range assessments {
			if cur, ok := latest[a.CandidateID]; !ok || a.DerivedAt.After(cur.DerivedAt) {
				latest[a.CandidateID] = a
			}
		}
	}
	type candidateView struct {
		lifetime.Candidate
		Assessment *lifetime.Assessment `json:"assessment,omitempty"`
	}
	views := make([]candidateView, 0, len(cands))
	for _, c := range cands {
		v := candidateView{Candidate: c}
		if a, ok := latest[c.CandidateID]; ok {
			v.Assessment = &a
		}
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": views, "assessments": latest})
}

func (s *Server) ttlFor(q lifetime.QueueKind, seconds int) time.Duration {
	if seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if d, ok := s.opts.DefaultLeaseTTL[q]; ok && d > 0 {
		return d
	}
	return 60 * time.Minute
}

func (s *Server) respond(w http.ResponseWriter, id string, err error) {
	if err != nil {
		writeError(w, http.StatusBadRequest, "rejected", err)
		return
	}
	writeJSON(w, http.StatusOK, ToolResponse{OK: true, Result: map[string]string{"id": id}})
}

func envelope(r *http.Request) (lifetime.Role, string, error) {
	role := lifetime.Role(r.Header.Get("X-Lifetime-Role"))
	if !role.Valid() {
		return "", "", fmt.Errorf("invalid or missing X-Lifetime-Role %q", role)
	}
	session := r.Header.Get("X-Lifetime-Session")
	if session == "" {
		return "", "", errors.New("missing X-Lifetime-Session")
	}
	return role, session, nil
}

func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	var buf json.RawMessage
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	if err := dec.Decode(&buf); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	return buf, nil
}

func decode[T any](raw []byte) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, errors.New("empty body")
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("decode json: %w", err)
	}
	return v, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[lifetimed] encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	writeJSON(w, status, ToolResponse{OK: false, Error: msg, ErrorCode: code})
}
