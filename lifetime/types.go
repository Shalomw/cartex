package lifetime

import "time"

// 本文件是与 02-ARTEX二开方案/ARTEX二开方案-v1.8.md 对齐的实体契约。
// 字段命名遵循 §2.4：schema 字段 snake_case，枚举小写；时间戳 UTC ISO-8601，
// 哈希 hex。未在文档定义的字段不得由实现自行添加。

// ---------------------------------------------------------------------------
// §4 对象族 / 并发域 / 索引产物
// ---------------------------------------------------------------------------

// ObjectFamily 对象族（§4.1）。family_id 由 §2.2 规则生成。
type ObjectFamily struct {
	FamilyID           string          `json:"family_id"`
	TypeCanonical      string          `json:"type_canonical"`
	AllocSite          string          `json:"alloc_site"`
	StorageClass       StorageClass    `json:"storage_class"`
	LifetimeForm       LifetimeForm    `json:"lifetime_form"`
	MemberPath         string          `json:"member_path,omitempty"`
	Domains            []string        `json:"domains,omitempty"`
	ApplicableForms    []Form          `json:"applicable_forms,omitempty"`
	Confidence         ConfidenceLevel `json:"confidence,omitempty"`
	IdentityConfidence ConfidenceLevel `json:"identity_confidence,omitempty"`
}

// ObjectRelation 跨 family 关系（§4.9）。
type ObjectRelation struct {
	RelationID      string          `json:"relation_id"`
	Type            string          `json:"type"` // owns/refers/registers/notifies/depends_on/destruct_order
	SubjectFamilyID string          `json:"subject_family_id"`
	ObjectFamilyID  string          `json:"object_family_id"`
	Site            string          `json:"site"`
	SourceRef       string          `json:"source_ref"`
	Confidence      ConfidenceLevel `json:"confidence"`
	IndexHash       string          `json:"index_hash"`
}

// Domain 并发域（§4.2）。
type Domain struct {
	DomainClassID     string            `json:"domain_class_id"`
	Kind              DomainKind        `json:"kind"`
	InstanceSite      string            `json:"instance_site"`
	ParentPool        string            `json:"parent_pool,omitempty"`
	Serialization     Serialization     `json:"serialization"`
	ConcurrencyDegree ConcurrencyDegree `json:"concurrency_degree"`
}

// DomainPair 无序域对（§4.2）。单域形态用 (d,d)。
type DomainPair struct {
	PairClassID      string           `json:"pair_class_id"`
	DomainA          string           `json:"domain_a"`
	DomainB          string           `json:"domain_b"`
	DomainConfidence DomainConfidence `json:"domain_confidence"`
	PairBasisOptions []PairBasis      `json:"pair_basis_options,omitempty"`
}

// LifetimePoint 生命周期点（§8.1）。
type LifetimePoint struct {
	FamilyID  string   `json:"family_id"`
	Site      string   `json:"site"`
	Role      NodeRole `json:"role"`
	SourceRef string   `json:"source_ref"`
}

// SyncPrimitive 同步原语（§8.1）。protects 必填（R-8.12）。
type SyncPrimitive struct {
	Primitive string `json:"primitive"`
	Scope     string `json:"scope"`
	Protects  string `json:"protects"`
	FileLine  string `json:"file_line"`
}

// CallbackSpec 注册/反注册回调（§8.1）。
type CallbackSpec struct {
	RegisterSite   string `json:"register_site"`
	UnregisterSite string `json:"unregister_site,omitempty"`
	Capture        string `json:"capture"` // raw_this/weak/shared/handle
	SourceRef      string `json:"source_ref"`
}

// IndexUnknown 索引无法解析的范围（§8.1/R-8.3）。
type IndexUnknown struct {
	Scope  string        `json:"scope"`
	Reason UnknownReason `json:"reason"`
	Detail string        `json:"detail"`
}

// IndexOutput 索引器输出（§8.1）。
type IndexOutput struct {
	IndexHash       string           `json:"index_hash"`
	IndexQuality    string           `json:"index_quality"`   // full/heuristic
	TypeResolution  string           `json:"type_resolution"` // exact/partial/unknown
	ObjectFamilies  []ObjectFamily   `json:"object_families"`
	ObjectRelations []ObjectRelation `json:"object_relations"`
	Domains         []Domain         `json:"domains"`
	DomainPairs     []DomainPair     `json:"domain_pairs"`
	LifetimePoints  []LifetimePoint  `json:"lifetime_points"`
	SyncPrimitives  []SyncPrimitive  `json:"sync_primitives"`
	Callbacks       []CallbackSpec   `json:"callbacks"`
	Unknowns        []IndexUnknown   `json:"unknowns"`
}

// FamilyDetail 是查询工具返回的单族视图：族本身 + 生命周期事实 + 域/覆盖。
// 域与 sync/callback 是 task 级索引事实，按族查询时一并返回供 agent 交叉判断。
type FamilyDetail struct {
	Family         ObjectFamily     `json:"family"`
	LifetimePoints []LifetimePoint  `json:"lifetime_points"`
	SyncPrimitives []SyncPrimitive  `json:"sync_primitives"`
	Callbacks      []CallbackSpec   `json:"callbacks"`
	Relations      []ObjectRelation `json:"relations"`
	Domains        []Domain         `json:"domains"`
	DomainPairs    []DomainPair     `json:"domain_pairs"`
	Coverage       []CoverageUnit   `json:"coverage"`
	IndexHash      string           `json:"index_hash"`
	LineageID      string           `json:"lineage_id"`
}

// BootstrapResult 是 index/bootstrap 端点的汇总（平台写，agent 只读）。
type BootstrapResult struct {
	TaskID        string `json:"task_id"`
	LineageID     string `json:"lineage_id"`
	PolicyHash    string `json:"policy_hash"`
	IndexHash     string `json:"index_hash"`
	IndexQuality  string `json:"index_quality"`
	Families      int    `json:"families"`
	Domains       int    `json:"domains"`
	DomainPairs   int    `json:"domain_pairs"`
	CoverageUnits int    `json:"coverage_units"`
	WorkItems     int    `json:"work_items"`
}

