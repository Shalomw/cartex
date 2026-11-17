package lifetime

// 本文件实现 §2.2/§3.5/§10.3 的 lineage 输入规范化与 ID 生成。
// lineage 是 source/index/build/driver/policy/profile/harness/instrumentation
// 的内容哈希；任一分量变化即产生新 lineage（R-3.2），旧 assessment 置 stale。
// confirmation lineage 只允许 driver_spec_hash 不同（R-10.9）。

import (
	"fmt"
	"sort"
	"strconv"
)

// SourceFile 是规范化 source manifest 的一个文件项（R-3.4）：
// 只记录路径、sha256 与是否生成文件，不包含构建产物。
type SourceFile struct {
	Path      string
	SHA256    string
	Generated bool
}

// SourceManifest 是 source root 的规范化文件清单（R-3.4）。
type SourceManifest struct {
	Files []SourceFile
}

// Hash 按 path 排序（同 path 时依次按 sha256、generated 排序）后对
// path + sha256 + generated 取 CanonicalHash。完全重复的项去重，
// 使清单的物理顺序不影响结果。
func (m SourceManifest) Hash() string {
	files := make([]SourceFile, len(m.Files))
	copy(files, m.Files)
	sort.Slice(files, func(i, j int) bool {
		if files[i].Path != files[j].Path {
			return files[i].Path < files[j].Path
		}
		if files[i].SHA256 != files[j].SHA256 {
			return files[i].SHA256 < files[j].SHA256
		}
		return !files[i].Generated && files[j].Generated
	})

	parts := make([]string, 0, len(files)*3)
	lastPath, lastSHA, lastGen := "", "", false
	haveLast := false
	for _, f := range files {
		if haveLast && f.Path == lastPath && f.SHA256 == lastSHA && f.Generated == lastGen {
			continue
		}
		parts = append(parts, f.Path, f.SHA256, strconv.FormatBool(f.Generated))
		lastPath, lastSHA, lastGen = f.Path, f.SHA256, f.Generated
		haveLast = true
	}
	return CanonicalHash(parts...)
}

// BuildIDInput 是 build_id 的规范输入（§2.2）。
type BuildIDInput struct {
	BuildHash string
	VariantID string
	Compiler  string
	Flags     string
	Sanitizer string
	TargetABI string
}

// BuildIDFrom = hash(build_hash, variant_id, compiler, flags, sanitizer, target_abi)。
func BuildIDFrom(in BuildIDInput) string {
	return CanonicalHash(in.BuildHash, in.VariantID, in.Compiler,
		in.Flags, in.Sanitizer, in.TargetABI)
}

// LineageInput 是 §2.2/§10.3 的 lineage 八个分量，字段顺序即哈希顺序。
type LineageInput struct {
	SourceManifestHash         string
	IndexHash                  string
	BuildID                    string
	DriverSpecHash             string
	PolicyHash                 string
	ProfileVersion             string
	HarnessVersion             string
	InstrumentationProfileHash string
}

// LineageIDFrom = hash(source_manifest_hash, index_hash, build_id,
// driver_spec_hash, policy_hash, profile_version, harness_version,
// instrumentation_profile_hash)。
func LineageIDFrom(in LineageInput) string {
	return CanonicalHash(
		in.SourceManifestHash,
		in.IndexHash,
		in.BuildID,
		in.DriverSpecHash,
		in.PolicyHash,
		in.ProfileVersion,
		in.HarnessVersion,
		in.InstrumentationProfileHash,
	)
}

// ValidateConfirmationLineage 校验 confirmation lineage 与 claim lineage
// 只允许 driver_spec_hash 不同（R-10.9）。其他任一分量不同即不是
// confirmation，而是新的 claim lineage。
func ValidateConfirmationLineage(claim, confirmation LineageInput) error {
	fields := []struct {
		name string
		a, b string
	}{
		{"source_manifest_hash", claim.SourceManifestHash, confirmation.SourceManifestHash},
		{"index_hash", claim.IndexHash, confirmation.IndexHash},
		{"build_id", claim.BuildID, confirmation.BuildID},
		{"policy_hash", claim.PolicyHash, confirmation.PolicyHash},
		{"profile_version", claim.ProfileVersion, confirmation.ProfileVersion},
		{"harness_version", claim.HarnessVersion, confirmation.HarnessVersion},
		{"instrumentation_profile_hash", claim.InstrumentationProfileHash, confirmation.InstrumentationProfileHash},
	}
	for _, f := range fields {
		if f.a != f.b {
			return fmt.Errorf(
				"lifetime: confirmation lineage differs in %s (R-10.9): claim=%q confirmation=%q; only driver_spec_hash may differ",
				f.name, f.a, f.b)
		}
	}
	return nil
}
