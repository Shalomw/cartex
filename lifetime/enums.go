// Package lifetime implements the deterministic core of the ARTEX lifetime
// profile defined by 02-ARTEX二开方案/ARTEX二开方案-v1.8.md.
//
// 本包只做纯逻辑：实体、ID/哈希、S1–S4 谓词、状态机、覆盖账本、队列语义、
// 对账检查与 profile 校验。不依赖 ARTEX server/agent；持久化在 lifetime/store，
// HTTP 入口在 lifetime/service。平台写 evidence，agent 只写候选/事实/DriverSpec/
// review evidence（I-1/I-2）。
package lifetime

// StorageClass 对象存储类别（§4.1）。
type StorageClass string

const (
	StorageHeap   StorageClass = "heap"
	StorageStack  StorageClass = "stack"
	StorageStatic StorageClass = "static"
	StorageMember StorageClass = "member"
	StoragePool   StorageClass = "pool"
	StorageHandle StorageClass = "handle"
)

func (v StorageClass) Valid() bool {
	switch v {
	case StorageHeap, StorageStack, StorageStatic, StorageMember, StoragePool, StorageHandle:
		return true
	}
	return false
}

// LifetimeForm 生命周期形式（§4.1）。
type LifetimeForm string

const (
	LifetimeRaw       LifetimeForm = "raw"
	LifetimeShared    LifetimeForm = "shared"
	LifetimeWeak      LifetimeForm = "weak"
	LifetimeHandle    LifetimeForm = "handle"
	LifetimeRefcount  LifetimeForm = "refcount"
	LifetimeIntrusive LifetimeForm = "intrusive"
	LifetimeSingleton LifetimeForm = "singleton"
	LifetimePool      LifetimeForm = "pool"
)

func (v LifetimeForm) Valid() bool {
	switch v {
	case LifetimeRaw, LifetimeShared, LifetimeWeak, LifetimeHandle,
		LifetimeRefcount, LifetimeIntrusive, LifetimeSingleton, LifetimePool:
		return true
	}
	return false
}

// DomainKind 并发域种类（§4.2）。
type DomainKind string

const (
	DomainThread   DomainKind = "thread"
	DomainQueue    DomainKind = "queue"
	DomainExecutor DomainKind = "executor"
	DomainIPCPool  DomainKind = "ipc_pool"
	DomainTimer    DomainKind = "timer"
	DomainSignal   DomainKind = "signal"
	DomainShutdown DomainKind = "shutdown"
)

func (v DomainKind) Valid() bool {
	switch v {
	case DomainThread, DomainQueue, DomainExecutor, DomainIPCPool, DomainTimer, DomainSignal, DomainShutdown:
		return true
	}
	return false
}

// Serialization 域内操作是否串行（§4.2）。
type Serialization string

const (
	SerializationSerial     Serialization = "serial"
	SerializationConcurrent Serialization = "concurrent"
	SerializationUnknown    Serialization = "unknown"
)

func (v Serialization) Valid() bool {
	switch v {
	case SerializationSerial, SerializationConcurrent, SerializationUnknown:
		return true
	}
	return false
}

// ConcurrencyDegree 域内并发度（§4.2）：1 / N / unknown。
type ConcurrencyDegree string

const (
	ConcurrencyOne     ConcurrencyDegree = "1"
	ConcurrencyN       ConcurrencyDegree = "N"
	ConcurrencyUnknown ConcurrencyDegree = "unknown"
)

func (v ConcurrencyDegree) Valid() bool {
	switch v {
	case ConcurrencyOne, ConcurrencyN, ConcurrencyUnknown:
		return true
	}
	return false
}

// Form 生命周期漏洞形态（§1.2/§12.5）。
type Form string

