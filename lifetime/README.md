# lifetime — ARTEX 多线程对象生命周期 profile

本包实现 `02-ARTEX二开方案/ARTEX二开方案-v1.8.md` 的确定性内核与 P1/P2 竖切：
对象族/并发域/候选、S1–S4 谓词、覆盖账本、Q1–Q5 队列、对账 C1–C12、角色工具面，
以及独立的 PostgreSQL 持久化与 HTTP 服务。

ARTEX 核心只增加 lifetime 工具注册点（`agent/lifetime_tools.go` + `ToolSet`
接线）；领域逻辑作为独立服务落地（R-15.1），核心不承载 lifetime 语义。

## 包结构

| 路径 | 内容 |
|---|---|
| `enums.go` / `types.go` | §2–§6、§9、§10 的实体与枚举契约 |
| `ids.go` / `lineage.go` | §2.2 内容寻址 ID、build/lineage、confirmation lineage 约束 |
| `profile.go` | §5.6 TaskProfile、§14.6 默认值与下限校验、policy_hash |
| `forms.go` | §4.4/§12 形态矩阵、form facts、四问、eligible/最低 fidelity |
| `predicates.go` | §3.6–3.8 S1–S4、对外 finding、confidence、review 独立性 |
| `state.go` | §7 生命周期状态机与合法转移 |
| `coverage.go` | §4.6–4.8 覆盖状态、闭合规则、统计公式 |
| `queue.go` | §6 Q1–Q5 ready 谓词、优先级、claim/lease 状态迁移 |
| `reconcile.go` | §6.10 C1–C12 对账检查 |
| `roles.go` / `tools.go` | §5.1 角色定义、§16 工具签名、§16.6 工具面矩阵 |
| `index/` | 启发式 C++ 索引器（class/struct、new/shared_ptr、sync、callback、domain） |
| `store/` | PostgreSQL schema 与持久化（`lifetime_` 前缀，独立于 ARTEX 主 schema） |
| `service/` | §16.2/§16.5 工具面的 HTTP JSON 服务 + 索引 bootstrap/查询 |
| `cmd/lifetimed` | 独立服务入口 |
| `cmd/lifetime-index` | 索引器调试 CLI（输出并摘要 IndexOutput） |

## 启动

```bash
cd cartex
go build ./lifetime/... ./cmd/lifetimed/

ARTEX_PG_DSN='postgres://artex:devpass@127.0.0.1:5432/artex?sslmode=disable' \
  ./lifetimed -addr :8790
```

服务启动时自动创建 `lifetime_*` 表（幂等，advisory lock 7337742001）。
可选 `-profile profile.json` 加载并校验 TaskProfile；`GET /v1/profile` 返回。

## HTTP 工具面

请求头：

- `X-Lifetime-Role`：`lifetime_auditor` / `lifetime_verifier` / `lifetime_reviewer` /
  `reporter` / `planner` / `owner` / `reconciler` / `mainagent`
- `X-Lifetime-Session`：会话 ID（写入 provenance / claim_log）

路由：

```text
GET  /healthz
GET  /v1/profile
GET  /v1/roles
POST /v1/tasks/{task_id}/bootstrap          # planner / reconciler：索引 + 覆盖初始化
GET  /v1/tasks/{task_id}/families?file=&subdir=&limit=
GET  /v1/tasks/{task_id}/families/{family_id}
GET  /v1/tasks/{task_id}/coverage?limit=
GET  /v1/tasks/{task_id}/candidates
POST /v1/tasks/{task_id}/tools/{tool}
POST /v1/tasks/{task_id}/candidates/{candidate_id}/assess   # planner / reconciler
POST /v1/tasks/{task_id}/reconcile        # reconciler / planner
```

`bootstrap` 接收 `{source_root, target_files, target_subdir, max_files,
max_pairs, profile?}`：调用 `lifetime/index` 生成 `IndexOutput`，创建
lineage/profile，入库索引，并按 `family × domain_pair × form` 初始化覆盖账本
与 Q1。`max_pairs` 默认 2（自对 + 首个跨域对），避免启发式域对把账本撑爆；
上限 8。

