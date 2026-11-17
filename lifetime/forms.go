package lifetime

// 本文件实现形态矩阵相关的确定性谓词：
//   - §12.6 form_min_fidelity / eligible 三元谓词
//   - §12.4 visibility 要求与 form_facts_complete
//   - §4.4 applicable_forms 推断
//   - §12.3 四问（q1–q4）校验
//   - §12.2 形态的决定性契约字段
//
// 共享契约（enums.go / types.go / reconcile_types.go）不可修改；本文件不新增
// 共享实体类型，只新增本文件私有的辅助函数与常量。

import (
	"fmt"
	"strings"
)

// q4 的 status 取值（§12.3）。
const (
	ltQ4StatusKnown   = "known"
	ltQ4StatusUnknown = "unknown"
	ltQ4StatusNone    = "none"
)

// q4.status=unknown 时允许的结构化 reason（§12.3）。
const (
	ltQ4ReasonNotDocumented     = "not_documented"
	ltQ4ReasonDynamicDispatch   = "dynamic_dispatch"
	ltQ4ReasonThirdParty        = "third_party"
	ltQ4ReasonInsufficientIndex = "insufficient_index"
	ltQ4ReasonOutOfScope        = "out_of_scope"
)

// ltFormOrder 是形态矩阵的稳定顺序（F1..F8），用于去重后的确定性输出。
var ltFormOrder = []Form{
	FormRefcountRace,
	FormDanglingThis,
	FormRegisterLeak,
	FormContainerInvalid,
	FormStaticDestroy,
	FormSharedPtrRace,
	FormLazyInitRace,
	FormPoolABA,
}

// FormMinFidelity 返回 §12.6 表规定的最低证据保真度。
//
// F5 的表格项是 “component（process_lifecycle=true）”：process_lifecycle 是
// build manifest 的事实（R-13.6），不在本函数签名内，必须由平台在证据绑定时
// 另行校验；本函数只返回 component。
//
// F8 的表格项是 “unit（真实池建议 component）”：文档只把 component 作为建议，
// 因此最低要求仍是 unit。
//
// 非法形态返回空 Fidelity（Rank()==0）；调用方应先用 Form.Valid() 校验。
func FormMinFidelity(f Form) Fidelity {
	switch f {
	case FormRefcountRace, FormRegisterLeak, FormSharedPtrRace, FormLazyInitRace, FormPoolABA:
		return FidelityUnit
	case FormDanglingThis, FormContainerInvalid, FormStaticDestroy:
		return FidelityComponent
	}
	return Fidelity("")
}

// ltEligibleMemory 报告 (event, tool) 是否为 ASan/Valgrind 内存事件。
// MSan 只对未初始化读合格，不能作为 UAF/double-free 证据（R-12.16），故排除。
func ltEligibleMemory(eventType EventType, tool EvidenceTool) bool {
	return eventType == EventMemory && (tool == ToolASan || tool == ToolValgrind)
}

// ltEligibleInvariant 报告 (event, tool) 是否为预注册 assertion 不变量事件。
func ltEligibleInvariant(eventType EventType, tool EvidenceTool) bool {
	return eventType == EventInvariant && tool == ToolAssertion
}

