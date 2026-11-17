package lifetime

import (
	"errors"
	"fmt"
	"strings"
)

// 本文件实现 §5.1 的角色定义与 §16.6 的角色工具面矩阵。
//
// 矩阵是硬约束（R-16.9）：角色只能调用其工具面允许的工具组；工具名到工具组
// 的映射在 tools.go 的注册表中，角色不得通过 MCP/自定义工具绕过。产物所有权
// 见 §5.3；平台写 evidence，agent 不得写 level/confidence/reportable（R-5.12）。

// ToolGroup 是 §16.6 角色工具面矩阵的行（工具组）。
type ToolGroup string

const (
	ToolGroupFileQuery            ToolGroup = "file_query"
	ToolGroupDiscoveryWrite       ToolGroup = "discovery_write"
	ToolGroupBuildSubmitReproduce ToolGroup = "build_submit_reproduce"
	ToolGroupReviewEvidence       ToolGroup = "review_evidence"
	ToolGroupReport               ToolGroup = "report"
	ToolGroupQueueClaim           ToolGroup = "queue_claim"
	ToolGroupPlannerPolicy        ToolGroup = "planner_policy"
	ToolGroupOwnerJournal         ToolGroup = "owner_journal"
	ToolGroupReconciliationWrite  ToolGroup = "reconciliation_write"
)

// ToolGroupKind 是 ToolGroup 的别名，供注册表/审计记录标注“工具组别”语义。
type ToolGroupKind = ToolGroup

// RoleSurface 是 §16.6 矩阵中一个角色对应的一行。
//
// 字段与矩阵单元格一一对应。矩阵存在“是 / 读 / 否”三态，bool 只能表达
// 两态，因此读权限用单独的 *Read 字段精确表达：
//
//   - FileQuery 表示可访问文件/查询/图谱工具；FileQueryReadOnly 记录矩阵标“读”
//     的单元格（reporter/reconciler）。§16.1 的工具本身都是只读的，该位用于
//     忠实保留矩阵区分。
//   - OwnerJournal 表示可写 owner journal（owner）；OwnerJournalRead 表示只读
//     （planner/reconciler）。owner 的写权限蕴含读，故 owner 两位均为 true。
//   - BuildDriverSandboxOnly 表示 build_driver 只在 review sandbox 注入
//     （reviewer，R-5.7/R-16.10）；它不限制组内 submit/reproduce 等其他工具。
//   - ReadOnly 仅指“不写 discovery/evidence 产物”，即 !DiscoveryWrite &&
//     !ReviewEvidence；它不表示不能写 DriverSpec、finding 报告、owner journal、
//     队列或对账发现。该定义与 §5.2 R-5.8/R-5.9 对齐。
type RoleSurface struct {
	FileQuery              bool
	FileQueryReadOnly      bool
	DiscoveryWrite         bool
	BuildSubmitReproduce   bool
	BuildDriverSandboxOnly bool
	ReviewEvidence         bool
	Report                 bool
	QueueClaim             []QueueKind
	PlannerPolicy          bool
	OwnerJournal           bool
	OwnerJournalRead       bool
	ReconciliationWrite    bool
	ReadOnly               bool
}

// RoleSpec 是 §5.1 的角色定义。
type RoleSpec struct {
	Role      Role
	Name      string
	Duty      string
	Artifacts []string
	Forbidden []string
	Surface   RoleSurface
}

// allRoleOrder 是 §5.1 表格的固定顺序。
var allRoleOrder = []Role{
	RoleLifetimeAuditor,
	RoleLifetimeVerifier,
	RoleLifetimeReviewer,
	RoleReporter,
	RolePlanner,
	RoleOwner,
	RoleReconciler,
	RoleMainAgent,
}

