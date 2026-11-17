-- ARTEX lifetime profile persistence schema.
--
-- 规格：02-ARTEX二开方案/ARTEX二开方案-v1.8.md
--   §2.1 核心实体、§2.2 ID 规则、§3.2-3.4 evidence、§4.5-4.7 覆盖账本、
--   §6.5-6.12 队列/owner/对账、§8.1-8.3 索引与候选、§10 复核、§15.3 表清单。
--
-- 约定：
--   * 所有表使用 lifetime_ 前缀；只新增本 profile 的表，不修改 ARTEX 现有表。
--   * 全部 CREATE 语句幂等（IF NOT EXISTS / CREATE OR REPLACE）。
--   * evidence 表（observations/experiments/assessments）append-only：
--     store 只 INSERT；仅状态列允许由平台单独 UPDATE。
--   * 时间戳统一 timestamptz；哈希/ID 为 hex 文本。
--   * agent 只写候选/事实/DriverSpec/review evidence；平台写 evidence（I-1/I-2）。

-- ---------------------------------------------------------------------------
-- §5.6 TaskProfile
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_task_profiles (
    task_id     text        NOT NULL,
    name        text        NOT NULL,
    version     text        NOT NULL,
    payload     jsonb       NOT NULL,
    policy_hash text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, name, version)
);

CREATE INDEX IF NOT EXISTS lifetime_task_profiles_task_idx
    ON lifetime_task_profiles (task_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- §2.2 / §10.3 lineage
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_lineages (
    lineage_id                    text        PRIMARY KEY,
    task_id                       text        NOT NULL,
    source_manifest_hash          text        NOT NULL DEFAULT '',
    index_hash                    text        NOT NULL DEFAULT '',
    build_id                      text        NOT NULL DEFAULT '',
    driver_spec_hash              text        NOT NULL DEFAULT '',
    policy_hash                   text        NOT NULL DEFAULT '',
    profile_version               text        NOT NULL DEFAULT '',
    harness_version               text        NOT NULL DEFAULT '',
    instrumentation_profile_hash  text        NOT NULL DEFAULT '',
    -- payload 保留 LineageInput 原始 JSON，便于 service 层直接回读。
    payload                       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at                    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_lineages_task_idx
    ON lifetime_lineages (task_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- §4.1 / §4.2 / §4.9 / §8.1 索引产物
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_object_families (
    task_id    text        NOT NULL,
    family_id  text        NOT NULL,
    lineage_id text        NOT NULL,
    index_hash text        NOT NULL DEFAULT '',
    payload    jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, family_id, lineage_id)
);

CREATE INDEX IF NOT EXISTS lifetime_object_families_task_idx
    ON lifetime_object_families (task_id, family_id);

CREATE TABLE IF NOT EXISTS lifetime_object_relations (
    task_id     text        NOT NULL,
    relation_id text        NOT NULL,
    lineage_id  text        NOT NULL,
    index_hash  text        NOT NULL DEFAULT '',
    payload     jsonb       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, relation_id, lineage_id)
);

CREATE INDEX IF NOT EXISTS lifetime_object_relations_task_idx
    ON lifetime_object_relations (task_id, relation_id);

CREATE TABLE IF NOT EXISTS lifetime_domains (
    task_id         text        NOT NULL,
    domain_class_id text        NOT NULL,
    lineage_id      text        NOT NULL,
    index_hash      text        NOT NULL DEFAULT '',
    payload         jsonb       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, domain_class_id, lineage_id)
);

CREATE INDEX IF NOT EXISTS lifetime_domains_task_idx
    ON lifetime_domains (task_id, domain_class_id);

CREATE TABLE IF NOT EXISTS lifetime_domain_pairs (
    task_id       text        NOT NULL,
    pair_class_id text        NOT NULL,
    lineage_id    text        NOT NULL,
    index_hash    text        NOT NULL DEFAULT '',
    payload       jsonb       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, pair_class_id, lineage_id)
);

CREATE INDEX IF NOT EXISTS lifetime_domain_pairs_task_idx
    ON lifetime_domain_pairs (task_id, pair_class_id);

