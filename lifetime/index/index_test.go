package index

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/lifetime"
)

func projectOptions() Options {
	return Options{
		SourceRoot:    "testdata/project",
		TargetSubdirs: []string{"src"},
	}
}

func familyWhere(out *lifetime.IndexOutput, pred func(lifetime.ObjectFamily) bool) *lifetime.ObjectFamily {
	for i := range out.ObjectFamilies {
		if pred(out.ObjectFamilies[i]) {
			return &out.ObjectFamilies[i]
		}
	}
	return nil
}

func hasType(out *lifetime.IndexOutput, typ string) bool {
	return familyWhere(out, func(f lifetime.ObjectFamily) bool {
		return f.TypeCanonical == typ || strings.HasPrefix(f.TypeCanonical, typ+"<")
	}) != nil
}

func formSet(out *lifetime.IndexOutput) map[lifetime.Form]bool {
	seen := make(map[lifetime.Form]bool)
	for _, f := range out.ObjectFamilies {
		for _, form := range f.ApplicableForms {
			seen[form] = true
		}
	}
	return seen
}

func relationTypes(out *lifetime.IndexOutput) map[string]bool {
	seen := make(map[string]bool)
	for _, rel := range out.ObjectRelations {
		seen[rel.Type] = true
	}
	return seen
}