// roleSpecTable 严格取自 §5.1 的“职责 / 主要产物 / 禁止”三列。
var roleSpecTable = map[Role]RoleSpec{
	RoleLifetimeAuditor: {
		Role: RoleLifetimeAuditor,
		Name: string(RoleLifetimeAuditor),
		Duty: "静态发现：family、生命周期事实、竞争访问、同步缺口、lifecycle visibility；S1/S2",
		Artifacts: []string{
			"static_support",
			"candidate",
			"no_risk",
			"unknown",
			"new_lead",
		},
		Forbidden: []string{
			"无 build/run/Bash",
			"不能执行 probe",
			"不能跨 unit 写候选",
		},
	},
	RoleLifetimeVerifier: {
		Role: RoleLifetimeVerifier,
		Name: string(RoleLifetimeVerifier),
		Duty: "DriverSpec、编译、提交作业、搜索、复现、影响",
		Artifacts: []string{
			"DriverSpec",
			"job",
			"impact",
		},
		Forbidden: []string{
			"无 raw Bash",
			"源码只读",
			"不能写 evidence level",
		},
	},
	RoleLifetimeReviewer: {
		Role: RoleLifetimeReviewer,
		Name: string(RoleLifetimeReviewer),
		Duty: "独立复核：反证、新驱动、独立复现",
		Artifacts: []string{
			"review_evidence",
			"review",
		},
		Forbidden: []string{
			"无源码编辑",
			"不能在主工作区构建",
		},
	},
	RoleReporter: {
		Role: RoleReporter,
		Name: string(RoleReporter),
		Duty: "报告",
		Artifacts: []string{
			"finding",
		},
		Forbidden: []string{
			"不能改证据等级",
		},
	},
	RolePlanner: {
		Role: RolePlanner,
		Name: string(RolePlanner),
		Duty: "预算、全局优先级、跨 unit 依赖、收尾、例外",
		Artifacts: []string{
			"PlannerPolicy",
			"ClosingPlan",
			"ExceptionDecision",
		},
		Forbidden: []string{
			"不直接执行",
			"不得绕过门禁",
			"queue 模式下不得逐项派活",
		},
	},
	RoleOwner: {
		Role: RoleOwner,
		Name: string(RoleOwner),
		Duty: "常驻 area 负责人；维护 area journal；在本 area 内优先 claim",
		Artifacts: []string{
			"owner_journal",
			"area_summary",
		},
		Forbidden: []string{
			"不得绕过队列/门禁",
			"不得写 evidence",
		},
	},
	RoleReconciler: {
		Role: RoleReconciler,
		Name: string(RoleReconciler),
		Duty: "对账与修复任务；处理 orphan/stale/queue health",
		Artifacts: []string{
			"reconciliation_finding",
			"repair_task",
		},
		Forbidden: []string{
			"不得写 evidence",
			"不得改 level/reportable",
		},
	},
	RoleMainAgent: {
		Role: RoleMainAgent,
		Name: string(RoleMainAgent),
		Duty: "人在环路：授权、约束、目标、例外",
		Artifacts: []string{
			"AuthorizationDecision",
			"Goal",
			"Constraint",
		},
		Forbidden: []string{
			"不得替 agent 写证据",
		},
	},
}

