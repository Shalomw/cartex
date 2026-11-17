// Package index 实现 lifetime 的启发式 C++ 索引器（§8.1）。
//
// 索引器只用标准库，通过“去注释/字符串 + 正则/状态机”做近似解析，
// 不要求 compile_commands.json，也不做完整语义分析。所有无法确定的
// 事实必须降级为 unknown（R-8.3），不得让单个文件/单个声明中断整体构建。
//
// 输出契约见 lifetime.IndexOutput（§8.1）：
//   - index_quality 恒为 heuristic；
//   - type_resolution 在完全没有类型时降级为 unknown，否则 partial；
//   - index_hash 由排序后的 relpath:sha256 清单与输出事实摘要共同决定，
//     同一输入两次构建必须得到同一哈希。
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Autumn-27/artex/lifetime"
)

// Options 是索引器输入。路径均相对 SourceRoot，除非显式给出绝对路径。
type Options struct {
	// SourceRoot 是源码根，例如 /src/v8。
	SourceRoot string
	// TargetFiles 必须包含在索引范围内（相对 SourceRoot）。
	TargetFiles []string
	// TargetSubdirs 递归收集其中的 C/C++ 源文件（相对 SourceRoot）。
	TargetSubdirs []string
	// MaxFiles 是文件数上限；0 表示默认 400。
	MaxFiles int
}

const (
	defaultMaxFiles   = 400
	maxSourceFileSize = 4 << 20 // 单文件 >4MB 跳过（§8.1 启发式边界）
)

// sourceExtensions 是 TargetSubdirs 递归收集的扩展名集合（§8.1）。
var sourceExtensions = map[string]bool{
	".cc":  true,
	".cpp": true,
	".cxx": true,
	".h":   true,
	".hpp": true,
	".hh":  true,
}