const (
	FormRefcountRace     Form = "F1" // 引用计数竞争
	FormDanglingThis     Form = "F2" // 悬垂 this
	FormRegisterLeak     Form = "F3" // 注册未反注册
	FormContainerInvalid Form = "F4" // 迭代器/容器失效
	FormStaticDestroy    Form = "F5" // 静态/单例析构顺序
	FormSharedPtrRace    Form = "F6" // shared_ptr 本体并发
	FormLazyInitRace     Form = "F7" // 惰性初始化竞争
	FormPoolABA          Form = "F8" // 对象池 ABA
)

func (v Form) Valid() bool {
	switch v {
	case FormRefcountRace, FormDanglingThis, FormRegisterLeak, FormContainerInvalid,
		FormStaticDestroy, FormSharedPtrRace, FormLazyInitRace, FormPoolABA:
		return true
	}
	return false
}

// PairBasis 域对基准（§4.2/§11.1）。
type PairBasis string

const (
	PairBasisLogical   PairBasis = "logical"
	PairBasisExecution PairBasis = "execution"
)

func (v PairBasis) Valid() bool { return v == PairBasisLogical || v == PairBasisExecution }

// DomainConfidence 域解析置信度（§4.2）。
type DomainConfidence string

const (
	DomainConfidenceHigh    DomainConfidence = "high"
	DomainConfidenceLow     DomainConfidence = "low"
	DomainConfidenceUnknown DomainConfidence = "unknown"
)

func (v DomainConfidence) Valid() bool {
	switch v {
	case DomainConfidenceHigh, DomainConfidenceLow, DomainConfidenceUnknown:
		return true
	}
	return false
}

// ConfidenceLevel 通用置信等级（§3.4/§5.4/§8.1）。
type ConfidenceLevel string

const (
	ConfidenceHigh    ConfidenceLevel = "high"
	ConfidenceMedium  ConfidenceLevel = "medium"
	ConfidenceLow     ConfidenceLevel = "low"
	ConfidenceUnknown ConfidenceLevel = "unknown"
)

func (v ConfidenceLevel) Valid() bool {
	switch v {
	case ConfidenceHigh, ConfidenceMedium, ConfidenceLow, ConfidenceUnknown:
		return true
	}
	return false
}

// EventType 证据事件类型（§3.2/§16.7）。
type EventType string

const (
	EventMemory    EventType = "memory"
	EventInvariant EventType = "invariant"
	EventHang      EventType = "hang"
	EventRaceLead  EventType = "race_lead"
)

func (v EventType) Valid() bool {
	switch v {
	case EventMemory, EventInvariant, EventHang, EventRaceLead:
		return true
	}
	return false
}

// EvidenceTool 产生证据的工具（§3.2）。
type EvidenceTool string

const (
	ToolASan      EvidenceTool = "asan"
	ToolValgrind  EvidenceTool = "valgrind"
	ToolMSan      EvidenceTool = "msan"
	ToolTSan      EvidenceTool = "tsan"
	ToolAssertion EvidenceTool = "assertion"
	ToolWatchdog  EvidenceTool = "watchdog"
)

func (v EvidenceTool) Valid() bool {
	switch v {
	case ToolASan, ToolValgrind, ToolMSan, ToolTSan, ToolAssertion, ToolWatchdog:
		return true
	}
	return false
}

// Platform 目标平台保真类别（§13.1）。
type Platform string

const (
	PlatformNative   Platform = "native"
	PlatformEmulated Platform = "emulated"
	PlatformForeign  Platform = "foreign"
)

func (v Platform) Valid() bool {
	switch v {
	case PlatformNative, PlatformEmulated, PlatformForeign:
		return true
	}
	return false
}

// PlatformQuality 平台质量（§13.1）。
type PlatformQuality string

const (
	PlatformQualityHigh   PlatformQuality = "high"
	PlatformQualityMedium PlatformQuality = "medium"
	PlatformQualityLow    PlatformQuality = "low"
)

func (v PlatformQuality) Valid() bool {
	switch v {
	case PlatformQualityHigh, PlatformQualityMedium, PlatformQualityLow:
		return true
	}
	return false
}

// Fidelity 证据保真度（§13.2）。
type Fidelity string

