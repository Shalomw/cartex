package service

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Autumn-27/artex/lifetime"
	"github.com/Autumn-27/artex/lifetime/index"
)

// buildIndex 是索引器入口的注入点：生产环境为 index.Build，测试可替换为
// 固定 IndexOutput，避免依赖真实源码树与索引器实现。
var buildIndex = index.Build

// bootstrapRequest 是 POST /v1/tasks/{taskID}/bootstrap 的请求体。
//
// profile 可选；缺省使用 lifetime.DefaultTaskProfile()。target_files 与
// target_subdir 至少给一个。max_files 透传给索引器的扫描上限。
type bootstrapRequest struct {
	SourceRoot   string                `json:"source_root"`
	TargetFiles  []string              `json:"target_files"`
	TargetSubdir []string              `json:"target_subdir"`
	MaxFiles     int                   `json:"max_files"`
	MaxPairs     int                   `json:"max_pairs"`
	Profile      *lifetime.TaskProfile `json:"profile,omitempty"`
}

// bootstrap 执行 Phase 0 的索引 bootstrap（§7.5/§8.1）：
//
//  1. 校验 source_root 与 target；
//  2. 规范化/校验 TaskProfile 并计算 policy_hash；
//  3. 调用索引器得到 IndexOutput；
//  4. 生成确定性 lineage（source_manifest_hash + index_hash + build/profile 常量）；
//  5. 落 lineage / profile / index；
//  6. 按 family × domain_pair × form 构造覆盖账本与工作包并初始化 Q1。
//
// 只有 planner / reconciler 可以触发；写回结果供 agent 通过读端点发现。
func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	role, _, err := envelope(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "bad_envelope", err)
		return
	}
	if role != lifetime.RolePlanner && role != lifetime.RoleReconciler {
		writeError(w, http.StatusForbidden, "forbidden", fmt.Errorf("role %s cannot bootstrap a task", role))
		return
	}

	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err)
		return
	}
	req, err := decode[bootstrapRequest](body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if strings.TrimSpace(req.SourceRoot) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("source_root is required"))
		return
	}
	if len(req.TargetFiles) == 0 && len(req.TargetSubdir) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("at least one target_files or target_subdir is required"))
		return
	}
	if req.MaxFiles < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("max_files must be >= 0"))
		return
	}
	// 启发式索引的 domain pair 置信度低；默认只展开 2 对（自对 + 首个跨域对），
	// 避免 family×pair×form 把覆盖账本和 Q1 队列撑到数万行。上限 8。
	maxPairs := req.MaxPairs
	if maxPairs <= 0 {
		maxPairs = 2
	}
	if maxPairs > 8 {
		maxPairs = 8
	}

	profile := req.Profile
	if profile == nil {
		profile = lifetime.DefaultTaskProfile()
	}
	lifetime.ApplyTaskProfileDefaults(profile)
	if err := lifetime.ValidateTaskProfile(profile); err != nil {
		writeError(w, http.StatusBadRequest, "bad_profile", err)
		return
	}
	policyHash := lifetime.ComputePolicyHash(profile)

	idx, err := buildIndex(index.Options{
		SourceRoot:    req.SourceRoot,
		TargetFiles:   req.TargetFiles,
		TargetSubdirs: req.TargetSubdir,
		MaxFiles:      req.MaxFiles,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "index_failed", err)
		return
	}
	if idx == nil {
		writeError(w, http.StatusBadRequest, "index_failed", errors.New("indexer returned nil output"))
		return
	}

	sourceHash := bootstrapSourceHash(req.TargetFiles, req.TargetSubdir, idx.IndexHash)
	lineageInput := lifetime.LineageInput{
		SourceManifestHash:         sourceHash,
		IndexHash:                  idx.IndexHash,
		BuildID:                    "bootstrap",
		DriverSpecHash:             "none",
		PolicyHash:                 policyHash,
		ProfileVersion:             profile.Version,
		HarnessVersion:             "m0",
		InstrumentationProfileHash: "none",
	}
	lineageID := lifetime.LineageIDFrom(lineageInput)

	ctx := r.Context()
	taskID := r.PathValue("taskID")
	if _, err := s.backend.CreateLineage(ctx, taskID, lineageInput); err != nil {
		writeError(w, http.StatusInternalServerError, "create_lineage_failed", err)
		return
	}
	if _, err := s.backend.RegisterTaskProfile(ctx, taskID, profile); err != nil {
		writeError(w, http.StatusInternalServerError, "register_profile_failed", err)
		return
	}
	if err := s.backend.IngestIndex(ctx, taskID, lineageID, *idx); err != nil {
		writeError(w, http.StatusInternalServerError, "ingest_index_failed", err)
		return
	}

	units, packages := buildBootstrapCoverage(idx, req.SourceRoot, lineageID, maxPairs)
	if err := s.backend.InitCoverage(ctx, taskID, lineageID, units, packages); err != nil {
		writeError(w, http.StatusInternalServerError, "init_coverage_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, lifetime.BootstrapResult{
		TaskID:        taskID,
		LineageID:     lineageID,
		PolicyHash:    policyHash,
		IndexHash:     idx.IndexHash,
		IndexQuality:  idx.IndexQuality,
		Families:      len(idx.ObjectFamilies),
		Domains:       len(idx.Domains),
		DomainPairs:   len(idx.DomainPairs),
		CoverageUnits: len(units),
		WorkItems:     len(packages),
	})
}