// Build 构建启发式索引输出（§8.1）。
func Build(opts Options) (*lifetime.IndexOutput, error) {
	root := strings.TrimSpace(opts.SourceRoot)
	if root == "" {
		return nil, errors.New("index: source_root is required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("index: resolve source_root: %w", err)
	}
	info, err := os.Stat(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("index: stat source_root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("index: source_root %q is not a directory", root)
	}

	rels, unknowns := selectFiles(rootAbs, opts)

	files := make([]*sourceFile, 0, len(rels))
	for _, rel := range rels {
		sf, uk := loadSourceFile(rootAbs, rel)
		if uk != nil {
			unknowns = append(unknowns, *uk)
			continue
		}
		files = append(files, sf)
	}

	a := newAnalyzer(root, files)
	out := a.run()

	// 剥离阶段的告警（未闭合注释/字符串）同样是解析失败（R-8.3）。
	for _, sf := range files {
		for _, w := range sf.warnings {
			out.Unknowns = append(out.Unknowns, lifetime.IndexUnknown{
				Scope:  sf.rel,
				Reason: lifetime.UnknownIndexFailed,
				Detail: w,
			})
		}
	}
	out.Unknowns = append(out.Unknowns, unknowns...)

	finalize(out, files)
	return out, nil
}

// selectFiles 实现 §8.1 的文件选择：TargetFiles 必须包含；TargetSubdirs
// 递归收集 C/C++ 扩展名；去重后按相对路径排序；MaxFiles 截断并记录 unknown。
func selectFiles(rootAbs string, opts Options) ([]string, []lifetime.IndexUnknown) {
	explicit := make(map[string]bool)
	all := make(map[string]bool)
	var unknowns []lifetime.IndexUnknown

	for _, p := range opts.TargetFiles {
		rel, ok := relativeToRoot(rootAbs, p)
		if !ok {
			unknowns = append(unknowns, lifetime.IndexUnknown{
				Scope:  p,
				Reason: lifetime.UnknownIndexFailed,
				Detail: "outside_source_root",
			})
			continue
		}
		explicit[rel] = true
		all[rel] = true
	}

	for _, d := range opts.TargetSubdirs {
		rel, ok := relativeToRoot(rootAbs, d)
		if !ok {
			unknowns = append(unknowns, lifetime.IndexUnknown{
				Scope:  d,
				Reason: lifetime.UnknownIndexFailed,
				Detail: "outside_source_root",
			})
			continue
		}
		dirAbs := filepath.Join(rootAbs, filepath.FromSlash(rel))
		walkErr := filepath.WalkDir(dirAbs, func(path string, de fs.DirEntry, err error) error {
			if err != nil {
				unknowns = append(unknowns, lifetime.IndexUnknown{
					Scope:  rel,
					Reason: lifetime.UnknownIndexFailed,
					Detail: "walk: " + err.Error(),
				})
				return nil
			}
			if de.IsDir() {
				return nil
			}
			if !sourceExtensions[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			r, err := filepath.Rel(rootAbs, path)
			if err != nil {
				unknowns = append(unknowns, lifetime.IndexUnknown{
					Scope:  rel,
					Reason: lifetime.UnknownIndexFailed,
					Detail: "relative: " + err.Error(),
				})
				return nil
			}
			all[filepath.ToSlash(r)] = true
			return nil
		})
		if walkErr != nil {
			unknowns = append(unknowns, lifetime.IndexUnknown{
				Scope:  rel,
				Reason: lifetime.UnknownIndexFailed,
				Detail: "walk: " + walkErr.Error(),
			})
		}
	}

	list := make([]string, 0, len(all))
	for rel := range all {
		list = append(list, rel)
	}
	sort.Strings(list)

	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = defaultMaxFiles
	}
	if len(list) > maxFiles {
		// 显式 TargetFiles 优先保留（“TargetFiles 必须包含”），其余按
		// 相对路径排序填充；最终输出仍按路径排序，保证确定性。
		kept := make([]string, 0, maxFiles)
		for _, rel := range list {
			if explicit[rel] && len(kept) < maxFiles {
				kept = append(kept, rel)
			}
		}
		for _, rel := range list {
			if !explicit[rel] && len(kept) < maxFiles {
				kept = append(kept, rel)
			}
		}
		sort.Strings(kept)
		list = kept
		unknowns = append(unknowns, lifetime.IndexUnknown{
			Scope:  "index",
			Reason: lifetime.UnknownIndexFailed,
			Detail: "max_files",
		})
	}

	return list, unknowns
}

// relativeToRoot 把用户输入规范化为相对 SourceRoot 的 slash 路径。
// 越界路径（.. 逃逸）返回 false。
func relativeToRoot(rootAbs, p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false
	}
	var rel string
	if filepath.IsAbs(p) {
		r, err := filepath.Rel(rootAbs, filepath.Clean(p))
		if err != nil {
			return "", false
		}
		rel = r
	} else {
		rel = filepath.Clean(filepath.FromSlash(p))
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// sourceFile 是一份已完成注释/字符串剥离的源文件。text 与原始文件等长
// （注释和字面量内容替换为空格，换行保留），因此偏移量与 line 均与原始
// 文件一致，SourceRef 可以直接使用原始 file:line。
type sourceFile struct {
	rel        string
	abs        string
	text       string
	sha        string
	lineStarts []int
	warnings   []string

	braceOpens  []int
	openToClose map[int]int

	// classRanges/memberRanges 用于避免把类内成员声明重复识别为
	// 堆分配或静态对象；区间使用 stripped text 的字节偏移。
	classRanges  []span
	memberRanges []span
}

// span 是 [start, end) 字节区间。
type span struct {
	start int
	end   int
}

func loadSourceFile(rootAbs, rel string) (*sourceFile, *lifetime.IndexUnknown) {
	abs := filepath.Join(rootAbs, filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if err != nil {
		return nil, &lifetime.IndexUnknown{
			Scope:  rel,
			Reason: lifetime.UnknownIndexFailed,
			Detail: "stat: " + err.Error(),
		}
	}
	if info.IsDir() {
		return nil, &lifetime.IndexUnknown{
			Scope:  rel,
			Reason: lifetime.UnknownIndexFailed,
			Detail: "is_directory",
		}
	}
	if info.Size() > maxSourceFileSize {
		return nil, &lifetime.IndexUnknown{
			Scope:  rel,
			Reason: lifetime.UnknownIndexFailed,
			Detail: "file_too_large",
		}
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, &lifetime.IndexUnknown{
			Scope:  rel,
			Reason: lifetime.UnknownIndexFailed,
			Detail: "read: " + err.Error(),
		}
	}
	text, warnings := stripSource(raw)
	sf := &sourceFile{
		rel:        rel,
		abs:        abs,
		text:       text,
		sha:        sha256Hex(raw),
		lineStarts: computeLineStarts(text),
		warnings:   warnings,
	}
	sf.computeBraces()
	return sf, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func computeLineStarts(text string) []int {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// lineOf 返回 1-based 行号；offset 越界时夹到文件末尾。
func (sf *sourceFile) lineOf(off int) int {
	if off < 0 {
		off = 0
	}
	if off > len(sf.text) {
		off = len(sf.text)
	}
	i := sort.SearchInts(sf.lineStarts, off+1) - 1
	if i < 0 {
		i = 0
	}
	return i + 1
}

func (sf *sourceFile) siteAt(off int) string {
	return fmt.Sprintf("%s:%d", sf.rel, sf.lineOf(off))
}

func (sf *sourceFile) computeBraces() {
	sf.openToClose = make(map[int]int)
	stack := make([]int, 0, 64)
	for i := 0; i < len(sf.text); i++ {
		switch sf.text[i] {
		case '{':
			stack = append(stack, i)
		case '}':
			if len(stack) == 0 {
				continue
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			sf.openToClose[open] = i
			sf.braceOpens = append(sf.braceOpens, open)
		}
	}
	sort.Ints(sf.braceOpens)
}

// matchBrace 返回与 open 处的 '{' 配对的 '}'；找不到返回 -1。
func (sf *sourceFile) matchBrace(open int) int {
	if close, ok := sf.openToClose[open]; ok {
		return close
	}
	return -1
}

// enclosingBrace 返回包含 off 的最内层花括号区间。
func (sf *sourceFile) enclosingBrace(off int) (int, int) {
	i := sort.SearchInts(sf.braceOpens, off+1) - 1
	for i >= 0 {
		open := sf.braceOpens[i]
		if close, ok := sf.openToClose[open]; ok && close > off {
			return open, close
		}
		i--
	}
	return -1, -1
}

// parentBrace 返回严格位于 open 之外的父花括号区间。
func (sf *sourceFile) parentBrace(open int) (int, int) {
	i := sort.SearchInts(sf.braceOpens, open) - 1
	for i >= 0 {
		o := sf.braceOpens[i]
		if close, ok := sf.openToClose[o]; ok && close > open {
			return o, close
		}
		i--
	}
	return -1, -1
}

// functionAt 通过“花括号 + 前缀签名”启发式返回 off 所在函数名。
// 找不到函数（命名空间/类体/全局）时返回空串。
func (sf *sourceFile) functionAt(off int) string {
	open, _ := sf.enclosingBrace(off)
	for open >= 0 {
		prefixStart := open
		for prefixStart > 0 {
			c := sf.text[prefixStart-1]
			if c == ';' || c == '{' || c == '}' {
				break
			}
			prefixStart--
		}
		if name := signatureName(sf.text[prefixStart:open]); name != "" {
			return name
		}
		open, _ = sf.parentBrace(open)
	}
	return ""
}

func (sf *sourceFile) inSpan(off int, spans []span) bool {
	for _, sp := range spans {
		if off >= sp.start && off < sp.end {
			return true
		}
	}
	return false
}

// finalize 排序所有输出集合并计算确定性 index_hash（§8.1/R-8.2）。
func finalize(out *lifetime.IndexOutput, files []*sourceFile) {
	out.IndexQuality = "heuristic"
	if len(out.ObjectFamilies) == 0 {
		out.TypeResolution = "unknown"
	} else {
		out.TypeResolution = "partial"
	}

	for i := range out.ObjectFamilies {
		fam := &out.ObjectFamilies[i]
		fam.ApplicableForms = sortedForms(fam.ApplicableForms)
		if len(fam.ApplicableForms) == 0 {
			// R-4.12 要求空形态显式进入 unknown，但 §4.4 同时要求
			// 启发式索引至少给出候选形态；这里保守回退到 F2（悬垂 this）。
			fam.ApplicableForms = []lifetime.Form{lifetime.FormDanglingThis}
		}
		fam.Domains = uniqueSortedStrings(fam.Domains)
	}

	sort.Slice(out.ObjectFamilies, func(i, j int) bool {
		return out.ObjectFamilies[i].FamilyID < out.ObjectFamilies[j].FamilyID
	})
	sort.Slice(out.ObjectRelations, func(i, j int) bool {
		return out.ObjectRelations[i].RelationID < out.ObjectRelations[j].RelationID
	})
	sort.Slice(out.Domains, func(i, j int) bool {
		return out.Domains[i].DomainClassID < out.Domains[j].DomainClassID
	})
	sort.Slice(out.DomainPairs, func(i, j int) bool {
		return out.DomainPairs[i].PairClassID < out.DomainPairs[j].PairClassID
	})
	sort.Slice(out.LifetimePoints, func(i, j int) bool {
		a, b := out.LifetimePoints[i], out.LifetimePoints[j]
		if a.FamilyID != b.FamilyID {
			return a.FamilyID < b.FamilyID
		}
		if a.Site != b.Site {
			return a.Site < b.Site
		}
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.SourceRef < b.SourceRef
	})
	sort.Slice(out.SyncPrimitives, func(i, j int) bool {
		a, b := out.SyncPrimitives[i], out.SyncPrimitives[j]
		if a.FileLine != b.FileLine {
			return a.FileLine < b.FileLine
		}
		if a.Primitive != b.Primitive {
			return a.Primitive < b.Primitive
		}
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		return a.Protects < b.Protects
	})
	sort.Slice(out.Callbacks, func(i, j int) bool {
		a, b := out.Callbacks[i], out.Callbacks[j]
		if a.RegisterSite != b.RegisterSite {
			return a.RegisterSite < b.RegisterSite
		}
		if a.UnregisterSite != b.UnregisterSite {
			return a.UnregisterSite < b.UnregisterSite
		}
		return a.Capture < b.Capture
	})

	// unknowns 按 scope/reason/detail 排序并去重，保证哈希稳定。
	uniq := make(map[string]lifetime.IndexUnknown, len(out.Unknowns))
	for _, u := range out.Unknowns {
		key := u.Scope + "\x1f" + string(u.Reason) + "\x1f" + u.Detail
		uniq[key] = u
	}
	out.Unknowns = out.Unknowns[:0]
	for _, u := range uniq {
		out.Unknowns = append(out.Unknowns, u)
	}
	sort.Slice(out.Unknowns, func(i, j int) bool {
		a, b := out.Unknowns[i], out.Unknowns[j]
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return a.Detail < b.Detail
	})

	// index_hash = hash(排序后的 relpath:sha256 清单 + 输出事实摘要)。
	manifest := make([]string, 0, len(files))
	for _, sf := range files {
		manifest = append(manifest, sf.rel+":"+sf.sha)
	}
	sort.Strings(manifest)

	digest := *out
	digest.IndexHash = "" // 自身不参与哈希；relations.IndexHash 此时为空
	facts, err := json.Marshal(&digest)
	if err != nil {
		// IndexOutput 仅含字符串/切片/结构体，正常情况下不会失败；
		// 失败时用 %#v 的确定性文本降级，避免静默丢事实。
		facts = []byte(fmt.Sprintf("%#v", digest))
	}
	out.IndexHash = lifetime.CanonicalHash(
		"index_manifest", strings.Join(manifest, "\n"),
		"index_facts", string(facts),
	)
	for i := range out.ObjectRelations {
		out.ObjectRelations[i].IndexHash = out.IndexHash
	}
}

func sortedForms(in []lifetime.Form) []lifetime.Form {
	if len(in) == 0 {
		return nil
	}
	cp := make([]lifetime.Form, len(in))
	copy(cp, in)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	n := 0
	for i, f := range cp {
		if i == 0 || f != cp[i-1] {
			cp[n] = f
			n++
		}
	}
	return cp[:n]
}

func uniqueSortedStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	cp := append([]string(nil), in...)
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