const (
	FidelityUnit      Fidelity = "unit"
	FidelityComponent Fidelity = "component"
	FidelityDevice    Fidelity = "device"
)

func (v Fidelity) Valid() bool {
	switch v {
	case FidelityUnit, FidelityComponent, FidelityDevice:
		return true
	}
	return false
}

// FidelityRank 返回 fidelity 的单调序，用于 ">=" 比较。
func (v Fidelity) Rank() int {
	switch v {
	case FidelityUnit:
		return 1
	case FidelityComponent:
		return 2
	case FidelityDevice:
		return 3
	}
	return 0
}

// AtLeast 报告 v 是否达到 want 的最低保真度。
func (v Fidelity) AtLeast(want Fidelity) bool { return v.Rank() >= want.Rank() }

// Binding 观察与候选的绑定结果（§3.2/§9.5）。
type Binding string

const (
	BindingMatched   Binding = "matched"
	BindingPartial   Binding = "partial"
	BindingUnmatched Binding = "unmatched"
)

func (v Binding) Valid() bool {
	switch v {
	case BindingMatched, BindingPartial, BindingUnmatched:
		return true
	}
	return false
}

// BindingQuality 绑定精度（§11.4）。
type BindingQuality string

const (
	BindingQualityExact     BindingQuality = "exact"
	BindingQualityHeuristic BindingQuality = "heuristic"
	BindingQualityPartial   BindingQuality = "partial"
)

func (v BindingQuality) Valid() bool {
	switch v {
	case BindingQualityExact, BindingQualityHeuristic, BindingQualityPartial:
		return true
	}
	return false
}

// IdentitySource 运行时身份来源（§11.1）。
type IdentitySource string

const (
	IdentityAllocHook      IdentitySource = "alloc_hook"
	IdentityStackHook      IdentitySource = "stack_hook"
	IdentityStaticRegistry IdentitySource = "static_registry"
	IdentityMemberOffset   IdentitySource = "member_offset"
	IdentityPoolGeneration IdentitySource = "pool_generation"
	IdentityHandleTable    IdentitySource = "handle_table"
	IdentitySymbol         IdentitySource = "symbol"
)

func (v IdentitySource) Valid() bool {
	switch v {
	case IdentityAllocHook, IdentityStackHook, IdentityStaticRegistry, IdentityMemberOffset,
		IdentityPoolGeneration, IdentityHandleTable, IdentitySymbol:
		return true
	}
	return false
}

// DomainSource 运行时域来源（§11.1）。
type DomainSource string

const (
	DomainSourceSubmitHook  DomainSource = "submit_hook"
	DomainSourceExecuteHook DomainSource = "execute_hook"
	DomainSourceTLS         DomainSource = "tls"
	DomainSourceExplicit    DomainSource = "explicit"
	DomainSourceUnknown     DomainSource = "unknown"
)

func (v DomainSource) Valid() bool {
	switch v {
	case DomainSourceSubmitHook, DomainSourceExecuteHook, DomainSourceTLS,
		DomainSourceExplicit, DomainSourceUnknown:
		return true
	}
	return false
}

// Level 评估等级（§3.6）。S1_provisional 不是 claim 等级，单独用
// Assessment.Provisional 表达。
type Level string

const (
	LevelS1 Level = "S1"
	LevelS2 Level = "S2"
	LevelS3 Level = "S3"
	LevelS4 Level = "S4"
)

func (v Level) Valid() bool {
	switch v {
	case LevelS1, LevelS2, LevelS3, LevelS4:
		return true
	}
	return false
}

// Rank 返回等级序（S1=1..S4=4）。
func (v Level) Rank() int {
	switch v {
	case LevelS1:
		return 1
	case LevelS2:
		return 2
	case LevelS3:
		return 3
	case LevelS4:
		return 4
	}
	return 0
}

// AtLeast 报告 v 是否达到 want。
func (v Level) AtLeast(want Level) bool { return v.Rank() >= want.Rank() }

// AssessmentStatus 评估状态（§3.4）。
type AssessmentStatus string