// bootstrapSourceHash 生成确定性的 source_manifest_hash：
// CanonicalHash("source", sorted(target_files ∪ target_subdir), index_hash)。
// CanonicalHash 自带长度前缀，因此文件与目录路径不会产生拼接歧义。
func bootstrapSourceHash(targetFiles, targetSubdirs []string, indexHash string) string {
	targets := make([]string, 0, len(targetFiles)+len(targetSubdirs))
	targets = append(targets, targetFiles...)
	targets = append(targets, targetSubdirs...)
	sort.Strings(targets)

	parts := make([]string, 0, len(targets)+2)
	parts = append(parts, "source")
	parts = append(parts, targets...)
	parts = append(parts, indexHash)
	return lifetime.CanonicalHash(parts...)
}

// buildBootstrapCoverage 把索引产物展开为覆盖账本行与工作包（§4.5）。
//
//   - 工作包 = family × domain_pair；
//   - 覆盖单位 = family × domain_pair × form；
//   - family.applicable_forms 为空时按 R-4.12 保留一行 unknown 形态的
//     兜底（F2），不让族静默移出分母；
//   - 索引没有 domain_pairs 时合成单域对 (d,d)，d 取 thread 类型、
//     instance_site="<source_root>:1"，domain_confidence=low；
//   - 同 (family, pair, form) / (family, pair) 去重，输出按 ID 排序保证
//     重复 bootstrap 的确定性。
func buildBootstrapCoverage(idx *lifetime.IndexOutput, sourceRoot, lineageID string, maxPairs int) ([]lifetime.CoverageUnit, []lifetime.WorkPackage) {
	if maxPairs <= 0 {
		maxPairs = 2
	}
	pairs := make([]lifetime.DomainPair, 0, len(idx.DomainPairs))
	if len(idx.DomainPairs) > 0 {
		pairs = append(pairs, idx.DomainPairs...)
	} else {
		d := lifetime.DomainClassID(lifetime.DomainThread, sourceRoot+":1", "")
		pairs = append(pairs, lifetime.DomainPair{
			PairClassID:      lifetime.PairClassID(d, d),
			DomainA:          d,
			DomainB:          d,
			DomainConfidence: lifetime.DomainConfidenceLow,
		})
	}
	if len(pairs) > maxPairs {
		pairs = pairs[:maxPairs]
	}

	unitSeen := make(map[string]struct{})
	pkgSeen := make(map[string]struct{})
	units := make([]lifetime.CoverageUnit, 0)
	packages := make([]lifetime.WorkPackage, 0)

	for _, fam := range idx.ObjectFamilies {
		familyID := fam.FamilyID
		if familyID == "" {
			familyID = deriveBootstrapFamilyID(fam)
		}
		if familyID == "" {
			continue // 既无 family_id 也无法从 key 推导：无法记入账本。
		}

		forms := dedupeForms(fam.ApplicableForms)
		if len(forms) == 0 {
			forms = []lifetime.Form{lifetime.FormDanglingThis}
		}

		for _, pair := range pairs {
			pairID := pair.PairClassID
			pkgID := lifetime.CanonicalHash("pkg", familyID, pairID)
			if _, ok := pkgSeen[pkgID]; !ok {
				pkgSeen[pkgID] = struct{}{}
				packages = append(packages, lifetime.WorkPackage{
					PackageID:       pkgID,
					FamilyID:        familyID,
					DomainPairID:    pairID,
					ApplicableForms: append([]lifetime.Form(nil), forms...),
				})
			}

			for _, form := range forms {
				unitID := lifetime.UnitID(lifetime.UnitKindFamily, familyID, "", pairID, form)
				if _, ok := unitSeen[unitID]; ok {
					continue
				}
				unitSeen[unitID] = struct{}{}
				unit := lifetime.NewCoverageUnit(lifetime.UnitKindFamily, familyID, "", pairID, form, lineageID)
				unit.UnitID = unitID
				unit.LineageID = lineageID
				units = append(units, unit)
			}
		}
	}

	sort.Slice(units, func(i, j int) bool { return units[i].UnitID < units[j].UnitID })
	sort.Slice(packages, func(i, j int) bool { return packages[i].PackageID < packages[j].PackageID })
	return units, packages
}

// dedupeForms 去重并保持索引器给出的顺序。
func dedupeForms(forms []lifetime.Form) []lifetime.Form {
	if len(forms) == 0 {
		return nil
	}
	seen := make(map[lifetime.Form]struct{}, len(forms))
	out := make([]lifetime.Form, 0, len(forms))
	for _, f := range forms {
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}

// deriveBootstrapFamilyID 在索引器漏填 family_id 时按 §2.2 从 family key
// 推导；key 不完整时返回空串（调用方跳过该族）。
func deriveBootstrapFamilyID(fam lifetime.ObjectFamily) string {
	if fam.TypeCanonical == "" || fam.AllocSite == "" {
		return ""
	}
	if !fam.StorageClass.Valid() || !fam.LifetimeForm.Valid() {
		return ""
	}
	return lifetime.FamilyID(lifetime.FamilyKey{
		TypeCanonical: fam.TypeCanonical,
		AllocSite:     fam.AllocSite,
		StorageClass:  fam.StorageClass,
		LifetimeForm:  fam.LifetimeForm,
		MemberPath:    fam.MemberPath,
	})
}
