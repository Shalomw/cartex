package lifetime

// 本文件实现 §2.2 的确定性 ID/哈希规则与 R-3.5/R-6.25/R-6.43 所需的
// 内容寻址哈希。所有函数必须满足：同内容同值、跨平台同值、不依赖 map
// 迭代顺序或进程内状态。
//
// 约定：
//   - 对外 ID（family_id / domain_class_id / domain_pair_id / ...）为
//     sha256 的 hex 字符串，输入严格按 §2.2 的公式列出的字段与顺序；
//   - 集合类哈希（lifetime_point / access_set / holder_set / evidence_set）
//     先排序再去重，调用方传入顺序不影响结果；
//   - 唯一例外是 unresolved domain_pair_id：R-2.1 规定其字面格式为
//     "unresolved:<family_id>:<access_set_hash>"。

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync/atomic"
	"time"
)

// CanonicalHash 对 parts 做无歧义拼接后取 sha256 hex。
// 每个 part 以 "<len>:" 长度前缀写入，因此 ("ab","c") 与 ("a","bc")
// 不会碰撞，且结果与平台和 locale 无关。
func CanonicalHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(strconv.Itoa(len(p))))
		h.Write([]byte{':'})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// idsSHA256Hex 返回原始字节的 sha256 hex（用于 PolicyHash 对 canonical
// JSON 字节做内容寻址）。
func idsSHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// idFallbackCounter 只在 crypto/rand 不可用时参与 NewID 的降级路径。
var idFallbackCounter uint64

// NewID 生成 16 字节 crypto/rand 随机数的 hex，格式为 "prefix_<hex>"。
// crypto/rand 失败时降级为 time+counter 的 hash（仍满足唯一性的工程需要，
// 但不再具备密码学随机性）；正常平台上不会走到该分支。
func NewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(b[:])
	}
	n := atomic.AddUint64(&idFallbackCounter, 1)
	h := CanonicalHash("new_id_fallback", strconv.FormatInt(time.Now().UnixNano(), 10), strconv.FormatUint(n, 10))
	return prefix + "_" + h[:32]
}

// ---------------------------------------------------------------------------
// §2.2 family_id
// ---------------------------------------------------------------------------

// familyMissingMemberPath 是 storage_class=member 但 member_path 缺失时
// 参与 FamilyID 的哨兵值。它保证非法 key 仍得到确定性且与任何合法 key
// 不同的哈希（"不静默"），同时 ValidateFamilyKey 会显式拒绝该 key。
// 使用 NUL 包裹，避免与真实成员路径字面量相撞。
const familyMissingMemberPath = "\x00missing_member_path\x00"

// FamilyKey 是 family_id 的规范输入（§2.2/R-2.2）。
//
// AllocSite 的语义由调用方按 storage_class 保证（R-4.1）：
//   - heap/stack/static：分配点/声明点（file:line + function）；
//   - member：外层 alloc_site，MemberPath 另由 FamilyID 参与；
//   - pool：池分配器站点；
//   - handle：handle 表站点。
//
// R-2.2 要求的“池分配器站点/句柄表站点 + 类型”中的“类型”恒由
// TypeCanonical 参与，因此 AllocSite 只需填站点本身。
type FamilyKey struct {
	TypeCanonical string
	AllocSite     string
	StorageClass  StorageClass
	LifetimeForm  LifetimeForm
	MemberPath    string
}

// ValidateFamilyKey 校验 family_id 输入是否满足 R-2.2。
func ValidateFamilyKey(k FamilyKey) error {
	if k.TypeCanonical == "" {
		return fmt.Errorf("family_key.type_canonical: empty")
	}
	if k.AllocSite == "" {
		return fmt.Errorf("family_key.alloc_site: empty")
	}
	if !k.StorageClass.Valid() {
		return fmt.Errorf("family_key.storage_class: invalid %q", string(k.StorageClass))
	}
	if !k.LifetimeForm.Valid() {
		return fmt.Errorf("family_key.lifetime_form: invalid %q", string(k.LifetimeForm))
	}
	if k.StorageClass == StorageMember && k.MemberPath == "" {
		return fmt.Errorf("family_key.member_path: required when storage_class=%q (R-2.2)", string(StorageMember))
	}
	return nil
}

// FamilyID 按 §2.2/R-2.2 生成 family_id：
// hash(type_canonical, alloc_site, storage_class, lifetime_form, member_path?)。
// 只有 storage_class=member 时 member_path 参与；缺失时用哨兵值，仍返回
// 确定性哈希，但该 key 会被 ValidateFamilyKey 拒绝。
func FamilyID(k FamilyKey) string {
	parts := []string{
		k.TypeCanonical,
		k.AllocSite,
		string(k.StorageClass),
		string(k.LifetimeForm),
	}
	if k.StorageClass == StorageMember {
		mp := k.MemberPath
		if mp == "" {
			mp = familyMissingMemberPath
		}
		parts = append(parts, mp)
	}
	return CanonicalHash(parts...)
}