CREATE TABLE IF NOT EXISTS lifetime_lifetime_points (
    task_id    text        NOT NULL,
    family_id  text        NOT NULL,
    site       text        NOT NULL,
    role       text        NOT NULL,
    source_ref text        NOT NULL DEFAULT '',
    lineage_id text        NOT NULL,
    index_hash text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, family_id, site, role, source_ref, lineage_id)
);

CREATE TABLE IF NOT EXISTS lifetime_sync_primitives (
    task_id    text        NOT NULL,
    primitive  text        NOT NULL,
    scope      text        NOT NULL,
    protects   text        NOT NULL,
    file_line  text        NOT NULL,
    lineage_id text        NOT NULL,
    index_hash text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, primitive, scope, protects, file_line, lineage_id)
);

CREATE TABLE IF NOT EXISTS lifetime_callbacks (
    task_id         text        NOT NULL,
    register_site   text        NOT NULL,
    unregister_site text        NOT NULL DEFAULT '',
    capture         text        NOT NULL,
    source_ref      text        NOT NULL DEFAULT '',
    lineage_id      text        NOT NULL,
    index_hash      text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, register_site, unregister_site, capture, source_ref, lineage_id)
);

-- detail 可能很长，用 md5(detail) 参与唯一索引，避免 btree 行大小限制。
CREATE TABLE IF NOT EXISTS lifetime_index_unknowns (
    task_id    text        NOT NULL,
    scope      text        NOT NULL,
    reason     text        NOT NULL,
    detail     text        NOT NULL DEFAULT '',
    lineage_id text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS lifetime_index_unknowns_key_idx
    ON lifetime_index_unknowns (task_id, lineage_id, scope, reason, (md5(detail)));

-- ---------------------------------------------------------------------------
-- §8.2 静态证据
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_static_support (
    support_id      text        PRIMARY KEY,
    task_id         text        NOT NULL,
    unit_id         text        NOT NULL DEFAULT '',
    candidate_id    text        NOT NULL DEFAULT '',
    facts           jsonb       NOT NULL DEFAULT '[]'::jsonb,
    chain           jsonb       NOT NULL DEFAULT '[]'::jsonb,
    citation_checks jsonb       NOT NULL DEFAULT '[]'::jsonb,
    index_hash      text        NOT NULL DEFAULT '',
    created_by      text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_static_support_task_idx
    ON lifetime_static_support (task_id, unit_id);

-- ---------------------------------------------------------------------------
-- §8.3 候选
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_candidates (
    candidate_id     text        PRIMARY KEY,
    task_id          text        NOT NULL,
    unit_id          text        NOT NULL DEFAULT '',
    support_id       text        NOT NULL DEFAULT '',
    candidate_key    text        NOT NULL DEFAULT '',
    mechanism_key    text        NOT NULL DEFAULT '',
    primary_form     text        NOT NULL DEFAULT '',
    domain_pair_id   text        NOT NULL,
    payload          jsonb       NOT NULL,
    contract_status  text        NOT NULL DEFAULT 'unknown',
    budget_remaining integer     NOT NULL DEFAULT 0,
    budget_used      integer     NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_candidates_key_idx
    ON lifetime_candidates (task_id, candidate_key);
CREATE INDEX IF NOT EXISTS lifetime_candidates_mechanism_idx
    ON lifetime_candidates (task_id, mechanism_key);
CREATE INDEX IF NOT EXISTS lifetime_candidates_task_idx
    ON lifetime_candidates (task_id, created_at);

-- ---------------------------------------------------------------------------
-- §3.2 Observation（append-only）
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_observations (
    observation_id text        PRIMARY KEY,
    task_id        text        NOT NULL,
    experiment_id  text        NOT NULL DEFAULT '',
    candidate_id   text        NOT NULL DEFAULT '',
    payload        jsonb       NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_observations_candidate_idx
    ON lifetime_observations (candidate_id, created_at);
CREATE INDEX IF NOT EXISTS lifetime_observations_task_idx
    ON lifetime_observations (task_id, created_at);

-- ---------------------------------------------------------------------------
-- §3.3 Experiment（append-only）
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_experiments (
    experiment_id  text        PRIMARY KEY,
    task_id        text        NOT NULL,
    candidate_id   text        NOT NULL DEFAULT '',
    lineage_id     text        NOT NULL DEFAULT '',
    lineage_role   text        NOT NULL DEFAULT 'claim',
    confirmation_of text       NOT NULL DEFAULT '',
    kind           text        NOT NULL DEFAULT 'candidate',
    state          text        NOT NULL DEFAULT 'queued',
    payload        jsonb       NOT NULL,
    started_at     timestamptz,
    finished_at    timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_experiments_candidate_idx
    ON lifetime_experiments (candidate_id, lineage_role);
CREATE INDEX IF NOT EXISTS lifetime_experiments_task_idx
    ON lifetime_experiments (task_id, created_at);

-- ---------------------------------------------------------------------------
-- §3.4 Assessment（append-only evidence；status 是平台状态列）
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_assessments (
    assessment_id      text        PRIMARY KEY,
    task_id            text        NOT NULL,
    candidate_id       text        NOT NULL,
    candidate_key      text        NOT NULL DEFAULT '',
    lineage_id         text        NOT NULL DEFAULT '',
    lineage_role       text        NOT NULL DEFAULT 'claim',
    confirmation_of    text        NOT NULL DEFAULT '',
    level              text        NOT NULL DEFAULT '',
    confidence         text        NOT NULL DEFAULT 'unknown',
    reportable_internal boolean    NOT NULL DEFAULT false,
    reportable_external boolean    NOT NULL DEFAULT false,
    status             text        NOT NULL DEFAULT 'open',
    stale              boolean     NOT NULL DEFAULT false,
    review_stale       boolean     NOT NULL DEFAULT false,
    payload            jsonb       NOT NULL,
    derived_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_assessments_cand_lineage_idx
    ON lifetime_assessments (candidate_id, lineage_id);
CREATE INDEX IF NOT EXISTS lifetime_assessments_task_idx
    ON lifetime_assessments (task_id, derived_at);
CREATE INDEX IF NOT EXISTS lifetime_assessments_status_idx
    ON lifetime_assessments (task_id, status);

-- §3.5 claim 视图：S3/S4、claim lineage、未 refuted/stale。
CREATE OR REPLACE VIEW lifetime_lifetime_claims AS
SELECT *
FROM lifetime_assessments
WHERE level IN ('S3', 'S4')
  AND lineage_role = 'claim'
  AND stale = false
  AND status NOT IN ('refuted', 'superseded');

-- ---------------------------------------------------------------------------
-- §4.5 work package / coverage ledger
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_work_packages (
    package_id       text        PRIMARY KEY,
    task_id          text        NOT NULL,
    family_id        text        NOT NULL DEFAULT '',
    domain_pair_id   text        NOT NULL DEFAULT '',
    applicable_forms jsonb       NOT NULL DEFAULT '[]'::jsonb,
    area_id          text        NOT NULL DEFAULT '',
    lease_owner      text        NOT NULL DEFAULT '',
    leased_at        timestamptz,
    expires_at       timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_work_packages_task_idx
    ON lifetime_work_packages (task_id, family_id, domain_pair_id);

CREATE TABLE IF NOT EXISTS lifetime_coverage_units (
    unit_id       text        PRIMARY KEY,
    task_id       text        NOT NULL,
    unit_kind     text        NOT NULL,
    family_id     text,
    relation_id   text,
    domain_pair_id text       NOT NULL,
    form          text        NOT NULL,
    status        text        NOT NULL DEFAULT 'unexamined',
    reason        text        NOT NULL DEFAULT '',
    evidence_ref  text        NOT NULL DEFAULT '',
    proof_ref     text        NOT NULL DEFAULT '',
    review_status text        NOT NULL DEFAULT 'self_asserted',
    lineage_id    text        NOT NULL DEFAULT '',
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- 自然键唯一：family/relation 可为空，用 COALESCE 表达式索引；
-- PostgreSQL 的 UNIQUE 表约束不允许表达式，因此必须写成独立唯一索引。
CREATE UNIQUE INDEX IF NOT EXISTS lifetime_coverage_units_natural_idx
    ON lifetime_coverage_units (
        task_id,
        unit_kind,
        (COALESCE(family_id, '')),
        (COALESCE(relation_id, '')),
        domain_pair_id,
        form
    );

CREATE INDEX IF NOT EXISTS lifetime_coverage_units_task_status_idx
    ON lifetime_coverage_units (task_id, status);

-- ---------------------------------------------------------------------------
-- §4.7 no_risk / unknown / new_lead
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_no_risk (
    no_risk_id          text        PRIMARY KEY,
    task_id             text        NOT NULL,
    family_id           text        NOT NULL DEFAULT '',
    domain_pair_id      text        NOT NULL DEFAULT '',
    form                text        NOT NULL,
    arguments           jsonb       NOT NULL DEFAULT '[]'::jsonb,
    covers_current_form boolean     NOT NULL DEFAULT false,
    review_status       text        NOT NULL DEFAULT 'self_asserted',
    reviewer_id         text        NOT NULL DEFAULT '',
    created_by          text        NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_no_risk_unit_idx
    ON lifetime_no_risk (task_id, family_id, domain_pair_id, form);

CREATE TABLE IF NOT EXISTS lifetime_unknowns (
    unknown_id text        PRIMARY KEY,
    task_id    text        NOT NULL,
    family_id  text        NOT NULL DEFAULT '',
    scope      text        NOT NULL,
    reason     text        NOT NULL,
    detail     text        NOT NULL DEFAULT '',
    lineage_id text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_unknowns_task_idx
    ON lifetime_unknowns (task_id, created_at);

CREATE TABLE IF NOT EXISTS lifetime_new_leads (
    lead_id      text        PRIMARY KEY,
    task_id      text        NOT NULL,
    source_event text        NOT NULL DEFAULT '',
    family_id    text        NOT NULL DEFAULT '',
    domains      jsonb       NOT NULL DEFAULT '[]'::jsonb,
    reason       text        NOT NULL DEFAULT '',
    status       text        NOT NULL DEFAULT 'open',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_new_leads_task_idx
    ON lifetime_new_leads (task_id, status);

-- ---------------------------------------------------------------------------
-- §6.5 work queue
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_work_queue (
    item_id                text        PRIMARY KEY,
    task_id                text        NOT NULL,
    queue_kind             text        NOT NULL,
    item_ref               text        NOT NULL,
    ready_predicate_version text       NOT NULL DEFAULT '',
    state                  text        NOT NULL DEFAULT 'ready',
    priority               double precision NOT NULL DEFAULT 0,
    claimed_by_session     text        NOT NULL DEFAULT '',
    claim_id               text        NOT NULL DEFAULT '',
    lease_expires_at       timestamptz,
    attempts               integer     NOT NULL DEFAULT 0,
    dedup_key              text        NOT NULL,
    reason                 text        NOT NULL DEFAULT '',
    lineage_hash           text        NOT NULL DEFAULT '',
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_work_queue_ready_idx
    ON lifetime_work_queue (task_id, queue_kind, state, priority DESC, created_at);

-- 同一 (task, queue, dedup_key) 只能有一个活跃项；cancelled 后允许重新投影。
CREATE UNIQUE INDEX IF NOT EXISTS lifetime_work_queue_dedup_idx
    ON lifetime_work_queue (task_id, queue_kind, dedup_key)
    WHERE state <> 'cancelled';

-- ---------------------------------------------------------------------------
-- §6.12 claim log
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_claim_log (
    seq            bigserial   PRIMARY KEY,
    task_id        text        NOT NULL,
    claim_id       text        NOT NULL DEFAULT '',
    item_id        text        NOT NULL DEFAULT '',
    session_id     text        NOT NULL DEFAULT '',
    action         text        NOT NULL,
    priority       double precision NOT NULL DEFAULT 0,
    scheduler_seed bigint      NOT NULL DEFAULT 0,
    -- release reason / complete outcome_ref 是平台审计扩展列。
    reason         text        NOT NULL DEFAULT '',
    outcome_ref    text        NOT NULL DEFAULT '',
    timestamp      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_claim_log_task_idx
    ON lifetime_claim_log (task_id, seq DESC);

-- ---------------------------------------------------------------------------
-- §6.11 area lease / owner journal
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_area_leases (
    area_id       text        PRIMARY KEY,
    owner_session text        NOT NULL,
    leased_at     timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS lifetime_owner_journal (
    entry_id    text        PRIMARY KEY,
    task_id     text        NOT NULL,
    area_id     text        NOT NULL,
    owner_id    text        NOT NULL DEFAULT '',
    kind        text        NOT NULL,
    text        text        NOT NULL,
    refs        jsonb       NOT NULL DEFAULT '[]'::jsonb,
    confidence  text        NOT NULL DEFAULT 'unknown',
    source_hash text        NOT NULL DEFAULT '',
    index_hash  text        NOT NULL DEFAULT '',
    stale       boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_owner_journal_area_idx
    ON lifetime_owner_journal (area_id, created_at);
CREATE INDEX IF NOT EXISTS lifetime_owner_journal_task_idx
    ON lifetime_owner_journal (task_id, created_at);

-- ---------------------------------------------------------------------------
-- §6.10 reconciliation
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_reconciliation_findings (
    recon_id     text        PRIMARY KEY,
    task_id      text        NOT NULL,
    sweep_id     text        NOT NULL DEFAULT '',
    check_id     text        NOT NULL,
    severity     text        NOT NULL,
    subject_kind text        NOT NULL DEFAULT '',
    subject_ref  text        NOT NULL DEFAULT '',
    expected     text        NOT NULL DEFAULT '',
    actual       text        NOT NULL DEFAULT '',
    action       text        NOT NULL,
    status       text        NOT NULL DEFAULT 'open',
    dedup_key    text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    resolved_at  timestamptz
);

-- R-6.29：同一问题不重复生成 repair task；空 dedup_key 不参与唯一约束。
CREATE UNIQUE INDEX IF NOT EXISTS lifetime_reconciliation_findings_open_dedup_idx
    ON lifetime_reconciliation_findings (task_id, dedup_key)
    WHERE status = 'open' AND dedup_key <> '';

CREATE INDEX IF NOT EXISTS lifetime_reconciliation_findings_task_idx
    ON lifetime_reconciliation_findings (task_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS lifetime_reconciliation_runs (
    sweep_id    text        PRIMARY KEY,
    task_id     text        NOT NULL,
    mode        text        NOT NULL DEFAULT '',
    checks      integer     NOT NULL DEFAULT 0,
    findings    integer     NOT NULL DEFAULT 0,
    repairs     integer     NOT NULL DEFAULT 0,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);

CREATE INDEX IF NOT EXISTS lifetime_reconciliation_runs_task_idx
    ON lifetime_reconciliation_runs (task_id, started_at DESC);

-- ---------------------------------------------------------------------------
-- §16.2 request_probe 投影
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_probes (
    probe_request_id text        PRIMARY KEY,
    task_id          text        NOT NULL,
    candidate_id     text        NOT NULL,
    question         text        NOT NULL,
    expected_signal  text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lifetime_probes_candidate_idx
    ON lifetime_probes (task_id, candidate_id, created_at);

-- ---------------------------------------------------------------------------
-- §8.1 索引运行元数据（index_quality / type_resolution 是 S1 门禁输入）
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS lifetime_index_runs (
    task_id          text        NOT NULL,
    lineage_id       text        NOT NULL,
    index_hash       text        NOT NULL,
    index_quality    text        NOT NULL DEFAULT 'heuristic',
    type_resolution  text        NOT NULL DEFAULT 'unknown',
    created_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, lineage_id)
);