const (
	AssessmentOpen       AssessmentStatus = "open"
	AssessmentReviewed   AssessmentStatus = "reviewed"
	AssessmentRefuted    AssessmentStatus = "refuted"
	AssessmentSuperseded AssessmentStatus = "superseded"
)

func (v AssessmentStatus) Valid() bool {
	switch v {
	case AssessmentOpen, AssessmentReviewed, AssessmentRefuted, AssessmentSuperseded:
		return true
	}
	return false
}

// ReviewVerdict 复核结论（§10.1）。
type ReviewVerdict string

const (
	VerdictNone         ReviewVerdict = ""
	VerdictConfirmed    ReviewVerdict = "confirmed"
	VerdictRefuted      ReviewVerdict = "refuted"
	VerdictInconclusive ReviewVerdict = "inconclusive"
)

func (v ReviewVerdict) Valid() bool {
	switch v {
	case VerdictNone, VerdictConfirmed, VerdictRefuted, VerdictInconclusive:
		return true
	}
	return false
}

// Independence 复核独立性，由平台从 provenance 派生（§10.2）。
type Independence string

const (
	IndependenceSameDriver            Independence = "same_driver"
	IndependenceNewDriverSameVerifier Independence = "new_driver_same_verifier"
	IndependenceNewDriverReviewer     Independence = "new_driver_reviewer"
	IndependenceIndependentRepro      Independence = "independent_reproduction"
	IndependenceLogRederivation       Independence = "log_rederivation"
	IndependenceSourceRederived       Independence = "source_rederived"
)

func (v Independence) Valid() bool {
	switch v {
	case IndependenceSameDriver, IndependenceNewDriverSameVerifier,
		IndependenceNewDriverReviewer, IndependenceIndependentRepro,
		IndependenceLogRederivation, IndependenceSourceRederived:
		return true
	}
	return false
}

// ExternalAllowed 报告该独立性级别能否支撑对外 finding（§10.2 表）。
func (v Independence) ExternalAllowed() bool {
	return v == IndependenceNewDriverReviewer || v == IndependenceIndependentRepro
}

// LineageRole 实验/评估的 lineage 角色（§10.3）。
type LineageRole string

const (
	LineageRoleClaim        LineageRole = "claim"
	LineageRoleConfirmation LineageRole = "confirmation"
)

func (v LineageRole) Valid() bool { return v == LineageRoleClaim || v == LineageRoleConfirmation }

// ExperimentKind 实验种类（§3.3）。
type ExperimentKind string

const (
	ExperimentKindCandidate ExperimentKind = "candidate"
	ExperimentKindControl   ExperimentKind = "control"
)

func (v ExperimentKind) Valid() bool {
	return v == ExperimentKindCandidate || v == ExperimentKindControl
}

// ControlKind 负控制类型（§9.6）。
type ControlKind string

const (
	ControlKindTrigger ControlKind = "trigger"
	ControlKindTarget  ControlKind = "target"
	ControlKindDriver  ControlKind = "driver"
	ControlKindNone    ControlKind = "none"
)

func (v ControlKind) Valid() bool {
	switch v {
	case ControlKindTrigger, ControlKindTarget, ControlKindDriver, ControlKindNone:
		return true
	}
	return false
}

// ControlResult 负控制结果（§3.3）。
type ControlResult string

const (
	ControlResultNone          ControlResult = "none"
	ControlResultPassed        ControlResult = "passed"
	ControlResultFailed        ControlResult = "failed"
	ControlResultNotApplicable ControlResult = "not_applicable"
)

func (v ControlResult) Valid() bool {
	switch v {
	case ControlResultNone, ControlResultPassed, ControlResultFailed, ControlResultNotApplicable:
		return true
	}
	return false
}

// ExperimentState 作业/实验状态（§3.3/§9.3）。
type ExperimentState string