// ---------------------------------------------------------------------------
// §2.2 domain / pair
// ---------------------------------------------------------------------------

// DomainClassID = hash(kind, instance_site, parent_pool)。
func DomainClassID(kind DomainKind, instanceSite, parentPool string) string {
	return CanonicalHash(string(kind), instanceSite, parentPool)
}

// DomainInstanceID = hash(domain_class_id, runtime_ordinal)。
func DomainInstanceID(classID string, ordinal uint64) string {
	return CanonicalHash(classID, strconv.FormatUint(ordinal, 10))
}

// idsOrderedPair 返回无序对排序后的两个元素。
func idsOrderedPair(a, b string) (string, string) {
	if a <= b {
		return a, b
	}
	return b, a
}

// DomainPairID 生成无序域对 ID；单域形态 (d,d) 合法。
func DomainPairID(a, b string) string {
	x, y := idsOrderedPair(a, b)
	return CanonicalHash(x, y)
}

// PairClassID 生成无序域类对 ID。
func PairClassID(a, b string) string {
	x, y := idsOrderedPair(a, b)
	return CanonicalHash(x, y)
}

// PairInstanceID 生成无序域实例对 ID。
func PairInstanceID(a, b string) string {
	x, y := idsOrderedPair(a, b)
	return CanonicalHash(x, y)
}

// ---------------------------------------------------------------------------
// §2.2 集合哈希
// ---------------------------------------------------------------------------

// idsSortedUnique 返回排序去重后的副本；空输入返回 nil。
func idsSortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	cp := make([]string, len(in))
	copy(cp, in)
	sort.Strings(cp)
	n := 0
	for i, s := range cp {
		if i == 0 || s != cp[i-1] {
			cp[n] = s
			n++
		}
	}
	return cp[:n]
}

// idsSortedStrings 返回排序后的副本（保留重复）。
func idsSortedStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	cp := make([]string, len(in))
	copy(cp, in)
	sort.Strings(cp)
	return cp
}

// idsEvidenceSite 把 EvidenceNode 的 file:line 渲染为稳定的 site 字符串。
func idsEvidenceSite(n EvidenceNode) string {
	return n.File + ":" + strconv.Itoa(n.Line)
}

// LifetimePointHash = hash(sorted(lifetime_point.site + role))。
// EvidenceNode 没有显式 site 字段，site 取 file:line。
func LifetimePointHash(points []EvidenceNode) string {
	keys := make([]string, 0, len(points))
	for _, p := range points {
		keys = append(keys, idsEvidenceSite(p)+"\x1f"+string(p.Role))
	}
	keys = idsSortedUnique(keys)
	return CanonicalHash(keys...)
}

// AccessSetHash = hash(sorted(access.site + role + guarded_by))。
// Access 没有 role 字段，按 §2.2 以 guarded_by 作为 role 位置的参与者。
func AccessSetHash(accesses []Access) string {
	keys := make([]string, 0, len(accesses))
	for _, a := range accesses {
		keys = append(keys, a.Site+"\x1f"+a.GuardedBy)
	}
	keys = idsSortedUnique(keys)
	return CanonicalHash(keys...)
}

// HolderSetHash = hash(sorted(holder.site + holder.form))。
func HolderSetHash(holders []Holder) string {
	keys := make([]string, 0, len(holders))
	for _, h := range holders {
		keys = append(keys, h.Site+"\x1f"+h.Form)
	}
	keys = idsSortedUnique(keys)
	return CanonicalHash(keys...)
}

// ---------------------------------------------------------------------------
// §2.2 candidate_key / mechanism_key / unresolved pair
// ---------------------------------------------------------------------------

// CandidateKey = hash(family_id, domain_pair_id, primary_form,
// lifetime_point_hash, access_set_hash, holder_set_hash)。
func CandidateKey(familyID, domainPairID string, primary Form, lifetimePointHash, accessSetHash, holderSetHash string) string {
	return CanonicalHash(familyID, domainPairID, string(primary),
		lifetimePointHash, accessSetHash, holderSetHash)
}

// MechanismKey = hash(family_set, lifetime_point_hash, access_set_hash,
// root_cause_kind)。family_set 排序去重后作为整体参与哈希（先取集合哈希，
// 以免可变长度的 family 列表与后续字段产生边界歧义）。
func MechanismKey(familySet []string, lifetimePointHash, accessSetHash, rootCauseKind string) string {
	sorted := idsSortedUnique(familySet)
	familyPart := CanonicalHash(sorted...)
	return CanonicalHash(familyPart, lifetimePointHash, accessSetHash, rootCauseKind)
}