// ---------------------------------------------------------------------------
// §8.2 静态证据
// ---------------------------------------------------------------------------

// EvidenceNode 证据链节点（§8.2）。
type EvidenceNode struct {
	FactID string   `json:"fact_id"`
	Role   NodeRole `json:"role"`
	File   string   `json:"file"`
	Line   int      `json:"line"`
	Symbol string   `json:"symbol"`
	Source string   `json:"source"`
}

// CitationCheck 机械引用校验结果（§8.2）。
type CitationCheck struct {
	Status         CitationStatus `json:"status"`
	File           string         `json:"file"`
	Line           int            `json:"line"`
	Symbol         string         `json:"symbol"`
	Role           NodeRole       `json:"role"`
	Range          string         `json:"range,omitempty"`
	SymbolPattern  string         `json:"symbol_pattern,omitempty"`
	CheckedAt      time.Time      `json:"checked_at"`
	CheckerVersion string         `json:"checker_version"`
	Detail         string         `json:"detail,omitempty"`
}

// StaticSupport 静态支持（§8.2）。
type StaticSupport struct {
	SupportID      string          `json:"support_id"`
	UnitID         string          `json:"unit_id"`
	CandidateID    string          `json:"candidate_id,omitempty"`
	Facts          []EvidenceNode  `json:"facts"`
	Chain          []EvidenceNode  `json:"chain"`
	CitationChecks []CitationCheck `json:"citation_checks"`
	IndexHash      string          `json:"index_hash"`
	CreatedBy      string          `json:"created_by"`
	CreatedAt      time.Time       `json:"created_at"`
}

// ---------------------------------------------------------------------------
// §8.3 候选
// ---------------------------------------------------------------------------

// Holder 持有者（§8.3）。
type Holder struct {
	Site   string `json:"site"`
	Form   string `json:"form"`
	Domain string `json:"domain,omitempty"`
}

// Access 竞争访问（§8.3）。
type Access struct {
	Site      string `json:"site"`
	Domain    string `json:"domain"`
	GuardedBy string `json:"guarded_by"`
}