const (
	ExperimentQueued       ExperimentState = "queued"
	ExperimentRunning      ExperimentState = "running"
	ExperimentCompleted    ExperimentState = "completed"
	ExperimentFailed       ExperimentState = "failed"
	ExperimentTimeout      ExperimentState = "timeout"
	ExperimentCancelled    ExperimentState = "cancelled"
	ExperimentInconclusive ExperimentState = "inconclusive"
)

func (v ExperimentState) Valid() bool {
	switch v {
	case ExperimentQueued, ExperimentRunning, ExperimentCompleted,
		ExperimentFailed, ExperimentTimeout, ExperimentCancelled, ExperimentInconclusive:
		return true
	}
	return false
}

// ReproductionMode 复现模式（§14.5）。
type ReproductionMode string

const (
	ReproSingle              ReproductionMode = "single"
	ReproDeterministicReplay ReproductionMode = "deterministic_replay"
	ReproStochasticStable    ReproductionMode = "stochastic_stable"
)

func (v ReproductionMode) Valid() bool {
	switch v {
	case ReproSingle, ReproDeterministicReplay, ReproStochasticStable:
		return true
	}
	return false
}

// ContractStatus API 契约状态（§12.1）。
type ContractStatus string

const (
	ContractKnown       ContractStatus = "known"
	ContractUnknown     ContractStatus = "unknown"
	ContractConflicting ContractStatus = "conflicting"
)

func (v ContractStatus) Valid() bool {
	switch v {
	case ContractKnown, ContractUnknown, ContractConflicting:
		return true
	}
	return false
}

// ContractCheck DriverSpec 契约检查结果（§3.3/§12.1）。
type ContractCheck string

const (
	ContractCheckPassed        ContractCheck = "passed"
	ContractCheckViolated      ContractCheck = "violated"
	ContractCheckNotApplicable ContractCheck = "not_applicable"
)

func (v ContractCheck) Valid() bool {
	switch v {
	case ContractCheckPassed, ContractCheckViolated, ContractCheckNotApplicable:
		return true
	}
	return false
}

// LifecycleState 任务/候选状态机状态（§7.1）。
type LifecycleState string

const (
	StatePrepare       LifecycleState = "PREPARE"
	StateDiscover      LifecycleState = "DISCOVER"
	StateCandidate     LifecycleState = "CANDIDATE"
	StateProbe         LifecycleState = "PROBE"
	StateVerify        LifecycleState = "VERIFY"
	StateEvidence      LifecycleState = "EVIDENCE"
	StateReproduce     LifecycleState = "REPRODUCE"
	StateReview        LifecycleState = "REVIEW"
	StateReport        LifecycleState = "REPORT"
	StateRefute        LifecycleState = "REFUTE"
	StateInconclusive  LifecycleState = "INCONCLUSIVE"
	StateBlocked       LifecycleState = "BLOCKED"
	StateDeferred      LifecycleState = "DEFERRED"
	StateIndexRevision LifecycleState = "INDEX_REVISION"
)

func (v LifecycleState) Valid() bool {
	switch v {
	case StatePrepare, StateDiscover, StateCandidate, StateProbe, StateVerify,
		StateEvidence, StateReproduce, StateReview, StateReport, StateRefute,
		StateInconclusive, StateBlocked, StateDeferred, StateIndexRevision:
		return true
	}
	return false
}

// QueueKind 工作队列（§6.2）。
type QueueKind string

const (
	QueueUnitReadyDiscover    QueueKind = "Q1"
	QueueCandidateReadyVerify QueueKind = "Q2"
	QueueClaimReadyReview     QueueKind = "Q3"
	QueueConfirmedReadyReport QueueKind = "Q4"
	QueueRepairTask           QueueKind = "Q5"
)

func (v QueueKind) Valid() bool {
	switch v {
	case QueueUnitReadyDiscover, QueueCandidateReadyVerify, QueueClaimReadyReview,
		QueueConfirmedReadyReport, QueueRepairTask:
		return true
	}
	return false
}

// WorkItemState 队列项状态（§6.5）。
type WorkItemState string