// Eligible 是 §12.6 的三元证据资格谓词 eligible(form, event_type, tool)。
//
// 严格按表：
//
//	F1: invariant(assertion), memory(asan|valgrind)
//	F2: memory(asan|valgrind), invariant(assertion)
//	F3: invariant(assertion), memory(asan|valgrind)
//	F4: memory(asan|valgrind), invariant(assertion)
//	F5: memory(asan|valgrind), hang(watchdog), invariant(assertion)
//	F6: invariant(assertion), memory(asan|valgrind)
//	F7: invariant(assertion)（S3）；race_lead(tsan)（仅 S2）
//	F8: invariant(assertion)（generation）；memory 仅在 pool_frees_slot=true 时合格
//
// 关于 F7/race_lead：本函数按 §12.6 返回 true，但 race_lead 不是 S3 证据
// （§3.6 明确排除 event_type=race_lead，R-3.9/R-3.15）；调用方在 S3/S4 评估中
// 仍必须排除，只有 S2/新线索可以使用。
//
// 关于 F8/memory：本函数没有 pool_frees_slot 参数。按 R-12.17，ASan 不报
// “池中复用后仍有效”的 ABA，F8 默认只收 invariant/generation，因此这里对
// F8+memory 一律返回 false；只有平台在 pool_frees_slot=true 的场景中另行
// 放宽时才可接受该观察。
//
// MSan 不能作为 UAF/double-free 证据（R-12.16），对任何形态的 memory 事件
// 都不合格。
func Eligible(form Form, eventType EventType, tool EvidenceTool) bool {
	switch form {
	case FormRefcountRace, FormRegisterLeak, FormSharedPtrRace:
		return ltEligibleInvariant(eventType, tool) || ltEligibleMemory(eventType, tool)
	case FormDanglingThis, FormContainerInvalid:
		return ltEligibleMemory(eventType, tool) || ltEligibleInvariant(eventType, tool)
	case FormStaticDestroy:
		return ltEligibleMemory(eventType, tool) ||
			(eventType == EventHang && tool == ToolWatchdog) ||
			ltEligibleInvariant(eventType, tool)
	case FormLazyInitRace:
		return ltEligibleInvariant(eventType, tool) ||
			(eventType == EventRaceLead && tool == ToolTSan)
	case FormPoolABA:
		// memory 仅在 pool_frees_slot=true 时合格（R-12.17）；本函数无该参数，
		// 返回 false 表示“默认不合格”，由平台在 F8 专用路径上放宽。
		return ltEligibleInvariant(eventType, tool)
	}
	return false
}

// FormVisibilityRequired 报告形态是否要求 q4 可见性必须已知（§12.4）。
// visibility_required 的表项：F2、F3、F5；其余为 optional。
// 非法形态按 optional 处理（返回 false）；调用方应先用 Form.Valid() 校验。
//
// 注意：共享契约 enums.go 已把 VisibilityRequired 定义为 Visibility 常量
// （值 "required"），Go 不允许同名包级函数。因此本谓词命名为
// FormVisibilityRequired；语义与规格中的 visibility_required(form) 相同。
func FormVisibilityRequired(f Form) bool {
	switch f {
	case FormDanglingThis, FormRegisterLeak, FormStaticDestroy:
		return true
	}
	return false
}

// SuggestedApplicableForms 按 §4.4 表从 family 特征推断适用形态。
//
// 能从 ObjectFamily 直接判定的规则：
//   - storage_class=static 或 lifetime_form=singleton → F5、F7
//   - lifetime_form=shared/weak → F6
//   - lifetime_form=refcount/intrusive → F1
//   - storage_class=pool 或 storage_class=handle → F8
//
// §4.4 中依赖“关系/容器”特征的规则（register/unregister 关系 → F3、
// 容器 + mutation/traversal → F4、异步 task/callback holder → F2、惰性初始化
// 站点 → F7）无法仅从 ObjectFamily 判定：ObjectFamily 不携带关系、容器或
// 站点信息。调用方应把 ObjectRelations / 索引站点信息与该结果合并。
//
// 若 family 自带 applicable_forms[]（索引器已产出，R-4.11），则一并返回；
// confidence 优先采用 family.confidence，缺失时按 medium（结构性推断）返回。
// 无任何判定时返回空切片 + ConfidenceUnknown（R-4.12：不得静默移出分母，
// 由调用方计入 form_scope=unknown）。
func SuggestedApplicableForms(fam ObjectFamily) ([]Form, ConfidenceLevel) {
	set := make(map[Form]struct{}, len(ltFormOrder))
	for _, f := range fam.ApplicableForms {
		if f.Valid() {
			set[f] = struct{}{}
		}
	}
	explicit := len(set) > 0

	if fam.StorageClass == StorageStatic || fam.LifetimeForm == LifetimeSingleton {
		set[FormStaticDestroy] = struct{}{}
		set[FormLazyInitRace] = struct{}{}
	}
	if fam.LifetimeForm == LifetimeShared || fam.LifetimeForm == LifetimeWeak {
		set[FormSharedPtrRace] = struct{}{}
	}
	if fam.LifetimeForm == LifetimeRefcount || fam.LifetimeForm == LifetimeIntrusive {
		set[FormRefcountRace] = struct{}{}
	}
	if fam.StorageClass == StoragePool || fam.StorageClass == StorageHandle {
		set[FormPoolABA] = struct{}{}
	}

	if len(set) == 0 {
		return nil, ConfidenceUnknown
	}

	out := make([]Form, 0, len(set))
	for _, f := range ltFormOrder {
		if _, ok := set[f]; ok {
			out = append(out, f)
		}
	}

	// 索引器显式给出的 confidence 优先；否则这是从 storage_class/
	// lifetime_form 直接推出的结构性结论，标 medium。
	if explicit && fam.Confidence.Valid() && fam.Confidence != ConfidenceUnknown {
		return out, fam.Confidence
	}
	return out, ConfidenceMedium
}

