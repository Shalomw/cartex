package lifetime

import "time"

// ReconcileSnapshot 是对账环的只读输入快照（§6.10）。store 负责从状态机、
// 覆盖账本和队列投影中装载；RunChecks 只做确定性判断，不写库。
type ReconcileSnapshot struct {
	TaskID          string
	LineageID       string
	Now             time.Time
	IndexAvailable  bool
	BudgetExhausted bool

	EnumeratedUnits   []CoverageUnit
	Candidates        []Candidate
	Assessments       []Assessment
	Experiments       []Experiment
	Observations      []Observation
	NoRisks           []NoRisk
	WorkItems         []WorkItem
	OwnerJournal      []OwnerJournalEntry
	ExistingDedupKeys []string

	// FindingExists 按 candidate_id 报告该 claim 是否已生成对外 finding（C9）。
	FindingExists map[string]bool
}

// CheckID 对账检查编号（§6.10）。
type CheckID string

const (
	CheckC1  CheckID = "C1"  // 覆盖完整性
	CheckC2  CheckID = "C2"  // 收尾候选
	CheckC3  CheckID = "C3"  // candidate 停滞
	CheckC4  CheckID = "C4"  // claim 停滞
	CheckC5  CheckID = "C5"  // lineage stale
	CheckC6  CheckID = "C6"  // control 缺失
	CheckC7  CheckID = "C7"  // 队列健康
	CheckC8  CheckID = "C8"  // no_risk 不完整
	CheckC9  CheckID = "C9"  // report gate 不一致
	CheckC10 CheckID = "C10" // 预算收尾
	CheckC11 CheckID = "C11" // 重复候选
	CheckC12 CheckID = "C12" // owner 记忆 stale
)