// OwnerAnswer 四问的单问回答（§12.3）。
type OwnerAnswer struct {
	Value  string `json:"value"`
	Form   string `json:"form,omitempty"`
	Domain string `json:"domain,omitempty"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
}

// VisibilityAnswer q4 析构可见性回答（§12.3）。
type VisibilityAnswer struct {
	Value     string `json:"value"`  // refcount|callback_unregister|weak_ptr|lock|generation|none|unknown
	Status    string `json:"status"` // known|unknown|none
	Reason    string `json:"reason,omitempty"`
	SourceRef string `json:"source_ref,omitempty"`
}

// OwnershipAnswers 四问（§12.3）。
type OwnershipAnswers struct {
	Q1CreatedBy   OwnerAnswer      `json:"q1_created_by"`
	Q2HeldBy      OwnerAnswer      `json:"q2_held_by"`
	Q3DestroyedBy OwnerAnswer      `json:"q3_destroyed_by"`
	Q4            VisibilityAnswer `json:"q4"`
}

// LifetimeVisibility 生命周期可见性（§8.3）。
type LifetimeVisibility struct {
	HolderNotification string           `json:"holder_notification"`
	NotificationSite   string           `json:"notification_site,omitempty"`
	Window             string           `json:"window,omitempty"`
	Q4Answer           VisibilityAnswer `json:"q4_answer"`
	VisibilityUnknown  bool             `json:"visibility_unknown"`
}

// Gap 生命周期缺口（§8.3）。
type Gap struct {
	Description   string   `json:"description"`
	Window        string   `json:"window"`
	Preconditions []string `json:"preconditions,omitempty"`
}

// TriggerHypothesis 触发假设（§8.3）。
type TriggerHypothesis struct {
	Domains      []string `json:"domains"`
	Interleaving string   `json:"interleaving"`
}

// CandidatePackage record_candidate 的候选包（§8.3）。
type CandidatePackage struct {
	Family             ObjectFamily       `json:"family"`
	Domains            []Domain           `json:"domains"`
	DomainPairID       string             `json:"domain_pair_id"` // never null
	PairBasis          PairBasis          `json:"pair_basis"`
	DomainConfidence   DomainConfidence   `json:"domain_confidence"`
	Holders            []Holder           `json:"holders"`
	CompetingAccesses  []Access           `json:"competing_accesses"`
	EvidenceChain      []EvidenceNode     `json:"evidence_chain"`
	Synchronization    []SyncPrimitive    `json:"synchronization"`
	LifetimeVisibility LifetimeVisibility `json:"lifetime_visibility"`
	OwnershipAnswers   OwnershipAnswers   `json:"ownership_answers"`
	Gap                Gap                `json:"gap"`
	TriggerHypothesis  TriggerHypothesis  `json:"trigger_hypothesis"`
	PrimaryForm        Form               `json:"primary_form"`
	ContributingForms  []Form             `json:"contributing_forms,omitempty"`
	FamilySet          []string           `json:"family_set,omitempty"`
	RelationRefs       []string           `json:"relation_refs,omitempty"`
	CandidateKey       string             `json:"candidate_key"`
	MechanismKey       string             `json:"mechanism_key"`
	ContractStatus     ContractStatus     `json:"contract_status"`
	ContractRefs       []string           `json:"contract_refs,omitempty"`
	ContractAuditID    string             `json:"contract_audit_id,omitempty"`
	Flags              []string           `json:"flags,omitempty"`
	DriverSpecRef      string             `json:"driver_spec_ref,omitempty"`
	Version            int                `json:"version"`
}

// CandidateBudget 候选级预算视图；平台维护，agent 不得写（§9.10）。
type CandidateBudget struct {
	Remaining int `json:"remaining"`
	Used      int `json:"used"`
}

// Candidate 生命周期候选（§2.1/§8.3）。
type Candidate struct {
	CandidateID string `json:"candidate_id"`
	UnitID      string `json:"unit_id"`
	SupportID   string `json:"support_id"`
	CandidatePackage
	Budget    CandidateBudget `json:"budget"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// §3 证据
// ---------------------------------------------------------------------------

// RuntimeContext 运行时身份与域上下文（§11.1）。
type RuntimeContext struct {
	ThreadID          string         `json:"thread_id"`
	TaskID            string         `json:"task_id,omitempty"`
	OriginDomainID    string         `json:"origin_domain_id"`
	ExecutionDomainID string         `json:"execution_domain_id"`
	DomainPath        []string       `json:"domain_path"`
	PairDomainID      string         `json:"pair_domain_id"`
	PairBasis         PairBasis      `json:"pair_basis"`
	FamilyID          string         `json:"family_id"`
	InstanceID        string         `json:"instance_id"`
	Generation        int64          `json:"generation,omitempty"`
	AccessSite        string         `json:"access_site"`
	AllocSite         string         `json:"alloc_site"`
	IdentitySource    IdentitySource `json:"identity_source"`
	DomainSource      DomainSource   `json:"domain_source"`
	BindingQuality    BindingQuality `json:"binding_quality"`
}

// Stacks 事件栈（§3.2）。
type Stacks struct {
	Alloc    []string `json:"alloc,omitempty"`
	Free     []string `json:"free,omitempty"`
	Use      []string `json:"use,omitempty"`
	AccessA  []string `json:"access_a,omitempty"`
	AccessB  []string `json:"access_b,omitempty"`
	Watchdog []string `json:"watchdog,omitempty"`
}

// Observation 单次运行事件；append-only，平台写（§3.2）。
type Observation struct {
	ObservationID   string          `json:"observation_id"`
	ExperimentID    string          `json:"experiment_id"`
	CandidateID     string          `json:"candidate_id"`
	EventType       EventType       `json:"event_type"`
	Tool            EvidenceTool    `json:"tool"`
	Platform        Platform        `json:"platform"`
	Fidelity        Fidelity        `json:"fidelity"`
	PlatformQuality PlatformQuality `json:"platform_quality"`
	Binding         Binding         `json:"binding"`
	BindingQuality  BindingQuality  `json:"binding_quality"`
	PairBasis       PairBasis       `json:"pair_basis"`
	RuntimeContext  RuntimeContext  `json:"runtime_context"`
	Stacks          Stacks          `json:"stacks"`
	InvariantID     string          `json:"invariant_id,omitempty"`
	PayloadRef      string          `json:"payload_ref"`
	LogSHA256       string          `json:"log_sha256"`
	IsControl       bool            `json:"is_control"`
	CreatedAt       time.Time       `json:"created_at"`
}

// Reproduction 复现统计（§3.3/§14.5）。
type Reproduction struct {
	Mode                   ReproductionMode `json:"mode"`
	N                      int              `json:"n"`
	Hits                   int              `json:"hits"`
	CPLowerBound95         float64          `json:"cp_lower_bound_95"`
	ReplayArtifactVerified bool             `json:"replay_artifact_verified"`
	ReplayArtifactRef      string           `json:"replay_artifact_ref,omitempty"`
}

// Experiment 作业/控制实验聚合；append-only，平台写（§3.3）。
type Experiment struct {
	ExperimentID          string          `json:"experiment_id"`
	JobID                 string          `json:"job_id"`
	CandidateID           string          `json:"candidate_id"`
	DriverID              string          `json:"driver_id"`
	DriverSpecHash        string          `json:"driver_spec_hash"`
	PolicyHash            string          `json:"policy_hash"`
	LineageID             string          `json:"lineage_id"`
	LineageRole           LineageRole     `json:"lineage_role"`
	ConfirmationOf        string          `json:"confirmation_of,omitempty"`
	Kind                  ExperimentKind  `json:"kind"`
	ControlKind           ControlKind     `json:"control_kind"`
	ControlPatchHash      string          `json:"control_patch_hash,omitempty"`
	ContractCheck         ContractCheck   `json:"contract_check"`
	ContractException     bool            `json:"contract_exception"`
	ObservationIDs        []string        `json:"observation_ids"`
	State                 ExperimentState `json:"state"`
	RunsAttempted         int             `json:"runs_attempted"`
	RunsCompleted         int             `json:"runs_completed"`
	ValidRuns             int             `json:"valid_runs"`
	HarnessFailures       int             `json:"harness_failures"`
	AnyHitRuns            int             `json:"any_hit_runs"`
	DistinctInterleavings int             `json:"distinct_interleavings"`
	DistinctSeedsHit      int             `json:"distinct_seeds_hit"`
	PlannedSeeds          int             `json:"planned_seeds"`
	HitRateAny            float64         `json:"hit_rate_any"`
	DistinctHitRate       float64         `json:"distinct_hit_rate"`
	ZeroHitUpperBound95   float64         `json:"zero_hit_upper_bound_95"`
	Reproduction          Reproduction    `json:"reproduction"`
	ControlResult         ControlResult   `json:"control_result"`
	ControlPowerAdequate  bool            `json:"control_power_adequate"`
	ControlCodeProof      string          `json:"control_code_proof"`
	StopRule              string          `json:"stop_rule"`
	StoppedReason         string          `json:"stopped_reason"`
	StartedAt             time.Time       `json:"started_at"`
	FinishedAt            time.Time       `json:"finished_at"`
}

// Provenance 评估来源（§3.4/§10.2）。
type Provenance struct {
	DriverAuthorSession string `json:"driver_author_session"`
	JobRunnerSession    string `json:"job_runner_session"`
}

// Review 复核结果（§3.4）。
type Review struct {
	Verdict           ReviewVerdict `json:"verdict"`
	Independence      Independence  `json:"independence"`
	ReviewerSessionID string        `json:"reviewer_session_id"`
	EvidenceSetHash   string        `json:"evidence_set_hash"`
	ReviewedAt        time.Time     `json:"reviewed_at"`
}

// Assessment 同一 lineage 的派生判定；append-only，平台写（§3.4）。
type Assessment struct {
	AssessmentID       string           `json:"assessment_id"`
	CandidateID        string           `json:"candidate_id"`
	CandidateKey       string           `json:"candidate_key"`
	LineageID          string           `json:"lineage_id"`
	LineageRole        LineageRole      `json:"lineage_role"`
	ConfirmationOf     string           `json:"confirmation_of,omitempty"`
	ClaimExperimentID  string           `json:"claim_experiment_id,omitempty"`
	Provenance         Provenance       `json:"provenance"`
	Level              Level            `json:"level"`
	Provisional        bool             `json:"provisional,omitempty"`
	Confidence         ConfidenceLevel  `json:"confidence"`
	ReportableInternal bool             `json:"reportable_internal"`
	ReportableExternal bool             `json:"reportable_external"`
	LimitedControl     bool             `json:"limited_control"`
	Review             Review           `json:"review"`
	ReviewStale        bool             `json:"review_stale"`
	Stale              bool             `json:"stale"`
	Status             AssessmentStatus `json:"status"`
	DerivedAt          time.Time        `json:"derived_at"`
}

// ---------------------------------------------------------------------------
// §4.5 工作包 / 覆盖账本 / no_risk / unknown / new_lead
// ---------------------------------------------------------------------------

// WorkPackage family × domain_pair，Q1 租约单位（§4.5）。
type WorkPackage struct {
	PackageID       string    `json:"package_id"`
	FamilyID        string    `json:"family_id"`
	DomainPairID    string    `json:"domain_pair_id"`
	ApplicableForms []Form    `json:"applicable_forms"`
	AreaID          string    `json:"area_id,omitempty"`
	LeaseOwner      string    `json:"lease_owner,omitempty"`
	LeasedAt        time.Time `json:"leased_at,omitempty"`
	ExpiresAt       time.Time `json:"expires_at,omitempty"`
}

// CoverageUnit family/relation × domain_pair × form 的账本行（§4.5）。
type CoverageUnit struct {
	UnitID       string         `json:"unit_id"`
	UnitKind     UnitKind       `json:"unit_kind"`
	FamilyID     string         `json:"family_id,omitempty"`
	RelationID   string         `json:"relation_id,omitempty"`
	DomainPairID string         `json:"domain_pair_id"`
	Form         Form           `json:"form"`
	Status       CoverageStatus `json:"status"`
	Reason       string         `json:"reason,omitempty"`
	EvidenceRef  string         `json:"evidence_ref,omitempty"`
	ProofRef     string         `json:"proof_ref,omitempty"`
	ReviewStatus string         `json:"review_status"` // self_asserted/reviewed
	LineageID    string         `json:"lineage_id"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// Proof no_risk 证明引用（§4.7）。
type Proof struct {
	Kind string   `json:"kind"`
	Refs []string `json:"refs"`
}

// NoRiskArgument no_risk 单条论证（§4.7）。
type NoRiskArgument struct {
	ArgumentType  NoRiskArgumentType `json:"argument_type"`
	CoversForms   []Form             `json:"covers_forms"`
	Sync          []SyncPrimitive    `json:"sync,omitempty"`
	AccessSet     []Access           `json:"access_set"`
	Proof         Proof              `json:"proof"`
	RelationRef   string             `json:"relation_ref,omitempty"`
	CitationCheck []CitationCheck    `json:"citation_check"`
}

// NoRisk 无风险证明（§4.7）。
type NoRisk struct {
	NoRiskID string `json:"no_risk_id"`
	// UnitID 是 record_no_risk(unit_id, ...) 的覆盖单位引用；平台据此解析
	// family_id / domain_pair_id，避免调用方重复传键。
	UnitID            string           `json:"unit_id,omitempty"`
	FamilyID          string           `json:"family_id"`
	DomainPairID      string           `json:"domain_pair_id"`
	Form              Form             `json:"form"`
	Arguments         []NoRiskArgument `json:"arguments"`
	CoversCurrentForm bool             `json:"covers_current_form"`
	ReviewStatus      string           `json:"review_status"` // self_asserted/reviewed
	ReviewerID        string           `json:"reviewer_id,omitempty"`
	CreatedBy         string           `json:"created_by"`
	CreatedAt         time.Time        `json:"created_at"`
}

// UnknownRecord unknown 行（§4.7）。
type UnknownRecord struct {
	UnknownID string        `json:"unknown_id"`
	FamilyID  string        `json:"family_id,omitempty"`
	Scope     string        `json:"scope"`
	Reason    UnknownReason `json:"reason"`
	Detail    string        `json:"detail"`
	LineageID string        `json:"lineage_id,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
}

// NewLead 新线索；不自动创建 candidate（§7.4/§8.4）。
type NewLead struct {
	LeadID      string    `json:"lead_id"`
	SourceEvent string    `json:"source_event"`
	FamilyID    string    `json:"family_id,omitempty"`
	Domains     []string  `json:"domains,omitempty"`
	Reason      string    `json:"reason"`
	Status      string    `json:"status"` // open/triaged/dismissed
	CreatedAt   time.Time `json:"created_at"`
}

// ---------------------------------------------------------------------------
// §6 工作队列 / claim / owner / 对账
// ---------------------------------------------------------------------------

// WorkItem 队列项（§6.5）。
type WorkItem struct {
	ItemID                string        `json:"item_id"`
	QueueKind             QueueKind     `json:"queue_kind"`
	ItemRef               string        `json:"item_ref"`
	ReadyPredicateVersion string        `json:"ready_predicate_version"`
	State                 WorkItemState `json:"state"`
	Priority              float64       `json:"priority"`
	ClaimedBySession      string        `json:"claimed_by_session,omitempty"`
	ClaimID               string        `json:"claim_id,omitempty"`
	LeaseExpiresAt        time.Time     `json:"lease_expires_at,omitempty"`
	Attempts              int           `json:"attempts"`
	DedupKey              string        `json:"dedup_key"`
	Reason                string        `json:"reason,omitempty"`
	LineageHash           string        `json:"lineage_hash,omitempty"`
	CreatedAt             time.Time     `json:"created_at"`
	UpdatedAt             time.Time     `json:"updated_at"`
}

// ClaimLog 调度日志（§6.12）。
type ClaimLog struct {
	Seq           int64     `json:"seq"`
	ClaimID       string    `json:"claim_id"`
	ItemID        string    `json:"item_id"`
	SessionID     string    `json:"session_id"`
	Action        string    `json:"action"` // claim|renew|release|complete|expire
	Priority      float64   `json:"priority"`
	SchedulerSeed int64     `json:"scheduler_seed"`
	Timestamp     time.Time `json:"timestamp"`
}

// AreaLease owner 对 area 的租约（§6.11）。
type AreaLease struct {
	AreaID       string    `json:"area_id"`
	OwnerSession string    `json:"owner_session"`
	LeasedAt     time.Time `json:"leased_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// OwnerJournalEntry owner 记忆条目；append-only（§6.11）。
type OwnerJournalEntry struct {
	EntryID    string           `json:"entry_id"`
	OwnerID    string           `json:"owner_id"`
	AreaID     string           `json:"area_id"`
	Kind       OwnerJournalKind `json:"kind"`
	Text       string           `json:"text"`
	Refs       []string         `json:"refs"`
	Confidence ConfidenceLevel  `json:"confidence"`
	SourceHash string           `json:"source_hash"`
	IndexHash  string           `json:"index_hash"`
	Stale      bool             `json:"stale"`
	CreatedAt  time.Time        `json:"created_at"`
}

// ReconciliationFinding 对账发现（§6.10）。
type ReconciliationFinding struct {
	ReconID     string                 `json:"recon_id"`
	SweepID     string                 `json:"sweep_id"`
	CheckID     string                 `json:"check_id"`
	Severity    ReconciliationSeverity `json:"severity"`
	SubjectKind string                 `json:"subject_kind"`
	SubjectRef  string                 `json:"subject_ref"`
	Expected    string                 `json:"expected"`
	Actual      string                 `json:"actual"`
	Action      ReconciliationAction   `json:"action"`
	Status      ReconciliationStatus   `json:"status"`
	DedupKey    string                 `json:"dedup_key"`
	CreatedAt   time.Time              `json:"created_at"`
	ResolvedAt  time.Time              `json:"resolved_at,omitempty"`
}

// ReconciliationRun 一次 sweep 的统计（§6.10/R-6.31）。
type ReconciliationRun struct {
	SweepID    string    `json:"sweep_id"`
	Mode       string    `json:"mode"` // incremental/full
	Checks     int       `json:"checks"`
	Findings   int       `json:"findings"`
	Repairs    int       `json:"repairs"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// ---------------------------------------------------------------------------
// §10 复核
// ---------------------------------------------------------------------------

// ReviewEvidence 复核证据（§10.1）。
type ReviewEvidence struct {
	ReviewID          string         `json:"review_id"`
	CandidateID       string         `json:"candidate_id"`
	AssessmentID      string         `json:"assessment_id"`
	EvidenceSetHash   string         `json:"evidence_set_hash"`
	Kind              ReviewKind     `json:"kind"`
	EvidenceIDs       []string       `json:"evidence_ids"`
	DriverID          string         `json:"driver_id,omitempty"`
	Citations         []EvidenceNode `json:"citations"`
	DerivedVerdict    ReviewVerdict  `json:"derived_verdict"`
	Reason            string         `json:"reason"`
	ReviewerSessionID string         `json:"reviewer_session_id"`
	CreatedAt         time.Time      `json:"created_at"`
}

// FindingProvenance 对外 finding 溯源（§10.6）。
type FindingProvenance struct {
	Status                 string             `json:"status"` // active/superseded/retracted
	Origin                 string             `json:"origin"` // static/dynamic/both
	CandidateID            string             `json:"candidate_id"`
	ClaimID                string             `json:"claim_id"`
	EvidenceIDs            []string           `json:"evidence_ids"`
	JobIDs                 []string           `json:"job_ids"`
	PrimaryLineageID       string             `json:"primary_lineage_id"`
	ConfirmationLineageIDs []string           `json:"confirmation_lineage_ids"`
	ReviewIDs              []string           `json:"review_ids"`
	FamilyID               string             `json:"family_id"`
	Type                   string             `json:"type"`
	AllocSite              string             `json:"alloc_site"`
	StorageClass           StorageClass       `json:"storage_class"`
	LifetimeForm           LifetimeForm       `json:"lifetime_form"`
	InstanceID             string             `json:"instance_id"`
	File                   string             `json:"file"`
	Line                   int                `json:"line"`
	Symbol                 string             `json:"symbol"`
	Domains                []Domain           `json:"domains"`
	DomainPairID           string             `json:"domain_pair_id"`
	LifetimeVisibility     LifetimeVisibility `json:"lifetime_visibility"`
	SyncGap                string             `json:"sync_gap"`
	EvidenceSetHash        string             `json:"evidence_set_hash"`
	Citations              []EvidenceNode     `json:"citations"`
	RootCause              string             `json:"root_cause"`
	EvidenceLevel          Level              `json:"evidence_level"`
	Impact                 Impact             `json:"impact"`
	NegativeControlKind    ControlKind        `json:"negative_control_kind"`
	NegativeControlResult  ControlResult      `json:"negative_control_result"`
	ContractStatus         ContractStatus     `json:"contract_status"`
	ContractBasis          []string           `json:"contract_basis"`
	ContractAuditID        string             `json:"contract_audit_id,omitempty"`
	LimitedControl         bool               `json:"limited_control"`
	SourceHash             string             `json:"source_hash"`
	IndexHash              string             `json:"index_hash"`
	BuildHash              string             `json:"build_hash"`
	DriverHash             string             `json:"driver_hash"`
	ProfileVersion         string             `json:"profile_version"`
	HarnessVersion         string             `json:"harness_version"`
	Model                  string             `json:"model"`
	Review                 Review             `json:"review"`
}

// ---------------------------------------------------------------------------
// §9 DriverSpec
// ---------------------------------------------------------------------------

// DriverOperation DriverSpec 操作（§9.1）。
type DriverOperation struct {
	OpID          string         `json:"op_id"`
	Name          string         `json:"name"`
	DomainID      string         `json:"domain_id"`
	APIPath       string         `json:"api_path"`
	APIVisibility string         `json:"api_visibility"` // public/internal
	ArgsSchema    []OperationArg `json:"args_schema"`
	Preconditions []string       `json:"preconditions,omitempty"`
}

// OperationArg 类型化参数（R-9.3）。
type OperationArg struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// DriverObject 对象图节点（§9.1）。
type DriverObject struct {
	Handle       string       `json:"handle"`
	Type         string       `json:"type"`
	CtorAPI      string       `json:"ctor_api"`
	Args         string       `json:"args"`
	StorageClass StorageClass `json:"storage_class"`
	MemberOf     string       `json:"member_of,omitempty"`
}

// DriverCallback 回调夹具（§9.1）。
type DriverCallback struct {
	Handle          string `json:"handle"`
	RegisterAPI     string `json:"register_api"`
	CallbackFixture string `json:"callback_fixture"`
	Capture         string `json:"capture"` // raw_this/weak/shared/handle
	UnregisterAPI   string `json:"unregister_api,omitempty"`
}

// DriverTaskOp 任务提交操作（§9.1）。
type DriverTaskOp struct {
	DomainID  string `json:"domain_id"`
	SubmitAPI string `json:"submit_api"`
	Payload   string `json:"payload"`
	CancelAPI string `json:"cancel_api,omitempty"`
	Join      string `json:"join,omitempty"`
}

// DriverBarrier 屏障（§9.1）。
type DriverBarrier struct {
	Kind         string   `json:"kind"` // start_gate/race_gate/delay
	Sites        []string `json:"sites"`
	Participants int      `json:"participants"`
}

// DriverTeardown 拆除动作（§9.1）。
type DriverTeardown struct {
	Handle   string `json:"handle"`
	API      string `json:"api"` // destroy/close/unregister/reset
	DomainID string `json:"domain_id"`
	Order    int    `json:"order"`
}

// InvariantSpec 预注册不变量（§9.1/R-3.11）。
type InvariantSpec struct {
	InvariantID             string            `json:"invariant_id"`
	Expr                    string            `json:"expr"`
	Expected                string            `json:"expected"`
	OracleType              OracleType        `json:"oracle_type"`
	OracleProvenance        OracleProvenance  `json:"oracle_provenance"`
	SourceRef               string            `json:"source_ref"`
	OnFail                  string            `json:"on_fail"`
	OracleEvaluationContext OracleEvalContext `json:"oracle_evaluation_context"`
	MetaTest                InvariantMetaTest `json:"meta_test"`
}

// InvariantMetaTest meta_test 结果（R-3.13）。
type InvariantMetaTest struct {
	SerializedPass bool `json:"serialized_pass"`
	TriggerFail    bool `json:"trigger_fail"`
}

// NegativeControl 负控制声明（§9.1）。
type NegativeControl struct {
	ID       string `json:"id"`
	Mode     string `json:"mode"` // add_sync/serialize_target/serialize_driver/remove_trigger/generation
	Expected string `json:"expected"`
}

// DriverSpec 声明式驱动（§9.1）。
type DriverSpec struct {
	CandidateID string `json:"candidate_id"`
	FamilyID    string `json:"family_id"`
	Version     int    `json:"version"`

	Phases struct {
		Setup    []string `json:"setup"`
		Race     []string `json:"race"`
		Teardown []string `json:"teardown"`
		Shutdown []string `json:"shutdown"`
	} `json:"phases"`

	Operations []DriverOperation `json:"operations"`
	Domains    []Domain          `json:"domains"`
	RaceGate   struct {
		Mode  string   `json:"mode"`
		Sites []string `json:"sites"`
	} `json:"race_gate"`
	ObjectGraph []DriverObject   `json:"object_graph"`
	Callbacks   []DriverCallback `json:"callbacks"`
	TaskOps     []DriverTaskOp   `json:"task_ops"`
	Barriers    []DriverBarrier  `json:"barriers"`
	Teardown    []DriverTeardown `json:"teardown"`

	RawFixture struct {
		Language string `json:"language"`
		Code     string `json:"code"`
		Lint     string `json:"lint"`
	} `json:"raw_fixture"`

	Invariants     []InvariantSpec `json:"invariants"`
	APIContracts   []APIContract   `json:"api_contracts"`
	InstanceOrigin string          `json:"instance_origin"` // target_api/driver_constructed
	Reachability   struct {
		Entry     string `json:"entry"`
		Path      string `json:"path"`
		PublicAPI bool   `json:"public_api"`
	} `json:"reachability"`
	SeedParam                   string            `json:"seed_param"`
	NegativeControls            []NegativeControl `json:"negative_controls"`
	ExpectedEvidence            []EventType       `json:"expected_evidence"`
	InstrumentationRequirements []string          `json:"instrumentation_requirements"`
	Timeouts                    struct {
		Run int `json:"run"`
		Job int `json:"job"`
	} `json:"timeouts"`
}

// LifecycleContract API 生命周期契约（§12.1）。
type LifecycleContract struct {
	Ownership            string `json:"ownership"`              // target/caller/shared/unknown
	DestroyWhileCallback string `json:"destroy_while_callback"` // cancels/forbidden/undefined/unknown
	UnregisterObligation string `json:"unregister_obligation"`  // target/caller/none/unknown
	CallbackCancellation string `json:"callback_cancellation"`  // guaranteed/best_effort/none/unknown
	ThreadAffinity       string `json:"thread_affinity"`        // any/creator/specific/unknown
	CloseSemantics       string `json:"close_semantics"`        // idempotent/single/unknown
	GenerationGuarantee  string `json:"generation_guarantee"`   // yes/no/unknown
	SourceRef            string `json:"source_ref"`
}

// APIContract API 契约（§12.1）。
type APIContract struct {
	APIID          string            `json:"api_id"`
	ThreadSafety   string            `json:"thread_safety"` // thread_safe/caller_locked/single_threaded/unknown
	CallerLocking  string            `json:"caller_locking"`
	Lifecycle      LifecycleContract `json:"lifecycle"`
	ContractStatus ContractStatus    `json:"contract_status"`
	ContractBasis  []string          `json:"contract_basis"`
	SourceRefs     []string          `json:"source_refs"`
}

// ---------------------------------------------------------------------------
// §11 插桩
// ---------------------------------------------------------------------------

// InstrumentationProfile 插桩能力（§11.2）。
type InstrumentationProfile struct {
	Capability       string   `json:"capability"` // full/limited/none
	AllocatorHook    bool     `json:"allocator_hook"`
	StackObjectHook  bool     `json:"stack_object_hook"`
	StaticRegistry   bool     `json:"static_registry"`
	MemberOffsetMap  bool     `json:"member_offset_map"`
	PoolGeneration   bool     `json:"pool_generation"`
	HandleTable      bool     `json:"handle_table"`
	DomainContext    string   `json:"domain_context"` // tls/explicit/none
	AssertionRuntime bool     `json:"assertion_runtime"`
	RaceGate         bool     `json:"race_gate"`
	CoverageHook     string   `json:"coverage_hook"` // sanitizer_coverage/gcov/none
	Symbolizer       string   `json:"symbolizer"`    // llvm-symbolizer/addr2line/none
	BuildFlags       []string `json:"build_flags"`
	Notes            string   `json:"notes,omitempty"`
}

// IdentityProducers 身份 producer 矩阵（§11.3）。
type IdentityProducers struct {
	Heap   IdentityProducerCell `json:"heap"`
	Stack  IdentityProducerCell `json:"stack"`
	Static IdentityProducerCell `json:"static"`
	Member IdentityProducerCell `json:"member"`
	Pool   IdentityProducerCell `json:"pool"`
	Handle IdentityProducerCell `json:"handle"`
	Domain IdentityProducerCell `json:"domain"`
}

// IdentityProducerCell 单个 storage_class × domain_kind 的能力格（§11.3）。
type IdentityProducerCell struct {
	Producer          string         `json:"producer,omitempty"`
	Present           bool           `json:"present"`
	MaxBindingQuality BindingQuality `json:"max_binding_quality,omitempty"`
	S3Forms           []Form         `json:"s3_forms,omitempty"`
	S2Only            bool           `json:"s2_only,omitempty"`
}

// PlatformPolicy 平台策略（§13.1）。
type PlatformPolicy struct {
	S3Allowed             []Platform      `json:"s3_allowed"`
	MinQuality            PlatformQuality `json:"min_quality"`
	AllowEmulatedExternal bool            `json:"allow_emulated_external"`
}

// TargetProfile Phase 0 目标剖面（§7.5）。
type TargetProfile struct {
	Name              string                 `json:"name"`
	Language          string                 `json:"language"`
	BuildSystem       string                 `json:"build_system"`
	TargetABI         string                 `json:"target_abi"`
	Platform          Platform               `json:"platform"`
	Instrumentation   InstrumentationProfile `json:"instrumentation"`
	IdentityProducers IdentityProducers      `json:"identity_producers"`
	SourceRoot        string                 `json:"source_root"`
	BuildHash         string                 `json:"build_hash"`
}

// ---------------------------------------------------------------------------
// §5.6 TaskProfile
// ---------------------------------------------------------------------------

// EligibleEvent 形态可接受的 (event_type, tool) 组合（§12.6）。
type EligibleEvent struct {
	EventType EventType    `json:"event_type"`
	Tool      EvidenceTool `json:"tool"`
}

// EvidenceEligibility 只能收紧的证据资格（R-5.14）。
type EvidenceEligibility struct {
	Extra map[Form][]EligibleEvent `json:"extra,omitempty"`
}

// EnumerationScope 枚举范围（§4.10）。
type EnumerationScope struct {
	EntryPoints       []string       `json:"entry_points"`
	Reachability      string         `json:"reachability"` // public_api/all
	StorageClasses    []StorageClass `json:"storage_classes"`
	IncludeThirdParty bool           `json:"include_third_party"`
	IncludeGenerated  bool           `json:"include_generated"`
	ModuleRoots       []string       `json:"module_roots"`
	MaxFamilies       int            `json:"max_families"`
}

// StatisticalDefaults 统计默认值，不得低于 §14.6（R-5.15）。
type StatisticalDefaults struct {
	MinRuns            int     `json:"min_runs"`
	TargetUpperBound   float64 `json:"target_upper_bound"`
	ControlUpperBound  float64 `json:"control_upper_bound"`
	MinControlRuns     int     `json:"min_control_runs"`
	MinReproLowerBound float64 `json:"min_repro_lower_bound"`
	CrossSeedMin       int     `json:"cross_seed_min"`
	CrossSeedFraction  float64 `json:"cross_seed_fraction"`
	Alpha              float64 `json:"alpha"`
	Beta               float64 `json:"beta"`
	P0                 float64 `json:"p0"`
	P1                 float64 `json:"p1"`
}

// PriorityWeights 优先级权重（§6.6）。
type PriorityWeights struct {
	Risk        float64 `json:"risk"`
	Novelty     float64 `json:"novelty"`
	ExpectedHit float64 `json:"expected_hit"`
	Cost        float64 `json:"cost"`
	Age         float64 `json:"age"`
	Cold        float64 `json:"cold"`
}

// ClosingPolicy 收尾策略（§6.7）。
type ClosingPolicy struct {
	RequireAllUnits   bool `json:"require_all_units"`
	AllowDeferred     bool `json:"allow_deferred"`
	MaxOpenCandidates int  `json:"max_open_candidates"`
}

// ReconciliationCadence 对账节奏（R-6.30）。
type ReconciliationCadence struct {
	Incremental time.Duration `json:"incremental"`
	FullDaily   bool          `json:"full_daily"`
}

// TaskProfile 任务参数集合（§5.6）。只能参数化文档列出的项。
type TaskProfile struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Forms   []Form `json:"forms"`

	FormFacts                   map[Form][]string           `json:"form_facts,omitempty"`
	EvidenceEligibility         EvidenceEligibility         `json:"evidence_eligibility"`
	EnumerationScope            EnumerationScope            `json:"enumeration_scope"`
	VerificationBackend         string                      `json:"verification_backend"`
	StatisticalDefaults         StatisticalDefaults         `json:"statistical_defaults"`
	InstrumentationRequirements []string                    `json:"instrumentation_requirements"`
	LimitedControlForms         []Form                      `json:"limited_control_forms"`
	ReportFormat                string                      `json:"report_format"`
	DispatchMode                DispatchMode                `json:"dispatch_mode"`
	QueueKinds                  []QueueKind                 `json:"queue_kinds"`
	PriorityWeights             PriorityWeights             `json:"priority_weights"`
	PerRoleCapacity             map[Role]int                `json:"per_role_capacity"`
	LeaseTTL                    map[QueueKind]time.Duration `json:"lease_ttl"`
	OwnerMode                   OwnerMode                   `json:"owner_mode"`
	ReconciliationCadence       ReconciliationCadence       `json:"reconciliation_cadence"`
	SchedulerSeed               int64                       `json:"scheduler_seed"`
	NS3Target                   int                         `json:"n_s3_target"`
	NDriverTarget               int                         `json:"n_driver_target"`

	// PolicyHash 是平台加载 profile 后计算的内容哈希（R-3.5/R-6.25）。
	PolicyHash string `json:"policy_hash,omitempty"`
}

// ---------------------------------------------------------------------------
// §5.4 judge / authorization
// ---------------------------------------------------------------------------

// AuthorizationDecision 授权门记录（§5.5）。
type AuthorizationDecision struct {
	DecisionID string    `json:"decision_id"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	Target     string    `json:"target"`
	PolicyHash string    `json:"policy_hash"`
	Decision   string    `json:"decision"` // allow/deny
	Reason     string    `json:"reason"`
	Timestamp  time.Time `json:"timestamp"`
}

// JudgeVerdict taste judge 结论（§5.4）。
type JudgeVerdict struct {
	VerdictID        string          `json:"verdict_id"`
	QuestionKind     string          `json:"question_kind"`
	SubjectRef       string          `json:"subject_ref"`
	PrecedentRefs    []string        `json:"precedent_refs"`
	Verdict          string          `json:"verdict"`
	Confidence       ConfidenceLevel `json:"confidence"`
	Escalation       string          `json:"escalation"` // none/human
	EscalationReason string          `json:"escalation_reason,omitempty"`
	Model            string          `json:"model"`
	PolicyHash       string          `json:"policy_hash"`
	CreatedAt        time.Time       `json:"created_at"`
}

// ---------------------------------------------------------------------------
// §7 状态机事件
// ---------------------------------------------------------------------------

// TransitionEvent 状态转移审计（R-7.2）。
type TransitionEvent struct {
	From        LifecycleState `json:"from"`
	To          LifecycleState `json:"to"`
	Trigger     string         `json:"trigger"`
	Actor       string         `json:"actor"`
	ClaimID     string         `json:"claim_id,omitempty"`
	Reason      string         `json:"reason"`
	LineageHash string         `json:"lineage_hash"`
	CreatedAt   time.Time      `json:"created_at"`
}

// ---------------------------------------------------------------------------
// §4.8 覆盖统计
// ---------------------------------------------------------------------------

// CoverageStats 覆盖/风险闭合统计（§4.8）。
type CoverageStats struct {
	EnumeratedUnits int `json:"enumerated_units"`
	// ExaminedUnits = candidate_open + concluded：账本"已有动作"的单位数。
	// lifetime 效果测试用它区分"完全没审"与"审到一半"。
	ExaminedUnits  int `json:"examined_units"`
	CandidateUnits int `json:"candidate_units"`
	ConcludedUnits int `json:"concluded_units"`
	// CandidateRate 是 candidate_open 占分母的比例（与 coverage_pct 分开，
	// 因为候选尚未结账，不能算 coverage）。
	CandidateRate           float64 `json:"candidate_rate"`
	RiskClosedUnits         int     `json:"risk_closed_units"`
	FindingUnits            int     `json:"finding_units"`
	UnknownRows             int     `json:"unknown_rows"`
	UnknownEstimate         int     `json:"unknown_estimate"`
	ReviewedConcludedUnits  int     `json:"reviewed_concluded_units"`
	ReviewedRiskClosedUnits int     `json:"reviewed_risk_closed_units"`
	CoveragePct             float64 `json:"coverage_pct"`
	RiskClosedPctAll        float64 `json:"risk_closed_pct_all"`
	RiskClosedPctReviewed   float64 `json:"risk_closed_pct_reviewed"`
	HitRateUnits            float64 `json:"hit_rate_units"`
	UnknownRate             float64 `json:"unknown_rate"`
	ReviewedConcludedRate   float64 `json:"reviewed_concluded_rate"`
}
