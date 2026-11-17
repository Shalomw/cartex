# lifetime/store

ARTEX lifetime profile 的 PostgreSQL 持久化层（§15.3）。只依赖标准库与
`github.com/jackc/pgx/v5/stdlib`（database/sql 驱动 `pgx`），无 ORM。

```go
import "github.com/Autumn-27/artex/lifetime/store"

st, err := store.Open(ctx, dsn) // Ping + advisory lock 7337742001 + 应用 schema.sql
defer st.Close()
```

`Open` 在单连接上持有 `pg_advisory_lock(7337742001)` 执行 embedded
`schema.sql`，随后解锁；schema 全部 `CREATE ... IF NOT EXISTS`，多进程并发打开安全。
`DB()` 返回底层 `*sql.DB`，供 service 只读查询或自定义事务。

## 错误

- `ErrNotFound`：目标行不存在。
- `ErrLeaseLost`：claim 丢失（claim_id/session 不匹配、租约已过期或已完成）。
- `ErrAreaOwned`：area 被其他未过期 session 持有（R-6.33）。

## 公开方法（service 层速查）

### 生命周期 / profile（lineage.go）

```go
CreateLineage(ctx, taskID string, in lifetime.LineageInput) (lineageID string, err error)
GetLineage(ctx, lineageID string) (in *lifetime.LineageInput, taskID string, err error)
RegisterTaskProfile(ctx, taskID string, p *lifetime.TaskProfile) (policyHash string, err error)
```

`RegisterTaskProfile` 依次执行 `ApplyTaskProfileDefaults` → `ValidateTaskProfile`
→ `ComputePolicyHash`，返回并写回 `p.PolicyHash`。

### 索引与覆盖初始化（index.go）

```go
IngestIndex(ctx, taskID, lineageID string, idx lifetime.IndexOutput) error
InitCoverage(ctx, taskID, lineageID string, units []lifetime.CoverageUnit, packages []lifetime.WorkPackage) error
GetIndexOutput(ctx, taskID, lineageID string) (*lifetime.IndexOutput, error)
ListFamilies(ctx, taskID string) ([]lifetime.ObjectFamily, error)   // 每 family 取最新 lineage
ListCoverage(ctx, taskID string) ([]lifetime.CoverageUnit, error)
```

`IngestIndex` 同时写 `lifetime_index_runs`（`index_quality` / `type_resolution`
是 S1 门禁输入，R-8.4），并按主键幂等 upsert 索引事实。
`GetIndexOutput` 从这些表重建 `IndexOutput`，供评估端点使用。

### 发现写回（discovery.go）

```go
RecordStaticSupport(ctx, taskID string, s lifetime.StaticSupport) (supportID string, err error)
RecordCandidate(ctx, taskID string, c lifetime.Candidate) (candidateID string, err error)
RecordNoRisk(ctx, taskID string, n lifetime.NoRisk) (noRiskID string, err error)
RecordUnknown(ctx, taskID string, u lifetime.UnknownRecord) (unknownID string, err error)
RecordNewLead(ctx, taskID string, l lifetime.NewLead) (leadID string, err error)
RequestProbe(ctx, taskID, candidateID, question, expectedSignal string) (probeRequestID string, err error)
GetCandidate(ctx, candidateID string) (*lifetime.Candidate, error)
GetStaticSupport(ctx, supportID string) (*lifetime.StaticSupport, error)
ListCandidates(ctx, taskID string) ([]lifetime.Candidate, error)
UpdateCandidateBudget(ctx, candidateID string, remaining, used int) error // 平台维护，R-6.9
```

- `RecordCandidate` 强制 `domain_pair_id` 非空（R-2.1），调用
  `ValidateFourQuestions` + `FormFactsComplete`，并执行 R-8.12 的结构检查
  （至少一个竞争访问、chain 非空且角色合法、sync 有 `protects`）。
  `candidate_key`/`mechanism_key` 为空时按 §2.2 计算并写回；
  mechanism 的 `root_cause_kind` 使用 `primary_form`（契约无该字段）。
  成功后把对应覆盖行推进为 `candidate_open`。
- `RecordStaticSupport` 要求 chain 含 `lifetime_point` 与 `access` 且每节点有
  `file:line`；`citation_check` 为 unresolved/mismatch 时允许写入，由 S1 谓词拒绝。
- `RecordNoRisk` 调用 `ValidateNoRiskArgument`；带 `unit_id` 时先从覆盖账本
  解析 `family_id/domain_pair_id/form`（未知 unit 或字段冲突拒绝）；`covers_current_form=true`
  时把对应覆盖行设为 `no_risk`，`evidence_ref=no_risk_id`。