// ltFormsContain 报告 forms 中是否包含 form。
func ltFormsContain(form Form, forms []Form) bool {
	for _, f := range forms {
		if f == form {
			return true
		}
	}
	return false
}

// ltHasHolder 报告候选是否至少有一个带站点的持有者。
func ltHasHolder(pkg *CandidatePackage) bool {
	for _, h := range pkg.Holders {
		if strings.TrimSpace(h.Site) != "" {
			return true
		}
	}
	return false
}

// ltHasSync 报告是否存在完整同步原语（protects 必填，R-8.12）。
func ltHasSync(pkg *CandidatePackage) bool {
	for _, s := range pkg.Synchronization {
		if strings.TrimSpace(s.Primitive) != "" && strings.TrimSpace(s.Protects) != "" {
			return true
		}
	}
	return false
}

// ltHasSyncFileLine 报告是否至少有一个同步原语带 file:line。
func ltHasSyncFileLine(pkg *CandidatePackage) bool {
	for _, s := range pkg.Synchronization {
		if strings.TrimSpace(s.FileLine) != "" {
			return true
		}
	}
	return false
}

// ltChainHas 报告 evidence_chain 中是否存在带 file:line 的指定角色节点。
func ltChainHas(pkg *CandidatePackage, role NodeRole) bool {
	for _, n := range pkg.EvidenceChain {
		if n.Role == role && strings.TrimSpace(n.File) != "" && n.Line > 0 {
			return true
		}
	}
	return false
}

// ltHasGap 报告生命周期缺口描述是否非空。
func ltHasGap(pkg *CandidatePackage) bool {
	return strings.TrimSpace(pkg.Gap.Description) != ""
}

// ltHasGenerationFact 报告候选层面是否有对象池 generation 事实的结构性证据。
//
// 共享契约中没有独立的 generation 字段；本实现使用三个候选级代理：
//   - ownership_answers.q4.value == "generation"
//   - lifetime_visibility.q4_answer.value == "generation"
//   - 某个同步原语的 primitive/protects 文本包含 "generation"
//
// 运行时的 (instance_id, generation) 绑定仍在 S3 中由
// obs.runtime_context.generation 校验（R-4.3/R-9.23）。
func ltHasGenerationFact(pkg *CandidatePackage) bool {
	if pkg.OwnershipAnswers.Q4.Value == "generation" ||
		pkg.LifetimeVisibility.Q4Answer.Value == "generation" {
		return true
	}
	for _, s := range pkg.Synchronization {
		text := strings.ToLower(s.Primitive + " " + s.Protects + " " + s.Scope)
		if strings.Contains(text, "generation") {
			return true
		}
	}
	return false
}