const (
	WorkItemReady     WorkItemState = "ready"
	WorkItemClaimed   WorkItemState = "claimed"
	WorkItemDone      WorkItemState = "done"
	WorkItemExpired   WorkItemState = "expired"
	WorkItemBlocked   WorkItemState = "blocked"
	WorkItemCancelled WorkItemState = "cancelled"
)

func (v WorkItemState) Valid() bool {
	switch v {
	case WorkItemReady, WorkItemClaimed, WorkItemDone, WorkItemExpired,
		WorkItemBlocked, WorkItemCancelled:
		return true
	}
	return false
}

// UnitKind 覆盖单位种类（§4.5）。
type UnitKind string

const (
	UnitKindFamily   UnitKind = "family"
	UnitKindRelation UnitKind = "relation"
)

func (v UnitKind) Valid() bool { return v == UnitKindFamily || v == UnitKindRelation }

// CoverageStatus 覆盖账本状态（§4.6）。
type CoverageStatus string

const (
	CoverageUnexamined         CoverageStatus = "unexamined"
	CoverageExaminedUnreviewed CoverageStatus = "examined_unreviewed"
	CoverageCandidateOpen      CoverageStatus = "candidate_open"
	CoverageNoRisk             CoverageStatus = "no_risk"
	CoverageVerified           CoverageStatus = "verified"
	CoverageRefuted            CoverageStatus = "refuted"
	CoverageDeferredBudget     CoverageStatus = "deferred_budget"
	CoverageBlocked            CoverageStatus = "blocked"
	CoverageUnknown            CoverageStatus = "unknown"
	CoverageNotApplicable      CoverageStatus = "not_applicable"
	CoverageStale              CoverageStatus = "stale"
)

func (v CoverageStatus) Valid() bool {
	switch v {
	case CoverageUnexamined, CoverageExaminedUnreviewed, CoverageCandidateOpen,
		CoverageNoRisk, CoverageVerified, CoverageRefuted, CoverageDeferredBudget,
		CoverageBlocked, CoverageUnknown, CoverageNotApplicable, CoverageStale:
		return true
	}
	return false
}

// Concluded 报告该状态是否已得出结论（§4.6）。
func (v CoverageStatus) Concluded() bool {
	return v == CoverageNoRisk || v == CoverageVerified || v == CoverageRefuted
}

// RiskClosed 报告该状态是否闭合风险（§4.6）。
func (v CoverageStatus) RiskClosed() bool {
	return v == CoverageNoRisk || v == CoverageRefuted
}

// UnknownReason unknown 行的结构化原因（§4.7/§8.4）。
type UnknownReason string

const (
	UnknownIndexFailed   UnknownReason = "index_failed"
	UnknownIndirectCall  UnknownReason = "indirect_call_unresolved"
	UnknownThirdParty    UnknownReason = "third_party"
	UnknownGeneratedCode UnknownReason = "generated_code"
	UnknownOutOfScope    UnknownReason = "out_of_scope"
)

func (v UnknownReason) Valid() bool {
	switch v {
	case UnknownIndexFailed, UnknownIndirectCall, UnknownThirdParty,
		UnknownGeneratedCode, UnknownOutOfScope:
		return true
	}
	return false
}

// NoRiskArgumentType no_risk 论证类型（§4.7）。
type NoRiskArgumentType string

const (
	NoRiskSerialized   NoRiskArgumentType = "serialized"
	NoRiskSingleDomain NoRiskArgumentType = "single_domain"
	NoRiskUnreachable  NoRiskArgumentType = "unreachable"
	NoRiskGuarded      NoRiskArgumentType = "guarded"
	NoRiskGeneration   NoRiskArgumentType = "generation"
	NoRiskRelationSafe NoRiskArgumentType = "relation_safe"
)

func (v NoRiskArgumentType) Valid() bool {
	switch v {
	case NoRiskSerialized, NoRiskSingleDomain, NoRiskUnreachable,
		NoRiskGuarded, NoRiskGeneration, NoRiskRelationSafe:
		return true
	}
	return false
}