func TestBuildFamiliesAndForms(t *testing.T) {
	out, err := Build(projectOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if out.IndexQuality != "heuristic" {
		t.Fatalf("index_quality = %q, want heuristic", out.IndexQuality)
	}
	if out.TypeResolution != "partial" {
		t.Fatalf("type_resolution = %q, want partial", out.TypeResolution)
	}
	if len(out.IndexHash) != 64 {
		t.Fatalf("index_hash length = %d, want 64 hex", len(out.IndexHash))
	}

	// 预期类型：分配点、类定义、仅方法定义（JSNumberFormat/Foo）。
	for _, typ := range []string{
		"Widget", "Observer", "RefCountedBase", "Config", "Registry",
		"NeverAllocated", "JSNumberFormat", "Foo",
		"icu::number::LocalizedNumberFormatter",
	} {
		if !hasType(out, typ) {
			t.Errorf("missing family for type %q", typ)
		}
	}
	// 真实 V8 验证中必须保留的命名空间限定 shared 类型。
	if f := familyWhere(out, func(f lifetime.ObjectFamily) bool {
		return f.TypeCanonical == "icu::number::LocalizedNumberFormatter" &&
			f.LifetimeForm == lifetime.LifetimeShared
	}); f == nil {
		t.Errorf("icu::number::LocalizedNumberFormatter should have a shared family")
	}
	// 注释/字符串/非 C++ 扩展名中的 new 不得产生 family。
	for _, ghost := range []string{"GhostFromComment", "GhostFromString", "GhostFromTxtFile"} {
		if hasType(out, ghost) {
			t.Errorf("unexpected family for %q from comment/string/ignored file", ghost)
		}
	}

	// 类定义即生成 family（不依赖 new）。
	if f := familyWhere(out, func(f lifetime.ObjectFamily) bool {
		return f.TypeCanonical == "NeverAllocated" &&
			f.StorageClass == lifetime.StorageHeap &&
			f.LifetimeForm == lifetime.LifetimeRaw
	}); f == nil {
		t.Errorf("NeverAllocated should have a heap/raw declaration family")
	}
	// 仅有 Foo::Bar() 方法实现、无 new/类定义的类型也必须出现。
	if f := familyWhere(out, func(f lifetime.ObjectFamily) bool {
		return f.TypeCanonical == "Foo" &&
			f.StorageClass == lifetime.StorageHeap &&
			f.LifetimeForm == lifetime.LifetimeRaw
	}); f == nil {
		t.Errorf("Foo should have a method-definition family")
	}

	if f := familyWhere(out, func(f lifetime.ObjectFamily) bool {
		return f.TypeCanonical == "Widget" &&
			f.StorageClass == lifetime.StorageHeap &&
			f.LifetimeForm == lifetime.LifetimeRaw
	}); f == nil {
		t.Errorf("Widget should have heap/raw family")
	}

	if f := familyWhere(out, func(f lifetime.ObjectFamily) bool {
		return f.TypeCanonical == "Observer" &&
			f.StorageClass == lifetime.StorageHeap &&
			f.LifetimeForm == lifetime.LifetimeShared
	}); f == nil {
		t.Errorf("Observer should have heap/shared family from make_shared")
	}
	if f := familyWhere(out, func(f lifetime.ObjectFamily) bool {
		return f.TypeCanonical == "Observer" && f.LifetimeForm == lifetime.LifetimeWeak
	}); f == nil {
		t.Errorf("Observer should have weak family")
	}
	if f := familyWhere(out, func(f lifetime.ObjectFamily) bool {
		return f.StorageClass == lifetime.StorageStatic &&
			f.LifetimeForm == lifetime.LifetimeSingleton
	}); f == nil {
		t.Errorf("expected a static/singleton family")
	}
	if f := familyWhere(out, func(f lifetime.ObjectFamily) bool {
		if f.TypeCanonical != "RefCountedBase" {
			return false
		}
		for _, form := range f.ApplicableForms {
			if form == lifetime.FormRefcountRace {
				return true
			}
		}
		return false
	}); f == nil {
		t.Errorf("RefCountedBase should have F1 applicable form")
	}
	if f := familyWhere(out, func(f lifetime.ObjectFamily) bool {
		return f.TypeCanonical == "Observer" &&
			f.StorageClass == lifetime.StoragePool
	}); f == nil {
		t.Errorf("ObjectPool<Observer> should have pool family")
	}
	if f := familyWhere(out, func(f lifetime.ObjectFamily) bool {
		return f.TypeCanonical == "Observer" &&
			f.StorageClass == lifetime.StorageHandle
	}); f == nil {
		t.Errorf("HandleTable<Observer> should have handle family")
	}

	// applicable_forms 至少覆盖 F1/F2/F3/F5/F6。
	forms := formSet(out)
	for _, want := range []lifetime.Form{
		lifetime.FormRefcountRace,
		lifetime.FormDanglingThis,
		lifetime.FormRegisterLeak,
		lifetime.FormStaticDestroy,
		lifetime.FormSharedPtrRace,
		lifetime.FormContainerInvalid,
	} {
		if !forms[want] {
			t.Errorf("applicable_forms missing %s", want)
		}
	}
	for _, f := range out.ObjectFamilies {
		if len(f.ApplicableForms) == 0 {
			t.Errorf("family %s has empty applicable_forms", f.FamilyID)
		}
		if f.Confidence != lifetime.ConfidenceLow {
			t.Errorf("family %s confidence = %q, want low", f.FamilyID, f.Confidence)
		}
		if f.IdentityConfidence != lifetime.ConfidenceLow {
			t.Errorf("family %s identity_confidence = %q, want low", f.FamilyID, f.IdentityConfidence)
		}
		if len(f.Domains) == 0 {
			t.Errorf("family %s has no domains", f.FamilyID)
		}
		if len(f.Domains) > 4 {
			t.Errorf("family %s has %d domains, want <= 4", f.FamilyID, len(f.Domains))
		}
	}

	// 域与域对必须有上限；优先保留 (first,first) 自对。
	if len(out.Domains) != 8 {
		t.Errorf("domains = %d, want capped at 8", len(out.Domains))
	}
	if len(out.DomainPairs) != 8 {
		t.Errorf("domain_pairs = %d, want capped at 8", len(out.DomainPairs))
	}
	var sawDomainCap, sawPairCap bool
	for _, u := range out.Unknowns {
		if u.Detail == "domain_cap" {
			sawDomainCap = true
		}
		if u.Detail == "domain_pairs_cap" {
			sawPairCap = true
		}
	}
	if !sawDomainCap {
		t.Errorf("expected domain_cap unknown")
	}
	if !sawPairCap {
		t.Errorf("expected domain_pairs_cap unknown")
	}
	sawSelfPair := false
	for _, p := range out.DomainPairs {
		if p.DomainA == p.DomainB {
			sawSelfPair = true
		}
		if p.DomainConfidence != lifetime.DomainConfidenceLow {
			t.Errorf("pair confidence = %q, want low", p.DomainConfidence)
		}
		if len(p.PairBasisOptions) != 2 {
			t.Errorf("pair basis options = %v, want logical+execution", p.PairBasisOptions)
		}
	}
	if !sawSelfPair {
		t.Errorf("expected a (first,first) domain pair")
	}

	// 同步原语 protects 不得为空（R-8.12）。
	if len(out.SyncPrimitives) == 0 {
		t.Errorf("expected sync primitives")
	}
	for _, sp := range out.SyncPrimitives {
		if sp.Protects == "" {
			t.Errorf("sync primitive %q at %s has empty protects", sp.Primitive, sp.FileLine)
		}
		if sp.Scope == "" {
			t.Errorf("sync primitive %q at %s has empty scope", sp.Primitive, sp.FileLine)
		}
	}

	// 回调：RegisterObserver 有配对反注册；AddListener 捕获 weak。
	if len(out.Callbacks) == 0 {
		t.Errorf("expected callbacks")
	}
	var sawPair, sawWeak bool
	for _, cb := range out.Callbacks {
		if cb.UnregisterSite != "" {
			sawPair = true
		}
		if cb.Capture == "weak" {
			sawWeak = true
		}
		if cb.Capture == "" {
			t.Errorf("callback %s has empty capture", cb.RegisterSite)
		}
	}
	if !sawPair {
		t.Errorf("expected a register/unregister pair")
	}
	if !sawWeak {
		t.Errorf("expected weak capture from std::weak_ptr listener")
	}

	// 关系：成员字段 owns + 注册 registers。
	rels := relationTypes(out)
	if !rels["owns"] {
		t.Errorf("expected owns relations from member fields")
	}
	if !rels["registers"] {
		t.Errorf("expected registers relations from Register/Unregister pair")
	}

	// 生命周期点非空，且每个 family 至少有一个。
	if len(out.LifetimePoints) == 0 {
		t.Errorf("expected lifetime points")
	}
	withPoint := make(map[string]bool)
	for _, lp := range out.LifetimePoints {
		withPoint[lp.FamilyID] = true
		if lp.SourceRef == "" || lp.Site == "" {
			t.Errorf("lifetime point for %s has empty site/source_ref", lp.FamilyID)
		}
		if lp.Role != lifetime.NodeLifetimePoint {
			t.Errorf("lifetime point role = %q, want lifetime_point", lp.Role)
		}
	}
	for _, f := range out.ObjectFamilies {
		if !withPoint[f.FamilyID] {
			t.Errorf("family %s has no lifetime point", f.FamilyID)
		}
	}
}

func TestBuildDeterministic(t *testing.T) {
	first, err := Build(projectOptions())
	if err != nil {
		t.Fatalf("first Build: %v", err)
	}
	second, err := Build(projectOptions())
	if err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if first.IndexHash != second.IndexHash {
		t.Fatalf("index_hash not deterministic: %s != %s", first.IndexHash, second.IndexHash)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Build output not deterministic")
	}
}

func TestBuildMaxFilesAndMissingFile(t *testing.T) {
	opts := projectOptions()
	opts.MaxFiles = 2
	out, err := Build(opts)
	if err != nil {
		t.Fatalf("Build with MaxFiles: %v", err)
	}
	var sawMaxFiles bool
	for _, u := range out.Unknowns {
		if strings.Contains(u.Detail, "max_files") && u.Reason == lifetime.UnknownIndexFailed {
			sawMaxFiles = true
		}
	}
	if !sawMaxFiles {
		t.Errorf("expected max_files unknown, got %+v", out.Unknowns)
	}

	missing := Options{
		SourceRoot:  "testdata/project",
		TargetFiles: []string{"src/objects/does_not_exist.cc"},
		TargetSubdirs: []string{
			"src/util",
		},
	}
	missingOut, err := Build(missing)
	if err != nil {
		t.Fatalf("Build with missing TargetFile must not fail: %v", err)
	}
	var sawMissing bool
	for _, u := range missingOut.Unknowns {
		if u.Scope == "src/objects/does_not_exist.cc" && u.Reason == lifetime.UnknownIndexFailed {
			sawMissing = true
		}
	}
	if !sawMissing {
		t.Errorf("expected unknown for missing target file, got %+v", missingOut.Unknowns)
	}
}

func TestStripSourcePreservesLinesAndHidesLiterals(t *testing.T) {
	src := []byte("a // new X()\nb/* c\nd */e\nf \"new Y()\" g\nh 'x'\n")
	got, warnings := stripSource(src)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if strings.Contains(got, "new X") || strings.Contains(got, "new Y") {
		t.Fatalf("comment/string content not stripped: %q", got)
	}
	if strings.Count(got, "\n") != strings.Count(string(src), "\n") {
		t.Fatalf("newlines not preserved")
	}
	if len(got) != len(src) {
		t.Fatalf("length changed: %d != %d", len(got), len(src))
	}
}