// UnresolvedDomainPairID 返回 R-2.1 规定的字面格式：
// "unresolved:<family_id>:<access_set_hash>"。
func UnresolvedDomainPairID(familyID, accessSetHash string) string {
	return "unresolved:" + familyID + ":" + accessSetHash
}

// EvidenceSetHash 对证据 ID 排序去重后哈希（R-10.12/R-10.14 的
// evidence_set_hash 输入；实际绑定规则由复核层派生）。
func EvidenceSetHash(ids []string) string {
	sorted := idsSortedUnique(ids)
	return CanonicalHash(sorted...)
}

// SortKeyHash 用于优先级并列打破（R-6.43）：hash(item_id, scheduler_seed)。
func SortKeyHash(itemID string, seed int64) string {
	return CanonicalHash(itemID, strconv.FormatInt(seed, 10))
}

// ---------------------------------------------------------------------------
// TaskProfile 内容哈希（R-3.5/R-6.25/R-14.13）
// ---------------------------------------------------------------------------

// idsSortedForms 返回排序后的 Form 副本。
func idsSortedForms(in []Form) []Form {
	if len(in) == 0 {
		return nil
	}
	cp := make([]Form, len(in))
	copy(cp, in)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp
}

// idsSortedStorageClasses 返回排序后的 StorageClass 副本。
func idsSortedStorageClasses(in []StorageClass) []StorageClass {
	if len(in) == 0 {
		return nil
	}
	cp := make([]StorageClass, len(in))
	copy(cp, in)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp
}

// idsCanonicalProfile 返回用于 canonical JSON 的规范化副本：
//   - 清空 PolicyHash 字段本身；
//   - 所有语义为集合的切片排序（Forms / limited_control_forms /
//     instrumentation_requirements / enumeration_scope 列表 /
//     form_facts 值 / evidence_eligibility 的 eligible 事件）；
//   - 空 map/切片归一为 nil，避免 nil 与空集合产生不同哈希。
//
// map[Form]/map[Role]/map[QueueKind] 的键排序由 encoding/json 保证。
func idsCanonicalProfile(p *TaskProfile) *TaskProfile {
	cp := *p
	cp.PolicyHash = ""

	cp.Forms = idsSortedForms(cp.Forms)
	cp.LimitedControlForms = idsSortedForms(cp.LimitedControlForms)
	cp.InstrumentationRequirements = idsSortedStrings(cp.InstrumentationRequirements)

	cp.EnumerationScope.EntryPoints = idsSortedStrings(cp.EnumerationScope.EntryPoints)
	cp.EnumerationScope.StorageClasses = idsSortedStorageClasses(cp.EnumerationScope.StorageClasses)
	cp.EnumerationScope.ModuleRoots = idsSortedStrings(cp.EnumerationScope.ModuleRoots)

	if len(cp.FormFacts) == 0 {
		cp.FormFacts = nil
	} else {
		facts := make(map[Form][]string, len(cp.FormFacts))
		for f, vs := range cp.FormFacts {
			facts[f] = idsSortedStrings(vs)
		}
		cp.FormFacts = facts
	}

	if len(cp.EvidenceEligibility.Extra) == 0 {
		cp.EvidenceEligibility.Extra = nil
	} else {
		extra := make(map[Form][]EligibleEvent, len(cp.EvidenceEligibility.Extra))
		for f, evs := range cp.EvidenceEligibility.Extra {
			evc := append([]EligibleEvent(nil), evs...)
			sort.Slice(evc, func(i, j int) bool {
				if evc[i].EventType != evc[j].EventType {
					return evc[i].EventType < evc[j].EventType
				}
				return evc[i].Tool < evc[j].Tool
			})
			if len(evc) == 0 {
				evc = nil
			}
			extra[f] = evc
		}
		cp.EvidenceEligibility.Extra = extra
	}

	if len(cp.PerRoleCapacity) == 0 {
		cp.PerRoleCapacity = nil
	}
	if len(cp.LeaseTTL) == 0 {
		cp.LeaseTTL = nil
	}
	return &cp
}

// PolicyHash 对 TaskProfile 的 canonical JSON 取 sha256 hex。
// PolicyHash 字段本身不参与哈希；nil profile 返回空字符串。
// 同一内容必得同一哈希；forms/值任一变化必得不同哈希。
func PolicyHash(p *TaskProfile) string {
	if p == nil {
		return ""
	}
	cp := idsCanonicalProfile(p)
	b, err := json.Marshal(cp)
	if err != nil {
		// TaskProfile 的字段类型均可 JSON 编码；仅当出现 NaN/Inf 浮点值时
		// Marshal 会失败。此时用 %#v（fmt 对 map 键排序）作为确定性降级
		// 表示，保证不返回空串、不静默丢信息。
		return CanonicalHash("policy_hash_marshal_error", fmt.Sprintf("%#v", *cp))
	}
	return idsSHA256Hex(b)
}