// ReconciliationSeverity 对账发现严重度（§6.10）。
type ReconciliationSeverity string

const (
	SeverityLow      ReconciliationSeverity = "low"
	SeverityMedium   ReconciliationSeverity = "medium"
	SeverityHigh     ReconciliationSeverity = "high"
	SeverityCritical ReconciliationSeverity = "critical"
)

func (v ReconciliationSeverity) Valid() bool {
	switch v {
	case SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical:
		return true
	}
	return false
}

// ReconciliationAction 对账动作（§6.10）。
type ReconciliationAction string

const (
	ActionRequeue          ReconciliationAction = "requeue"
	ActionMarkStale        ReconciliationAction = "mark_stale"
	ActionCreateRepairTask ReconciliationAction = "create_repair_task"
	ActionNotifyPlanner    ReconciliationAction = "notify_planner"
	ActionMerge            ReconciliationAction = "merge"
	ActionEscalateHuman    ReconciliationAction = "escalate_human"
)

func (v ReconciliationAction) Valid() bool {
	switch v {
	case ActionRequeue, ActionMarkStale, ActionCreateRepairTask,
		ActionNotifyPlanner, ActionMerge, ActionEscalateHuman:
		return true
	}
	return false
}

// ReconciliationStatus 对账发现状态（§6.10）。
type ReconciliationStatus string

const (
	ReconOpen     ReconciliationStatus = "open"
	ReconResolved ReconciliationStatus = "resolved"
	ReconIgnored  ReconciliationStatus = "ignored"
)

func (v ReconciliationStatus) Valid() bool {
	switch v {
	case ReconOpen, ReconResolved, ReconIgnored:
		return true
	}
	return false
}

// Role agent 角色（§5.1）。
type Role string

const (
	RoleLifetimeAuditor  Role = "lifetime_auditor"
	RoleLifetimeVerifier Role = "lifetime_verifier"
	RoleLifetimeReviewer Role = "lifetime_reviewer"
	RoleReporter         Role = "reporter"
	RolePlanner          Role = "planner"
	RoleOwner            Role = "owner"
	RoleReconciler       Role = "reconciler"
	RoleMainAgent        Role = "mainagent"
)

func (v Role) Valid() bool {
	switch v {
	case RoleLifetimeAuditor, RoleLifetimeVerifier, RoleLifetimeReviewer,
		RoleReporter, RolePlanner, RoleOwner, RoleReconciler, RoleMainAgent:
		return true
	}
	return false
}

// Impact 影响级别（§9.8）。
type Impact string

const (
	ImpactNone     Impact = "none"
	ImpactDoS      Impact = "dos"
	ImpactInfo     Impact = "info"
	ImpactCodeExec Impact = "code_exec"
)

func (v Impact) Valid() bool {
	switch v {
	case ImpactNone, ImpactDoS, ImpactInfo, ImpactCodeExec:
		return true
	}
	return false
}

// OracleType 不变量 oracle 类型（§3.8/§9.1）。
type OracleType string

const (
	OracleAPIContract          OracleType = "api_contract"
	OracleInternalConsistency  OracleType = "internal_consistency"
	OracleResourceConservation OracleType = "resource_conservation"
	OracleStateMachine         OracleType = "state_machine"
)

func (v OracleType) Valid() bool {
	switch v {
	case OracleAPIContract, OracleInternalConsistency, OracleResourceConservation, OracleStateMachine:
		return true
	}
	return false
}

// OracleProvenance 不变量来源（§9.1）。
type OracleProvenance string

const (
	OracleFromDoc      OracleProvenance = "doc"
	OracleFromHeader   OracleProvenance = "header"
	OracleFromTest     OracleProvenance = "test"
	OracleFromCodeFact OracleProvenance = "code_fact"
)

func (v OracleProvenance) Valid() bool {
	switch v {
	case OracleFromDoc, OracleFromHeader, OracleFromTest, OracleFromCodeFact:
		return true
	}
	return false
}