- `RequestProbe` 写 `lifetime_probes` 并投影 Q2：`dedup_key="Q2:probe:<candidate_id>"`，
  `reason="probe"`。

### evidence（append-only，evidence.go）

```go
AppendObservation(ctx, taskID string, o lifetime.Observation) error // 主键冲突报错
AppendExperiment(ctx, taskID string, e lifetime.Experiment) error   // 主键冲突报错
MaterializeAssessment(ctx, taskID string, a lifetime.Assessment) error // 只 INSERT + 自动投影 Q2/Q3/Q4
MarkAssessmentSuperseded(ctx, assessmentID string) error            // 平台状态列
MarkAssessmentStale(ctx, assessmentID string, stale bool) error      // 平台状态列
ListAssessments(ctx, taskID string) ([]lifetime.Assessment, error)
ListObservations(ctx, candidateID string) ([]lifetime.Observation, error)
ListExperiments(ctx, candidateID string) ([]lifetime.Experiment, error)
```

`MaterializeAssessment` 不更新既有 evidence 字段；同一
`(candidate_id, lineage_id)` 的新判定应插入新行，旧行由调用方标 superseded。
同一事务内按 assessment 状态自动投影下游队列：S3/S4→Q3、S2→Q2(verify)、
S1/S1_provisional→Q2(probe)、confirmed+reportable_external→Q4（R-6.9）。
`List*` 返回列与 payload 合并后的最新平台状态。

### 覆盖账本（coverage.go）

```go
GetCoverage(ctx, unitID string) (*lifetime.CoverageUnit, error)
UpdateCoverageStatus(ctx, unitID string, status lifetime.CoverageStatus, reason, evidenceRef, proofRef, reviewStatus, lineageID string) error
CoverageStats(ctx, taskID string, unknownEstimate int) (lifetime.CoverageStats, error)
```

`UpdateCoverageStatus` 中 `reviewStatus`/`lineageID` 为空串时保留原值。
`CoverageStats` 读全部账本行后调用 `lifetime.ComputeCoverageStats`；
该函数按实现以百分数（0–100）返回 `*_pct` 字段。

### 工作队列（queue.go）

```go
ProjectWorkItem(ctx, taskID string, it lifetime.WorkItem) (itemID string, err error)
ListReady(ctx, taskID string, q lifetime.QueueKind, limit int) ([]lifetime.WorkItem, error)
Claim(ctx, taskID string, q lifetime.QueueKind, sessionID string, ttl time.Duration) (*lifetime.WorkItem, error)
Renew(ctx, itemID, claimID, sessionID string, ttl time.Duration) (*lifetime.WorkItem, error)
Release(ctx, itemID, claimID, sessionID, reason string) error
Complete(ctx, itemID, claimID, sessionID, outcomeRef string) error
ExpireLeases(ctx, taskID string) (int, error)
ClaimLog(ctx, taskID string, limit int) ([]lifetime.ClaimLog, error)
```

- `ProjectWorkItem` 按 `(task_id, queue_kind, dedup_key)` 幂等：已存在活跃项时返回
  既有 `item_id`，不重置状态。`dedup_key` 必填。
- `Claim` 单条 `UPDATE ... FOR UPDATE SKIP LOCKED ... RETURNING`，并发只有一个成功；
  队列空返回 `(nil, nil)`。
- `ListReady` / `Claim` 的并列用 `md5(item_id || scheduler_seed)` / `SortKeyHash`
  打破，种子取 task 最新 profile（R-6.43/I-10）。
- `Renew`/`Release`/`Complete` 校验 claim/session，失配返回 `ErrLeaseLost`。
- `Complete` 对同一 claim 幂等（R-6.13）。
- `ExpireLeases` 按 `lifetime.ApplyExpire`：attempts>=3 → `blocked`，否则回 `ready`。
- 所有 claim 变更写 `lifetime_claim_log`（R-6.42），`scheduler_seed` 取 task 最新
  profile 的 `scheduler_seed`（缺省 0）。

### owner（owner.go）

```go
AcquireArea(ctx, areaID, sessionID string, ttl time.Duration) error // 同 session 续租；他人持有→ErrAreaOwned
ReleaseArea(ctx, areaID, sessionID string) error
AppendJournal(ctx, taskID, areaID string, e lifetime.OwnerJournalEntry) (entryID string, err error)
ListJournal(ctx, areaID string, includeStale bool) ([]lifetime.OwnerJournalEntry, error)
MarkJournalStale(ctx, areaID, sourceHash, indexHash string) (int, error)
```