// FormFactsComplete 检查 §12.4 表中该形态的“必需事实”（除 q1–q4 外）是否
// 在 CandidatePackage 中有结构性填充。q1–q4 由 ValidateFourQuestions 单独校验。
//
// 共享契约没有为 §12.4 的每个语义事实提供独立字段，因此本实现只能用候选包
// 的结构字段做代理检查。每形态检查项如下（缺失项按顺序进入返回列表）：
//
//	F1 refcount 操作/内存序/holder set/last-dec 站点：
//	    holders、synchronization（primitive+protects）、sync_file_line
//	F2 async holder/callback 注册/cancel 路径/destruction 路径：
//	    holders、domains、holder_notification、evidence_chain:lifetime_point、gap
//	F3 register/unregister 站点/notification 路径/destruction 路径：
//	    holder_notification、evidence_chain:notification、
//	    evidence_chain:lifetime_point、gap
//	F4 container/mutation/traversal/guard：
//	    domains、competing_accesses、evidence_chain:sync_guard、evidence_chain:access
//	F5 statics[]/destruct order/worker thread/shutdown 路径：
//	    storage_static_or_singleton、domains、evidence_chain:trigger、gap
//	F6 同一 shared_ptr 对象站点/copy-reset 站点/control block：
//	    lifetime_form_shared_or_weak、synchronization、evidence_chain:access
//	F7 init 站点/publication/first-use/call_once-guard：
//	    domains、synchronization、evidence_chain:lifetime_point、evidence_chain:access
//	F8 pool allocator/free-reuse 站点/generation 字段：
//	    storage_pool_or_handle、evidence_chain:lifetime_point、
//	    evidence_chain:access、generation（见 ltHasGenerationFact 的代理说明）
//
// chain 节点要求角色匹配且带 file:line；同步原语要求 protects 非空（R-8.12）。
// 返回 true 时缺失列表为空。
func FormFactsComplete(pkg *CandidatePackage) (bool, []string) {
	if pkg == nil {
		return false, []string{"candidate_package"}
	}
	if !pkg.PrimaryForm.Valid() {
		return false, []string{"primary_form"}
	}

	missing := make([]string, 0, 4)
	add := func(ok bool, name string) {
		if !ok {
			missing = append(missing, name)
		}
	}

	switch pkg.PrimaryForm {
	case FormRefcountRace:
		add(ltHasHolder(pkg), "holders")
		add(ltHasSync(pkg), "synchronization")
		add(ltHasSyncFileLine(pkg), "sync_file_line")
	case FormDanglingThis:
		add(ltHasHolder(pkg), "holders")
		add(len(pkg.Domains) > 0, "domains")
		add(strings.TrimSpace(pkg.LifetimeVisibility.HolderNotification) != "", "holder_notification")
		add(ltChainHas(pkg, NodeLifetimePoint), "evidence_chain:lifetime_point")
		add(ltHasGap(pkg), "gap")
	case FormRegisterLeak:
		add(strings.TrimSpace(pkg.LifetimeVisibility.HolderNotification) != "", "holder_notification")
		add(ltChainHas(pkg, NodeNotification), "evidence_chain:notification")
		add(ltChainHas(pkg, NodeLifetimePoint), "evidence_chain:lifetime_point")
		add(ltHasGap(pkg), "gap")
	case FormContainerInvalid:
		add(len(pkg.Domains) > 0, "domains")
		add(len(pkg.CompetingAccesses) > 0, "competing_accesses")
		add(ltChainHas(pkg, NodeSyncGuard), "evidence_chain:sync_guard")
		add(ltChainHas(pkg, NodeAccess), "evidence_chain:access")
	case FormStaticDestroy:
		add(pkg.Family.StorageClass == StorageStatic || pkg.Family.LifetimeForm == LifetimeSingleton,
			"storage_static_or_singleton")
		add(len(pkg.Domains) > 0, "domains")
		add(ltChainHas(pkg, NodeTrigger), "evidence_chain:trigger")
		add(ltHasGap(pkg), "gap")
	case FormSharedPtrRace:
		add(pkg.Family.LifetimeForm == LifetimeShared || pkg.Family.LifetimeForm == LifetimeWeak,
			"lifetime_form_shared_or_weak")
		add(ltHasSync(pkg), "synchronization")
		add(ltChainHas(pkg, NodeAccess), "evidence_chain:access")
	case FormLazyInitRace:
		add(len(pkg.Domains) > 0, "domains")
		add(ltHasSync(pkg), "synchronization")
		add(ltChainHas(pkg, NodeLifetimePoint), "evidence_chain:lifetime_point")
		add(ltChainHas(pkg, NodeAccess), "evidence_chain:access")
	case FormPoolABA:
		add(pkg.Family.StorageClass == StoragePool || pkg.Family.StorageClass == StorageHandle,
			"storage_pool_or_handle")
		add(ltChainHas(pkg, NodeLifetimePoint), "evidence_chain:lifetime_point")
		add(ltChainHas(pkg, NodeAccess), "evidence_chain:access")
		add(ltHasGenerationFact(pkg), "generation")
	}

	return len(missing) == 0, missing
}

// ltQ4ReasonValid 报告 q4.reason 是否属于 §12.3 的结构化枚举。
func ltQ4ReasonValid(reason string) bool {
	switch reason {
	case ltQ4ReasonNotDocumented, ltQ4ReasonDynamicDispatch, ltQ4ReasonThirdParty,
		ltQ4ReasonInsufficientIndex, ltQ4ReasonOutOfScope:
		return true
	}
	return false
}