// OracleEvalContext oracle 求值上下文（§3.8/§9.1）。
type OracleEvalContext string

const (
	OracleAtOperationSite OracleEvalContext = "operation_site"
	OracleLocked          OracleEvalContext = "locked"
	OracleAtomicSnapshot  OracleEvalContext = "atomic_snapshot"
)

func (v OracleEvalContext) Valid() bool {
	switch v {
	case OracleAtOperationSite, OracleLocked, OracleAtomicSnapshot:
		return true
	}
	return false
}

// Visibility 形态对 q4 可见性的要求（§12.4）。
type Visibility string

const (
	VisibilityRequired Visibility = "required"
	VisibilityOptional Visibility = "optional"
)

// OwnerJournalKind owner journal 条目类型（§6.11）。
type OwnerJournalKind string

const (
	JournalDecision     OwnerJournalKind = "decision"
	JournalHypothesis   OwnerJournalKind = "hypothesis"
	JournalDeadEnd      OwnerJournalKind = "dead_end"
	JournalAreaMap      OwnerJournalKind = "area_map"
	JournalOpenQuestion OwnerJournalKind = "open_question"
)

func (v OwnerJournalKind) Valid() bool {
	switch v {
	case JournalDecision, JournalHypothesis, JournalDeadEnd, JournalAreaMap, JournalOpenQuestion:
		return true
	}
	return false
}

// DispatchMode 调度模式（§6.8）。
type DispatchMode string

const (
	DispatchQueue   DispatchMode = "queue"
	DispatchPlanner DispatchMode = "planner"
	DispatchHybrid  DispatchMode = "hybrid"
)

func (v DispatchMode) Valid() bool {
	return v == DispatchQueue || v == DispatchPlanner || v == DispatchHybrid
}

// OwnerMode owner 模式（§6.11）。
type OwnerMode string

const (
	OwnerOff        OwnerMode = "off"
	OwnerSummary    OwnerMode = "summary"
	OwnerPersistent OwnerMode = "persistent"
	OwnerProactive  OwnerMode = "proactive"
)

func (v OwnerMode) Valid() bool {
	switch v {
	case OwnerOff, OwnerSummary, OwnerPersistent, OwnerProactive:
		return true
	}
	return false
}

// ReviewKind 复核证据种类（§10.1）。
type ReviewKind string

const (
	ReviewKindCounterEvidence ReviewKind = "counter_evidence"
	ReviewKindNewDriver       ReviewKind = "new_driver"
	ReviewKindRefutation      ReviewKind = "refutation"
	ReviewKindConfirmation    ReviewKind = "confirmation"
)

func (v ReviewKind) Valid() bool {
	switch v {
	case ReviewKindCounterEvidence, ReviewKindNewDriver, ReviewKindRefutation, ReviewKindConfirmation:
		return true
	}
	return false
}

// NodeRole 静态证据链节点角色（§3.10/§8.2）。
type NodeRole string

const (
	NodeLifetimePoint NodeRole = "lifetime_point"
	NodeHolder        NodeRole = "holder"
	NodeAccess        NodeRole = "access"
	NodeSyncGuard     NodeRole = "sync_guard"
	NodeNotification  NodeRole = "notification"
	NodeTrigger       NodeRole = "trigger"
	NodePropagation   NodeRole = "propagation"
)

func (v NodeRole) Valid() bool {
	switch v {
	case NodeLifetimePoint, NodeHolder, NodeAccess, NodeSyncGuard,
		NodeNotification, NodeTrigger, NodePropagation:
		return true
	}
	return false
}

// CitationStatus citation_check 结果（§8.2）。
type CitationStatus string

const (
	CitationVerified   CitationStatus = "verified"
	CitationUnresolved CitationStatus = "unresolved"
	CitationMismatch   CitationStatus = "mismatch"
)

func (v CitationStatus) Valid() bool {
	switch v {
	case CitationVerified, CitationUnresolved, CitationMismatch:
		return true
	}
	return false
}
