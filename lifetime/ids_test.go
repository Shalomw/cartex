package lifetime

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestCanonicalHashDeterministicAndUnambiguous(t *testing.T) {
	a := CanonicalHash("alpha", "beta")
	b := CanonicalHash("alpha", "beta")
	if a != b {
		t.Fatalf("CanonicalHash not deterministic: %q != %q", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("CanonicalHash length = %d, want 64 hex chars", len(a))
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Fatalf("CanonicalHash output is not hex: %v", err)
	}
	if CanonicalHash("ab", "c") == CanonicalHash("a", "bc") {
		t.Fatalf("CanonicalHash concatenation is ambiguous: (%q,%q) collides with (%q,%q)", "ab", "c", "a", "bc")
	}
	if CanonicalHash("a") == CanonicalHash("a", "") {
		t.Fatalf("CanonicalHash should distinguish one part from one part plus empty part")
	}
	if CanonicalHash() == CanonicalHash("") {
		t.Fatalf("CanonicalHash should distinguish no parts from one empty part")
	}

	// LifetimePointHash：只取 site(file:line)+role，排序去重，顺序无关。
	ptsA := []EvidenceNode{
		{File: "a.cc", Line: 10, Role: NodeLifetimePoint},
		{File: "b.cc", Line: 20, Role: NodeAccess},
		{File: "a.cc", Line: 10, Role: NodeLifetimePoint}, // 重复项
	}
	ptsB := []EvidenceNode{ptsA[1], ptsA[0]}
	if LifetimePointHash(ptsA) != LifetimePointHash(ptsB) {
		t.Fatalf("LifetimePointHash should be order- and duplicate-insensitive")
	}
	ptsC := []EvidenceNode{{File: "a.cc", Line: 11, Role: NodeLifetimePoint}}
	if LifetimePointHash(ptsA) == LifetimePointHash(ptsC) {
		t.Fatalf("LifetimePointHash should change when site changes")
	}
}

func TestIDNewFormat(t *testing.T) {
	id := NewID("cand")
	if !strings.HasPrefix(id, "cand_") {
		t.Fatalf("NewID prefix mismatch: %q", id)
	}
	raw := strings.TrimPrefix(id, "cand_")
	if len(raw) != 32 {
		t.Fatalf("NewID hex length = %d, want 32", len(raw))
	}
	if _, err := hex.DecodeString(raw); err != nil {
		t.Fatalf("NewID body is not hex: %v", err)
	}
	if other := NewID("cand"); other == id {
		t.Fatalf("NewID returned duplicate value %q", id)
	}
	if empty := NewID(""); !strings.HasPrefix(empty, "_") || len(empty) != 33 {
		t.Fatalf("NewID with empty prefix = %q, want _<32 hex>", empty)
	}
}

func TestFamilyIDMemberPathParticipates(t *testing.T) {
	base := FamilyKey{
		TypeCanonical: "ns::Foo",
		AllocSite:     "foo.cc:42",
		StorageClass:  StorageMember,
		LifetimeForm:  LifetimeShared,
		MemberPath:    "owner_",
	}
	if err := ValidateFamilyKey(base); err != nil {
		t.Fatalf("valid member key rejected: %v", err)
	}
	if FamilyID(base) != FamilyID(base) {
		t.Fatalf("FamilyID not deterministic")
	}

	otherPath := base
	otherPath.MemberPath = "callback_"
	if FamilyID(base) == FamilyID(otherPath) {
		t.Fatalf("member_path must participate in family_id when storage_class=member (R-2.2)")
	}

	missing := base
	missing.MemberPath = ""
	if err := ValidateFamilyKey(missing); err == nil {
		t.Fatalf("ValidateFamilyKey should reject member family without member_path (R-2.2)")
	}
	if FamilyID(missing) != FamilyID(missing) {
		t.Fatalf("FamilyID must stay deterministic for invalid keys")
	}
	if FamilyID(missing) == FamilyID(base) {
		t.Fatalf("FamilyID must not silently treat missing member_path as a valid empty path")
	}

	// 非 member 存储类别不要求 member_path。
	heapKey := FamilyKey{
		TypeCanonical: "ns::Foo",
		AllocSite:     "foo.cc:42",
		StorageClass:  StorageHeap,
		LifetimeForm:  LifetimeRaw,
	}
	if err := ValidateFamilyKey(heapKey); err != nil {
		t.Fatalf("valid heap key rejected: %v", err)
	}

	badForm := heapKey
	badForm.LifetimeForm = LifetimeForm("bogus")
	if err := ValidateFamilyKey(badForm); err == nil {
		t.Fatalf("ValidateFamilyKey should reject invalid lifetime_form")
	}
}

func TestDomainAndPairIDs(t *testing.T) {
	d1 := DomainClassID(DomainQueue, "submit.cc:10", "worker_pool")
	d2 := DomainClassID(DomainQueue, "submit.cc:10", "worker_pool")
	if d1 != d2 {
		t.Fatalf("DomainClassID not deterministic")
	}
	if d1 == DomainClassID(DomainQueue, "submit.cc:11", "worker_pool") {
		t.Fatalf("DomainClassID should depend on instance_site")
	}
	if d1 == DomainClassID(DomainExecutor, "submit.cc:10", "worker_pool") {
		t.Fatalf("DomainClassID should depend on kind")
	}
	if DomainInstanceID(d1, 1) == DomainInstanceID(d1, 2) {
		t.Fatalf("DomainInstanceID should depend on ordinal")
	}

	if DomainPairID("da", "db") != DomainPairID("db", "da") {
		t.Fatalf("DomainPairID must be unordered")
	}
	if DomainPairID("da", "db") == DomainPairID("da", "dc") {
		t.Fatalf("DomainPairID should depend on members")
	}
	if DomainPairID("d", "d") != DomainPairID("d", "d") {
		t.Fatalf("DomainPairID (d,d) must be deterministic")
	}
	if PairClassID("ca", "cb") != PairClassID("cb", "ca") {
		t.Fatalf("PairClassID must be unordered")
	}
	if PairInstanceID("ia", "ib") != PairInstanceID("ib", "ia") {
		t.Fatalf("PairInstanceID must be unordered")
	}
}

func TestAccessSetHashOrderIndependent(t *testing.T) {
	a := []Access{
		{Site: "a.cc:1", Domain: "d1", GuardedBy: "mu"},
		{Site: "b.cc:2", Domain: "d2", GuardedBy: ""},
	}
	reversed := []Access{a[1], a[0]}
	if AccessSetHash(a) != AccessSetHash(reversed) {
		t.Fatalf("AccessSetHash must be order-independent")
	}
	withDup := []Access{a[0], a[1], a[0]}
	if AccessSetHash(a) != AccessSetHash(withDup) {
		t.Fatalf("AccessSetHash must dedupe identical access entries")
	}
	changed := []Access{a[0], {Site: "b.cc:2", Domain: "d2", GuardedBy: "mtx"}}
	if AccessSetHash(a) == AccessSetHash(changed) {
		t.Fatalf("AccessSetHash must depend on guarded_by")
	}
	if AccessSetHash(nil) != AccessSetHash([]Access{}) {
		t.Fatalf("AccessSetHash nil and empty should match")
	}
}

func TestHolderSetHashOrderIndependent(t *testing.T) {
	h := []Holder{
		{Site: "reg.cc:3", Form: "raw_this", Domain: "d1"},
		{Site: "reg.cc:4", Form: "weak"},
	}
	reversed := []Holder{h[1], h[0]}
	if HolderSetHash(h) != HolderSetHash(reversed) {
		t.Fatalf("HolderSetHash must be order-independent")
	}
	changed := []Holder{h[0], {Site: "reg.cc:4", Form: "shared"}}
	if HolderSetHash(h) == HolderSetHash(changed) {
		t.Fatalf("HolderSetHash must depend on holder.form")
	}
	if HolderSetHash(nil) != HolderSetHash([]Holder{}) {
		t.Fatalf("HolderSetHash nil and empty should match")
	}
}

func TestCandidateKeyPrimaryFormSensitive(t *testing.T) {
	lph := LifetimePointHash([]EvidenceNode{{File: "a.cc", Line: 1, Role: NodeLifetimePoint}})
	ash := AccessSetHash([]Access{{Site: "a.cc:2", GuardedBy: ""}})
	hsh := HolderSetHash([]Holder{{Site: "a.cc:3", Form: "raw_this"}})
	base := CandidateKey("fam", "pair", FormRefcountRace, lph, ash, hsh)
	if base != CandidateKey("fam", "pair", FormRefcountRace, lph, ash, hsh) {
		t.Fatalf("CandidateKey not deterministic")
	}
	if base == CandidateKey("fam", "pair", FormDanglingThis, lph, ash, hsh) {
		t.Fatalf("CandidateKey must depend on primary form")
	}
	if base == CandidateKey("fam2", "pair", FormRefcountRace, lph, ash, hsh) {
		t.Fatalf("CandidateKey must depend on family_id")
	}
	if base == CandidateKey("fam", "pair", FormRefcountRace, lph, ash, HolderSetHash(nil)) {
		t.Fatalf("CandidateKey must depend on holder_set_hash")
	}
}

func TestMechanismKeyFamilySetCanonical(t *testing.T) {
	lph := "lph"
	ash := "ash"
	a := MechanismKey([]string{"famB", "famA", "famA"}, lph, ash, "missing_guard")
	b := MechanismKey([]string{"famA", "famB"}, lph, ash, "missing_guard")
	if a != b {
		t.Fatalf("MechanismKey must sort/dedupe family_set")
	}
	if a == MechanismKey([]string{"famA", "famB"}, lph, ash, "missing_unregister") {
		t.Fatalf("MechanismKey must depend on root_cause_kind")
	}
	if a == MechanismKey([]string{"famA", "famC"}, lph, ash, "missing_guard") {
		t.Fatalf("MechanismKey must depend on family_set content")
	}
}

func TestUnresolvedDomainPairIDFormat(t *testing.T) {
	got := UnresolvedDomainPairID("fam123", "ash456")
	want := "unresolved:fam123:ash456"
	if got != want {
		t.Fatalf("UnresolvedDomainPairID = %q, want %q (R-2.1)", got, want)
	}
	if !strings.HasPrefix(got, "unresolved:") {
		t.Fatalf("UnresolvedDomainPairID must use the unresolved: prefix")
	}
}

func TestEvidenceSetHashCanonicalAndDedup(t *testing.T) {
	a := EvidenceSetHash([]string{"e3", "e1", "e2", "e1"})
	b := EvidenceSetHash([]string{"e1", "e2", "e3"})
	if a != b {
		t.Fatalf("EvidenceSetHash must be order- and duplicate-insensitive")
	}
	if a == EvidenceSetHash([]string{"e1", "e2"}) {
		t.Fatalf("EvidenceSetHash must depend on set content")
	}
}

func TestPolicyHashStableAndFormSensitive(t *testing.T) {
	p := DefaultTaskProfile()
	h := PolicyHash(p)
	if h == "" || len(h) != 64 {
		t.Fatalf("PolicyHash returned %q, want 64 hex chars", h)
	}
	if h != PolicyHash(p) {
		t.Fatalf("PolicyHash must be stable for identical profiles")
	}
	// PolicyHash 字段本身不参与哈希。
	withHash := DefaultTaskProfile()
	withHash.PolicyHash = "deadbeef"
	if PolicyHash(withHash) != h {
		t.Fatalf("PolicyHash must exclude the PolicyHash field")
	}
	// form 变化必须改变哈希。
	q := DefaultTaskProfile()
	q.Forms = q.Forms[:len(q.Forms)-1]
	if PolicyHash(q) == h {
		t.Fatalf("PolicyHash must depend on forms")
	}
	// map 插入顺序不影响哈希（encoding/json 对 map 键排序）。
	r := DefaultTaskProfile()
	r.PerRoleCapacity = map[Role]int{
		RoleReconciler:       1,
		RoleOwner:            2,
		RoleReporter:         1,
		RoleLifetimeReviewer: 2,
		RoleLifetimeVerifier: 2,
		RoleLifetimeAuditor:  2,
	}
	if PolicyHash(r) != h {
		t.Fatalf("PolicyHash must not depend on map insertion order")
	}
	if PolicyHash(nil) != "" {
		t.Fatalf("PolicyHash(nil) should be empty")
	}
}

func TestLineageIDFromStableAndSensitive(t *testing.T) {
	in := LineageInput{
		SourceManifestHash:         "s",
		IndexHash:                  "i",
		BuildID:                    "b",
		DriverSpecHash:             "d",
		PolicyHash:                 "p",
		ProfileVersion:             "1.0",
		HarnessVersion:             "h",
		InstrumentationProfileHash: "ip",
	}
	id := LineageIDFrom(in)
	if id != LineageIDFrom(in) {
		t.Fatalf("LineageIDFrom not deterministic")
	}
	changed := in
	changed.DriverSpecHash = "d2"
	if id == LineageIDFrom(changed) {
		t.Fatalf("LineageIDFrom must depend on driver_spec_hash")
	}
	swapped := in
	swapped.IndexHash = "s"
	swapped.SourceManifestHash = "i"
	if id == LineageIDFrom(swapped) {
		t.Fatalf("LineageIDFrom must be order-sensitive to its fields")
	}

	m1 := SourceManifest{Files: []SourceFile{
		{Path: "b.cc", SHA256: "2", Generated: false},
		{Path: "a.cc", SHA256: "1", Generated: false},
	}}
	m2 := SourceManifest{Files: []SourceFile{
		{Path: "a.cc", SHA256: "1", Generated: false},
		{Path: "b.cc", SHA256: "2", Generated: false},
		{Path: "b.cc", SHA256: "2", Generated: false}, // 完全重复项去重
	}}
	if m1.Hash() != m2.Hash() {
		t.Fatalf("SourceManifest.Hash must be path-order-insensitive and dedupe exact duplicates")
	}
	m3 := SourceManifest{Files: []SourceFile{{Path: "a.cc", SHA256: "1", Generated: true}}}
	if m1.Hash() == m3.Hash() {
		t.Fatalf("SourceManifest.Hash must include generated flag")
	}

	b1 := BuildIDFrom(BuildIDInput{BuildHash: "bh", VariantID: "v", Compiler: "clang", Flags: "-O1", Sanitizer: "asan", TargetABI: "x86_64"})
	b2 := BuildIDFrom(BuildIDInput{BuildHash: "bh", VariantID: "v2", Compiler: "clang", Flags: "-O1", Sanitizer: "asan", TargetABI: "x86_64"})
	if b1 == b2 {
		t.Fatalf("BuildIDFrom must depend on variant_id")
	}
}