`assess` 是平台评估入口：从已落库的 index / static support / candidate /
observation / experiment 派生 S1–S4，物化 Assessment，并按状态自动投影
Q2/Q3/Q4（R-6.9）。body 可带 `{"lineage_id":"..."}`；缺省从候选的覆盖单位解析。
agent 不能直接调用它写 level（I-2）。

`{tool}` 支持本里程碑落地的发现与队列工具：

```text
record_static_support   record_candidate   record_no_risk
record_unknown          record_new_lead    request_probe
claim                   renew              release         complete
```

工具面由 `lifetime.ValidateToolCall` 强制（§16.6/R-16.9）；队列权限再由
`lifetime.CanClaimQueue` 收窄。`build_driver`/`submit_verification`/`reproduce`/
复核/报告工具属于 P3/P4，当前返回 `501 not_implemented`，但角色矩阵已就位。

示例：

```bash
curl -s localhost:8790/v1/tasks/t1/tools/claim \
  -H 'X-Lifetime-Role: lifetime_verifier' \
  -H 'X-Lifetime-Session: verifier-1' \
  -d '{"queue_kind":"Q2","lease_ttl_seconds":600}'
```

M0 信任上述头；生产部署必须放在受信网络或反向代理之后。

## ARTEX 工具桥

设置以下环境变量后，ARTEX worker/planner 会自动获得 `lifetime_*` 工具：

```text
LIFETIME_API_URL   http://127.0.0.1:8790
LIFETIME_TASK_ID   lifetimed 的 task id（SEC-bench 容器内为 "1"）
LIFETIME_SESSION   可选；缺省用 worker 名/role + pid
```

- worker（role=`lifetime_auditor`）：`lifetime_families`、`lifetime_family`、
  `lifetime_coverage`、`lifetime_candidates` + 发现写回
  `lifetime_record_static_support`、`lifetime_record_candidate`、
  `lifetime_record_no_risk`、`lifetime_record_unknown`、`lifetime_request_probe`。
- planner（role=`planner`）：只读工具 + `lifetime_assess`。
- 未设置 `LIFETIME_API_URL`/`LIFETIME_TASK_ID` 时工具完全不出现，原有 agent 行为不变。
- 工具调用失败（403/404/网络）作为工具错误返回，不会中断运行。

SEC-bench-Pro 的 `lifetime=true` 配置会在容器内启动 `lifetimed`、消费
`meta.target_source_files`/`target_subdir` 做 bootstrap，并把
`lifetime_{bootstrap,coverage,candidates,families}.json` 收集到输出目录。
打包脚本见 `benchmarks/secbench-pro/repo/scripts/build_lifetime_bundle.sh`。

## 测试

```bash
cd cartex
go test ./lifetime/... -count=1
go vet ./lifetime/...
```

`lifetime/store` 的集成测试在未设置 `LIFETIME_TEST_DSN` / `ARTEX_PG_DSN` 时自动
skip；设置后使用唯一 task_id，测试结束只清理本 task 的行。

## 实现边界

- 已实现：确定性 ID/lineage、S1–S4、覆盖闭合与统计、Q1–Q5 投影与原子 claim、
  C1–C12、TaskProfile 校验、角色工具面、PostgreSQL 持久化、独立 HTTP 服务、
  启发式 C++ 索引器、bootstrap/查询端点、ARTEX `lifetime_*` 工具桥、
  平台评估端点（S1/S2 → 自动 Q2 投影）。
- 未实现（P3/P4）：tree-sitter/libclang 精确索引（当前为启发式，S1 只能到
  S1_provisional）、DriverSpec 编译/执行 harness、统计引擎（SPRT/control
  power）、review sandbox、报告服务与 authorization 门。schema 与类型已为这些
  组件预留。
- 索引产物通过 `store.IngestIndex` 入库；`index_quality` / `type_resolution`
  持久化在 `lifetime_index_runs`，供 S1 门禁复算。
- `CoverageStats` 的 `*_pct` 字段按百分数 0–100 返回（分母定义见 §4.8）。