`MarkJournalStale` 把与给定 hash 不一致的未 stale 条目标 stale（R-6.38），不删除。

### 对账（reconcile.go）

```go
SnapshotForReconcile(ctx, taskID string) (*lifetime.ReconcileSnapshot, error)
SaveReconciliation(ctx, taskID string, run lifetime.ReconciliationRun, findings []lifetime.ReconciliationFinding) error
ListReconciliationFindings(ctx, taskID string, status string) ([]lifetime.ReconciliationFinding, error)
```

`SnapshotForReconcile` 装载 units/candidates/assessments/experiments/observations/
no_risk/work_items/owner_journal/existing open dedup keys；`IndexAvailable` = 是否有
family。两个已知限制：

- `FindingExists` 返回空 map：本 schema 不拥有 `findings` 表（report service 维护），
  C9 的 finding 一致性需 report service 侧补充。
- `BudgetExhausted` 恒为 `false`：没有 task 级预算表；预算在 candidate.budget 与
  planner policy 中，由 platform 判定。

`SaveReconciliation` 按 `sweep_id` upsert run；finding 在写入前检查同 task 的 open
`dedup_key`，已存在则跳过（R-6.29）。

## 表清单（全部 lifetime_ 前缀）

| 表 / 视图 | 用途 |
|---|---|
| `lifetime_task_profiles` | TaskProfile（§5.6） |
| `lifetime_lineages` | lineage 八分量（§2.2/§10.3） |
| `lifetime_object_families` | 对象族（§4.1） |
| `lifetime_object_relations` | 跨 family 关系（§4.9） |
| `lifetime_domains` / `lifetime_domain_pairs` | 并发域 / 域对（§4.2） |
| `lifetime_lifetime_points` / `lifetime_sync_primitives` / `lifetime_callbacks` | 索引事实（§8.1） |
| `lifetime_index_unknowns` | 索引无法解析范围（§8.1） |
| `lifetime_static_support` | 静态事实与链（§8.2） |
| `lifetime_index_runs` | 索引质量 / 类型解析（S1 门禁输入，§8.1） |
| `lifetime_candidates` | 候选（§8.3） |
| `lifetime_observations` | Observation，append-only（§3.2） |
| `lifetime_experiments` | Experiment，append-only（§3.3） |
| `lifetime_assessments` | Assessment，append-only（§3.4） |
| `lifetime_lifetime_claims`（视图） | S3/S4、claim lineage、未 refuted/stale（§3.5） |
| `lifetime_work_packages` | family × domain_pair 发现租约（§4.5） |
| `lifetime_coverage_units` | family/relation × pair × form 账本（§4.5/§4.6） |
| `lifetime_no_risk` / `lifetime_unknowns` / `lifetime_new_leads` | no_risk / unknown / 新线索（§4.7） |
| `lifetime_work_queue` | Q1–Q5 工作项（§6.5） |
| `lifetime_claim_log` | 调度日志，seq 单调（§6.12） |
| `lifetime_area_leases` / `lifetime_owner_journal` | owner 租约与记忆（§6.11） |
| `lifetime_reconciliation_findings` / `lifetime_reconciliation_runs` | 对账发现 / sweep 统计（§6.10） |
| `lifetime_probes` | request_probe 请求（§16.2） |

关键索引：queue `(task_id, queue_kind, state, priority DESC, created_at)`；
candidates `(task_id, candidate_key)`；assessments `(candidate_id, lineage_id)`；
coverage `(task_id, status)`；work_queue 与 reconciliation_findings 的
`dedup_key` 唯一 partial index（活跃/open 状态）。

平台扩展列（契约实体之外、仅用于审计/按 task scope）：`lifetime_claim_log`
带 `task_id / reason / outcome_ref`；`lifetime_owner_journal` 带 `task_id`；
`lifetime_lineages` 带 `payload`（完整 LineageInput JSON）；`lifetime_work_packages`
带 `created_at / updated_at`。

## 测试

```bash
LIFETIME_TEST_DSN='postgres://user:pass@127.0.0.1:5432/db?sslmode=disable' \
  go test ./lifetime/store/ -count=1
```

未设置 `LIFETIME_TEST_DSN`（或 `ARTEX_PG_DSN`）时集成测试自动 skip。
测试使用唯一 `test-<unixnano>` task_id，结束时只删除该 task 的行。