// ValidateFourQuestions 校验 §12.3 的四问与 §12.4 的 visibility 规则。
//
//   - q1/q2/q3：value + file + line 必填；q2 还要求 form + domain。
//   - q4.status ∈ {known, unknown, none}。
//   - status=unknown：reason 必须在结构化枚举
//     {not_documented, dynamic_dispatch, third_party, insufficient_index, out_of_scope}
//     内。
//   - visibility_required 形态（F2/F3/F5）q4 unknown → 错误（R-12.9）。
//   - visibility_optional 形态允许 unknown，但必须
//     lifetime_visibility.visibility_unknown=true（R-12.10）。
//
// 注意：q4.status=known|none 时不校验 primary_form 的有效性；只有 unknown
// 分支需要形态来判定 visibility 规则。
func ValidateFourQuestions(pkg *CandidatePackage) error {
	if pkg == nil {
		return fmt.Errorf("candidate package is nil")
	}
	qa := pkg.OwnershipAnswers

	if strings.TrimSpace(qa.Q1CreatedBy.Value) == "" ||
		strings.TrimSpace(qa.Q1CreatedBy.File) == "" || qa.Q1CreatedBy.Line <= 0 {
		return fmt.Errorf("ownership_answers.q1_created_by: value, file and line are required")
	}
	if strings.TrimSpace(qa.Q2HeldBy.Value) == "" ||
		strings.TrimSpace(qa.Q2HeldBy.File) == "" || qa.Q2HeldBy.Line <= 0 {
		return fmt.Errorf("ownership_answers.q2_held_by: value, file and line are required")
	}
	if strings.TrimSpace(qa.Q2HeldBy.Form) == "" {
		return fmt.Errorf("ownership_answers.q2_held_by: form is required")
	}
	if strings.TrimSpace(qa.Q2HeldBy.Domain) == "" {
		return fmt.Errorf("ownership_answers.q2_held_by: domain is required")
	}
	if strings.TrimSpace(qa.Q3DestroyedBy.Value) == "" ||
		strings.TrimSpace(qa.Q3DestroyedBy.File) == "" || qa.Q3DestroyedBy.Line <= 0 {
		return fmt.Errorf("ownership_answers.q3_destroyed_by: value, file and line are required")
	}

	switch qa.Q4.Status {
	case ltQ4StatusKnown, ltQ4StatusNone:
		return nil
	case ltQ4StatusUnknown:
		// 继续校验 unknown 的可见性规则。
	default:
		return fmt.Errorf("ownership_answers.q4.status: invalid %q (want known|unknown|none)", qa.Q4.Status)
	}

	if !ltQ4ReasonValid(qa.Q4.Reason) {
		return fmt.Errorf("ownership_answers.q4.reason: invalid %q for status=unknown", qa.Q4.Reason)
	}
	if !pkg.PrimaryForm.Valid() {
		return fmt.Errorf("primary_form: invalid %q for q4 visibility rules", string(pkg.PrimaryForm))
	}
	if FormVisibilityRequired(pkg.PrimaryForm) {
		return fmt.Errorf("ownership_answers.q4: unknown not allowed for visibility-required form %s (R-12.9)",
			string(pkg.PrimaryForm))
	}
	if !pkg.LifetimeVisibility.VisibilityUnknown {
		return fmt.Errorf("ownership_answers.q4: visibility_optional form %s with unknown status requires lifetime_visibility.visibility_unknown=true (R-12.10)",
			string(pkg.PrimaryForm))
	}
	return nil
}

// DecisiveContractFields 返回 §12.2 表规定的决定性契约字段。
// 字段名与 §12.1 APIContract/LifecycleContract 的 schema 键一致：
// thread_safety / caller_locking 在 api_contract 顶层；ownership、
// destroy_while_callback、callback_cancellation、thread_affinity、
// close_semantics、generation_guarantee 在 lifecycle 内。
// 非法形态返回空切片。
func DecisiveContractFields(f Form) []string {
	switch f {
	case FormRefcountRace:
		return []string{"thread_safety", "caller_locking", "ownership"}
	case FormDanglingThis:
		return []string{"destroy_while_callback", "callback_cancellation", "thread_affinity"}
	case FormRegisterLeak:
		return []string{"unregister_obligation", "callback_cancellation", "ownership"}
	case FormContainerInvalid:
		return []string{"thread_safety", "caller_locking"}
	case FormStaticDestroy:
		return []string{"close_semantics", "thread_affinity", "ownership"}
	case FormSharedPtrRace:
		return []string{"thread_safety"}
	case FormLazyInitRace:
		return []string{"thread_safety", "ownership"}
	case FormPoolABA:
		return []string{"close_semantics", "ownership", "generation_guarantee"}
	}
	return nil
}