// toolSurfaceMatrix 严格取自 §16.6 矩阵，各列顺序为 auditor、verifier、
// reviewer、reporter、planner、owner、reconciler。
//
// mainagent 不在 §16.6 矩阵内：它是人在环路角色（§5.5），其动作是授权/目标/
// 约束等平台 API，不是 §16 的 worker 工具，因此不注入任何工具组。
var toolSurfaceMatrix = map[Role]RoleSurface{
	RoleLifetimeAuditor: {
		FileQuery:            true,
		DiscoveryWrite:       true,
		QueueClaim:           []QueueKind{QueueUnitReadyDiscover, QueueRepairTask},
		BuildSubmitReproduce: false,
		ReviewEvidence:       false,
		Report:               false,
		PlannerPolicy:        false,
		OwnerJournal:         false,
		ReconciliationWrite:  false,
		ReadOnly:             false,
	},
	RoleLifetimeVerifier: {
		FileQuery:              true,
		DiscoveryWrite:         false,
		BuildSubmitReproduce:   true,
		BuildDriverSandboxOnly: false,
		ReviewEvidence:         false,
		Report:                 false,
		QueueClaim:             []QueueKind{QueueCandidateReadyVerify, QueueRepairTask},
		PlannerPolicy:          false,
		OwnerJournal:           false,
		OwnerJournalRead:       false,
		ReconciliationWrite:    false,
		ReadOnly:               true,
	},
	RoleLifetimeReviewer: {
		FileQuery:              true,
		DiscoveryWrite:         false,
		BuildSubmitReproduce:   true,
		BuildDriverSandboxOnly: true,
		ReviewEvidence:         true,
		Report:                 false,
		QueueClaim:             []QueueKind{QueueClaimReadyReview, QueueRepairTask},
		PlannerPolicy:          false,
		OwnerJournal:           false,
		OwnerJournalRead:       false,
		ReconciliationWrite:    false,
		ReadOnly:               false,
	},
	RoleReporter: {
		FileQuery:              true,
		FileQueryReadOnly:      true, // 矩阵标“读”
		DiscoveryWrite:         false,
		BuildSubmitReproduce:   false,
		BuildDriverSandboxOnly: false,
		ReviewEvidence:         false,
		Report:                 true,
		QueueClaim:             []QueueKind{QueueConfirmedReadyReport, QueueRepairTask},
		PlannerPolicy:          false,
		OwnerJournal:           false,
		OwnerJournalRead:       false,
		ReconciliationWrite:    false,
		ReadOnly:               true,
	},
	RolePlanner: {
		FileQuery:              true,
		DiscoveryWrite:         false,
		BuildSubmitReproduce:   false,
		BuildDriverSandboxOnly: false,
		ReviewEvidence:         false,
		Report:                 false,
		QueueClaim:             nil, // 矩阵：queue claim 否（R-5.3 不得替其他角色 claim）
		PlannerPolicy:          true,
		OwnerJournal:           false,
		OwnerJournalRead:       true, // 矩阵标“读”
		ReconciliationWrite:    false,
		ReadOnly:               true,
	},
	RoleOwner: {
		FileQuery:              true,
		DiscoveryWrite:         true,
		BuildSubmitReproduce:   false,
		BuildDriverSandboxOnly: false,
		ReviewEvidence:         false,
		Report:                 false,
		QueueClaim:             []QueueKind{QueueUnitReadyDiscover, QueueRepairTask},
		PlannerPolicy:          false,
		OwnerJournal:           true,
		OwnerJournalRead:       true, // 写权限蕴含读
		ReconciliationWrite:    false,
		ReadOnly:               false,
	},
	RoleReconciler: {
		FileQuery:              true,
		FileQueryReadOnly:      true, // 矩阵标“读”
		DiscoveryWrite:         false,
		BuildSubmitReproduce:   false,
		BuildDriverSandboxOnly: false,
		ReviewEvidence:         false,
		Report:                 false,
		QueueClaim:             []QueueKind{QueueRepairTask},
		PlannerPolicy:          false,
		OwnerJournal:           false,
		OwnerJournalRead:       true, // 矩阵标“读”
		ReconciliationWrite:    true,
		ReadOnly:               true,
	},
}

var (
	errToolSurfaceRoleUnknown     = errors.New("lifetime: 未知角色")
	errToolSurfaceRoleUnspecified = errors.New("lifetime: 角色没有 §16.6 工具面矩阵行")
	errToolSurfaceUnknownTool     = errors.New("lifetime: 未知工具")
	errToolSurfaceDenied          = errors.New("lifetime: 角色工具面越权")
)

// AllRoles 返回 §5.1 全部角色，顺序固定为表格顺序。
func AllRoles() []Role {
	out := make([]Role, len(allRoleOrder))
	copy(out, allRoleOrder)
	return out
}

func cloneRoleSpec(spec RoleSpec) RoleSpec {
	spec.Artifacts = append([]string(nil), spec.Artifacts...)
	spec.Forbidden = append([]string(nil), spec.Forbidden...)
	spec.Surface.QueueClaim = append([]QueueKind(nil), spec.Surface.QueueClaim...)
	return spec
}

// RoleSpecFor 返回单个角色的 §5.1 定义（含 §16.6 工具面）。
func RoleSpecFor(r Role) (RoleSpec, bool) {
	spec, ok := roleSpecTable[r]
	if !ok {
		return RoleSpec{}, false
	}
	if surface, err := ToolSurfaceFor(r); err == nil {
		spec.Surface = surface
	}
	return cloneRoleSpec(spec), true
}

// RoleSpecs 返回全部角色定义。调用方修改返回值不会影响包内状态。
func RoleSpecs() map[Role]RoleSpec {
	out := make(map[Role]RoleSpec, len(allRoleOrder))
	for _, r := range allRoleOrder {
		spec, _ := RoleSpecFor(r)
		out[r] = spec
	}
	return out
}

// ToolSurfaceFor 返回角色在 §16.6 矩阵中的工具面。
//
// 非法角色返回错误；mainagent 是合法的人在环路角色但不在矩阵内，返回零值
// 工具面（不注入任何工具组），由 ValidateToolCall 拒绝所有 §16 工具调用。
func ToolSurfaceFor(r Role) (RoleSurface, error) {
	if !r.Valid() {
		return RoleSurface{}, fmt.Errorf("%w: %q", errToolSurfaceRoleUnknown, r)
	}
	if r == RoleMainAgent {
		// §5.5：mainagent 只做授权/约束/目标/例外，不持有 worker 工具。
		return RoleSurface{ReadOnly: true}, nil
	}
	surface, ok := toolSurfaceMatrix[r]
	if !ok {
		return RoleSurface{}, fmt.Errorf("%w: %q", errToolSurfaceRoleUnspecified, r)
	}
	surface.QueueClaim = append([]QueueKind(nil), surface.QueueClaim...)
	return surface, nil
}

// CanClaimQueue 报告角色是否可 claim 指定队列（§16.6 queue claim 行）。
func CanClaimQueue(r Role, q QueueKind) bool {
	if !q.Valid() {
		return false
	}
	surface, err := ToolSurfaceFor(r)
	if err != nil {
		return false
	}
	for _, allowed := range surface.QueueClaim {
		if allowed == q {
			return true
		}
	}
	return false
}

// ValidateToolCall 校验角色能否调用某个工具（R-5.10/R-16.9）。
//
// 工具到工具组的映射来自 tools.go 注册表；未知角色、未知工具、越权调用均返回
// error。reviewer 的 sandbox 约束（R-5.7/R-16.10）由 RoleSurface 的
// BuildDriverSandboxOnly 标记，供工具注入层仅在 review sandbox 注入 build_driver；
// 本函数只做组级授权（reviewer 仍可独立提交/复现）。
//
// 注意：claim/renew/release/complete 是队列组通用工具，本函数只校验角色是否有
// 队列 claim 权限；具体队列是否允许由 CanClaimQueue 结合请求中的 queue_kind 判断。
func ValidateToolCall(r Role, tool string) error {
	surface, err := ToolSurfaceFor(r)
	if err != nil {
		return err
	}
	group, ok := ToolGroupOf(tool)
	if !ok {
		return fmt.Errorf("%w: %q", errToolSurfaceUnknownTool, tool)
	}

	allowed := false
	switch group {
	case ToolGroupFileQuery:
		allowed = surface.FileQuery
	case ToolGroupDiscoveryWrite:
		allowed = surface.DiscoveryWrite
	case ToolGroupBuildSubmitReproduce:
		// reviewer 的 sandbox 约束（R-5.7/R-16.10）是注入位置约束：build_driver
		// 只在 review sandbox 注入，主工作区不注入。ValidateToolCall 没有 workspace
		// 上下文，因此只校验组级权限；注入层依据 BuildDriverSandboxOnly 执行。
		allowed = surface.BuildSubmitReproduce
	case ToolGroupReviewEvidence:
		allowed = surface.ReviewEvidence
	case ToolGroupReport:
		allowed = surface.Report
	case ToolGroupQueueClaim:
		allowed = len(surface.QueueClaim) > 0
	case ToolGroupPlannerPolicy:
		allowed = surface.PlannerPolicy
	case ToolGroupOwnerJournal:
		allowed = surface.OwnerJournal
	case ToolGroupReconciliationWrite:
		allowed = surface.ReconciliationWrite
	}

	if !allowed {
		return fmt.Errorf("%w: role=%s tool=%s group=%s", errToolSurfaceDenied, r, tool, group)
	}
	return nil
}

// AgentKeyForRole 返回 ARTEX agent 注册使用的角色 key：
// lifetime_auditor → lt_auditor，lifetime_verifier → lt_verifier，其余角色
// 统一加 lt_ 命名空间前缀（reporter → lt_reporter 等），避免与 ARTEX 内置
// agent key（planner/worker/mainagent 等）冲突。非法角色返回空串。
func AgentKeyForRole(r Role) string {
	if !r.Valid() {
		return ""
	}
	name := strings.TrimPrefix(string(r), "lifetime_")
	return "lt_" + name
}
