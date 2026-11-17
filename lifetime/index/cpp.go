package index

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/lifetime"
)

// ---------------------------------------------------------------------------
// 预处理：剥离注释、字符串/字符字面量与 raw string
// ---------------------------------------------------------------------------

// stripSource 用状态机把注释和字面量内容替换为空格，换行与所有偏移量
// 保持不变。这样所有正则都在“干净文本”上运行，而 SourceRef 仍可直接
// 使用原始 file:line（§8.1）。
func stripSource(src []byte) (string, []string) {
	out := make([]byte, len(src))
	copy(out, src)
	var warnings []string

	i := 0
	for i < len(src) {
		c := src[i]

		// 行注释
		if c == '/' && i+1 < len(src) && src[i+1] == '/' {
			j := i
			for j < len(src) && src[j] != '\n' {
				out[j] = ' '
				j++
			}
			i = j
			continue
		}

		// 块注释
		if c == '/' && i+1 < len(src) && src[i+1] == '*' {
			start := i
			out[i] = ' '
			out[i+1] = ' '
			i += 2
			closed := false
			for i < len(src) {
				if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
					out[i] = ' '
					out[i+1] = ' '
					i += 2
					closed = true
					break
				}
				if src[i] != '\n' && src[i] != '\r' {
					out[i] = ' '
				}
				i++
			}
			if !closed {
				warnings = append(warnings, "unterminated_block_comment@"+strconv.Itoa(start))
			}
			continue
		}

		// raw string：R"delim(...)delim"
		if c == 'R' && i+1 < len(src) && src[i+1] == '"' && (i == 0 || !isIdentByte(src[i-1])) {
			j := i + 2
			for j < len(src) && src[j] != '(' && src[j] != '\n' {
				j++
			}
			if j < len(src) && src[j] == '(' {
				delim := string(src[i+2 : j])
				closeSeq := ")" + delim + `"`
				if k := strings.Index(string(src[j+1:]), closeSeq); k >= 0 {
					end := j + 1 + k + len(closeSeq)
					for p := i; p < end && p < len(src); p++ {
						if src[p] != '\n' && src[p] != '\r' {
							out[p] = ' '
						}
					}
					i = end
					continue
				}
			}
			// 不是合法 raw string，按普通标识符 R 处理。
		}

		// 双引号字符串
		if c == '"' {
			start := i
			out[i] = ' '
			i++
			closed := false
			for i < len(src) {
				if src[i] == '\\' {
					if src[i] != '\n' {
						out[i] = ' '
					}
					i++
					if i < len(src) {
						if src[i] != '\n' && src[i] != '\r' {
							out[i] = ' '
						}
						i++
					}
					continue
				}
				if src[i] == '"' {
					out[i] = ' '
					i++
					closed = true
					break
				}
				if src[i] != '\n' && src[i] != '\r' {
					out[i] = ' '
				}
				i++
			}
			if !closed {
				warnings = append(warnings, "unterminated_string@"+strconv.Itoa(start))
			}
			continue
		}

		// 字符字面量；数字分隔符 1'000 不算字符字面量。
		if c == '\'' {
			prev := byte(0)
			if i > 0 {
				prev = src[i-1]
			}
			if isIdentByte(prev) {
				i++
				continue
			}
			start := i
			out[i] = ' '
			i++
			closed := false
			for i < len(src) {
				if src[i] == '\\' {
					out[i] = ' '
					i++
					if i < len(src) {
						out[i] = ' '
						i++
					}
					continue
				}
				if src[i] == '\'' {
					out[i] = ' '
					i++
					closed = true
					break
				}
				if src[i] != '\n' && src[i] != '\r' {
					out[i] = ' '
				}
				i++
			}
			if !closed {
				warnings = append(warnings, "unterminated_char@"+strconv.Itoa(start))
			}
			continue
		}

		i++
	}
	return string(out), warnings
}

// ---------------------------------------------------------------------------
// 低层词法辅助
// ---------------------------------------------------------------------------

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentByte(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func skipSpacesStr(s string, i int) int {
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	return i
}

func wordAt(s string, i int) (string, int) {
	i = skipSpacesStr(s, i)
	if i >= len(s) || !isIdentStart(s[i]) {
		return "", i
	}
	j := i + 1
	for j < len(s) && isIdentByte(s[j]) {
		j++
	}
	return s[i:j], j
}

func precedingWord(text string, off int) string {
	i := off - 1
	for i >= 0 && isSpace(text[i]) {
		i--
	}
	end := i + 1
	for i >= 0 && isIdentByte(text[i]) {
		i--
	}
	return text[i+1 : end]
}

func firstWord(s string) string {
	w, _ := wordAt(s, 0)
	return w
}

func lastWord(s string) string {
	i := len(s) - 1
	for i >= 0 && isSpace(s[i]) {
		i--
	}
	end := i + 1
	for i >= 0 && isIdentByte(s[i]) {
		i--
	}
	return s[i+1 : end]
}

func matchParenStr(s string, open int) int {
	if open < 0 || open >= len(s) || s[open] != '(' {
		return -1
	}
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// matchAngleStr 返回与 open 处 '<' 配对的 '>'。模板参数中的嵌套尖括号
// 会被计入；无法配对时返回 -1。
func matchAngleStr(s string, open int) int {
	if open < 0 || open >= len(s) || s[open] != '<' {
		return -1
	}
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func splitTopLevel(s string, sep byte) []string {
	var parts []string
	start := 0
	angle, paren, bracket := 0, 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			angle++
		case '>':
			if angle > 0 {
				angle--
			}
		case '(':
			paren++
		case ')':
			if paren > 0 {
				paren--
			}
		case '[':
			bracket++
		case ']':
			if bracket > 0 {
				bracket--
			}
		default:
			if s[i] == sep && angle == 0 && paren == 0 && bracket == 0 {
				parts = append(parts, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	parts = append(parts, strings.TrimSpace(s[start:]))
	return parts
}

// hasTopLevelParens 判断括号是否出现在模板实参/下标之外。std::function<void()>
// 的成员字段不是函数声明。
func hasTopLevelParens(s string) bool {
	angle, bracket := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			angle++
		case '>':
			if angle > 0 {
				angle--
			}
		case '[':
			bracket++
		case ']':
			if bracket > 0 {
				bracket--
			}
		case '(':
			if angle == 0 && bracket == 0 {
				return true
			}
		}
	}
	return false
}

func findTopLevelParen(s string) int {
	angle, bracket := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			angle++
		case '>':
			if angle > 0 {
				angle--
			}
		case '[':
			bracket++
		case ']':
			if bracket > 0 {
				bracket--
			}
		case '(':
			if angle == 0 && bracket == 0 {
				return i
			}
		}
	}
	return -1
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func shorten(s string) string {
	const max = 80
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// ---------------------------------------------------------------------------
// 类型规范化与分类
// ---------------------------------------------------------------------------

var (
	reSmartType     = regexp.MustCompile(`^(?:std::)?(shared_ptr|weak_ptr|unique_ptr)\s*<`)
	rePoolType      = regexp.MustCompile(`^(?:std::)?(ObjectPool)\s*<`)
	reHandleType    = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_]*::)?(HandleTable|HandleScope|HandleMap|HandleSet)\s*<`)
	reSingletonType = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_]*::)?Singleton\s*<`)
	reContainerType = regexp.MustCompile(`^(?:std::)?(vector|list|deque|map|set|multimap|multiset|unordered_map|unordered_set|array|span|queue|stack|forward_list)\s*<`)
	reCallbackWord  = regexp.MustCompile(`(?i)(callback|observer|listener|handler|delegate|function|task)`)
	reMacroWord     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

type typeInfo struct {
	canonical string
	form      lifetime.LifetimeForm
	storage   lifetime.StorageClass
	container bool
	callback  bool
}

// classifyType 把字段/变量类型归一化为 type_canonical，并给出可识别的
// 生命周期形式（§4.1/§4.4）。只做模式识别，不做语义解析。
func classifyType(raw string) typeInfo {
	s := stripDeclQualifiers(raw)
	s = trimTypeSuffixes(s)
	if s == "" || hasTopLevelParens(s) {
		return typeInfo{}
	}

	if m := reSmartType.FindStringSubmatchIndex(s); m != nil {
		open := m[1] - 1 // 匹配以 '<' 结尾
		close := matchAngleStr(s, open)
		if close < 0 {
			return typeInfo{}
		}
		inner := normalizeTypeName(s[open+1 : close])
		if inner == "" {
			return typeInfo{}
		}
		ti := typeInfo{canonical: inner}
		switch s[m[2]:m[3]] {
		case "shared_ptr":
			ti.form = lifetime.LifetimeShared
		case "weak_ptr":
			ti.form = lifetime.LifetimeWeak
		default: // unique_ptr
			ti.form = lifetime.LifetimeRaw
		}
		if reCallbackWord.MatchString(typeBase(inner)) {
			ti.callback = true
		}
		return ti
	}

	if m := rePoolType.FindStringSubmatchIndex(s); m != nil {
		inner := templateInnerOr(s, m[1]-1, "ObjectPool")
		return typeInfo{canonical: inner, form: lifetime.LifetimePool, storage: lifetime.StoragePool}
	}
	if m := reHandleType.FindStringSubmatchIndex(s); m != nil {
		inner := templateInnerOr(s, m[1]-1, s[m[2]:m[3]])
		return typeInfo{canonical: inner, form: lifetime.LifetimeHandle, storage: lifetime.StorageHandle}
	}
	if m := reSingletonType.FindStringSubmatchIndex(s); m != nil {
		inner := templateInnerOr(s, m[1]-1, "Singleton")
		return typeInfo{canonical: inner, form: lifetime.LifetimeSingleton, storage: lifetime.StorageStatic}
	}

	ti := typeInfo{canonical: normalizeTypeName(s)}
	if reContainerType.MatchString(s) {
		ti.container = true
	}
	if reCallbackWord.MatchString(typeBase(ti.canonical)) {
		ti.callback = true
	}
	// 无模板的池/句柄类型，例如 v8::HandleScope scope_;
	switch typeBase(ti.canonical) {
	case "ObjectPool":
		ti.storage = lifetime.StoragePool
		ti.form = lifetime.LifetimePool
	case "HandleTable", "HandleScope", "HandleMap", "HandleSet":
		ti.storage = lifetime.StorageHandle
		ti.form = lifetime.LifetimeHandle
	}
	return ti
}

func templateInnerOr(s string, open int, fallback string) string {
	if open >= 0 && open < len(s) && s[open] == '<' {
		if close := matchAngleStr(s, open); close > open {
			if inner := normalizeTypeName(s[open+1 : close]); inner != "" {
				return inner
			}
		}
	}
	return normalizeTypeName(fallback)
}

// stripDeclQualifiers 去掉声明前导限定符、属性与导出宏。
func stripDeclQualifiers(s string) string {
	for {
		t := strings.TrimSpace(s)
		if t == "" {
			return ""
		}
		w := firstWord(t)
		switch w {
		case "mutable", "constexpr", "inline", "static", "thread_local",
			"volatile", "const", "constinit", "register", "extern",
			"typename", "class", "struct", "enum", "union":
			s = strings.TrimSpace(t[len(w):])
			continue
		}
		if strings.HasPrefix(t, "[[") {
			if i := strings.Index(t, "]]"); i >= 0 {
				s = t[i+2:]
				continue
			}
		}
		if strings.HasPrefix(t, "__attribute__") {
			if i := strings.Index(t, "))"); i >= 0 {
				s = t[i+2:]
				continue
			}
		}
		if strings.HasPrefix(t, "alignas") {
			if i := strings.Index(t, ")"); i >= 0 {
				s = t[i+1:]
				continue
			}
		}
		// 导出宏（V8_EXPORT / MY_API 等）；单个模板参数 T 不当作宏。
		if len(w) >= 3 && reMacroWord.MatchString(w) && len(t) > len(w) && isSpace(t[len(w)]) {
			s = strings.TrimSpace(t[len(w):])
			continue
		}
		return t
	}
}

// trimTypeSuffixes 去掉指针/引用/cv 后缀。调用方需要的是对象类型本身。
func trimTypeSuffixes(s string) string {
	s = strings.TrimSpace(s)
	for {
		t := strings.TrimSpace(s)
		changed := false
		for _, suf := range []string{"[]", "*", "&", "const", "volatile"} {
			if strings.HasSuffix(t, suf) {
				t = strings.TrimSpace(strings.TrimSuffix(t, suf))
				changed = true
			}
		}
		s = t
		if !changed {
			return s
		}
	}
}

// normalizeTypeName 去掉命名空间前导 ::、折叠空白并移除标点周围空格。
func normalizeTypeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "::")
	var b strings.Builder
	for i := 0; i < len(s); {
		if isSpace(s[i]) {
			j := i
			for j < len(s) && isSpace(s[j]) {
				j++
			}
			if b.Len() > 0 && j < len(s) && isIdentByte(s[j]) && isIdentByte(b.String()[b.Len()-1]) {
				b.WriteByte(' ')
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return strings.TrimSpace(b.String())
}

// typeBase 返回去掉模板实参与命名空间后的简单类型名。
func typeBase(s string) string {
	s = normalizeTypeName(s)
	if i := strings.Index(s, "<"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "::"); i >= 0 {
		s = s[i+2:]
	}
	return strings.TrimSpace(s)
}

var builtinTypeNames = map[string]bool{
	"void": true, "bool": true, "char": true, "signed": true, "unsigned": true,
	"short": true, "int": true, "long": true, "float": true, "double": true,
	"wchar_t": true, "char8_t": true, "char16_t": true, "char32_t": true,
	"size_t": true, "ssize_t": true, "ptrdiff_t": true, "intptr_t": true,
	"uintptr_t": true, "int8_t": true, "int16_t": true, "int32_t": true,
	"int64_t": true, "uint8_t": true, "uint16_t": true, "uint32_t": true,
	"uint64_t": true, "auto": true, "decltype": true, "nullptr_t": true,
	"byte": true,
}

func isBuiltinType(s string) bool {
	s = normalizeTypeName(s)
	if s == "" {
		return true
	}
	fields := strings.Fields(s)
	if len(fields) > 0 {
		head := fields[0]
		if i := strings.LastIndex(head, "::"); i >= 0 {
			head = head[i+2:]
		}
		switch head {
		case "long", "unsigned", "signed", "short":
			return true
		}
		if builtinTypeNames[head] && len(fields) == 1 {
			return true
		}
	}
	base := typeBase(s)
	return builtinTypeNames[base] && !strings.Contains(s, "<")
}

// looksLikeClassType 用于变量声明映射的保守过滤。
func looksLikeClassType(t string) bool {
	t = stripDeclQualifiers(t)
	if t == "" {
		return false
	}
	if strings.Contains(t, "::") || strings.Contains(t, "<") || strings.HasPrefix(t, "::") {
		return true
	}
	c := t[0]
	return c >= 'A' && c <= 'Z'
}

// ---------------------------------------------------------------------------
// 分析器
// ---------------------------------------------------------------------------

type classInfo struct {
	name     string
	kind     string
	file     *sourceFile
	open     int
	close    int
	line     int
	site     string
	fields   []*fieldDef
	methods  map[string][]string
	refcount lifetime.LifetimeForm
}

type fieldDef struct {
	owner     *classInfo
	name      string
	typeText  string
	storage   lifetime.StorageClass
	form      lifetime.LifetimeForm
	container bool
	callback  bool
	site      string
	familyID  string
}

type familyRecord struct {
	id       string
	key      lifetime.FamilyKey
	file     string
	line     int
	funcName string
	forms    map[lifetime.Form]struct{}
}

type analyzer struct {
	sourceRoot string
	files      []*sourceFile

	classes   map[string]*classInfo
	classList []*classInfo

	families     map[string]*familyRecord
	familyByType map[string][]string

	domains map[string]lifetime.Domain
	pairs   map[string]lifetime.DomainPair

	relations map[string]lifetime.ObjectRelation
	points    map[string]lifetime.LifetimePoint
	syncs     map[string]lifetime.SyncPrimitive
	callbacks map[string]lifetime.CallbackSpec
	unknowns  map[string]lifetime.IndexUnknown

	lazyFiles       map[string]bool
	refcount        map[string]lifetime.LifetimeForm
	memberFamilies  map[string][]string // 字段名 -> family ids
	typesWithFamily map[string]bool     // 已有 family 的 type_canonical/简单名
	singletonTypes  map[string]bool     // 已识别为 static/singleton 的类型
	orderedDomains  []string            // 按启发式相关度排序的 domain_class_id
}

func newAnalyzer(sourceRoot string, files []*sourceFile) *analyzer {
	return &analyzer{
		sourceRoot:      sourceRoot,
		files:           files,
		classes:         make(map[string]*classInfo),
		families:        make(map[string]*familyRecord),
		domains:         make(map[string]lifetime.Domain),
		pairs:           make(map[string]lifetime.DomainPair),
		relations:       make(map[string]lifetime.ObjectRelation),
		points:          make(map[string]lifetime.LifetimePoint),
		syncs:           make(map[string]lifetime.SyncPrimitive),
		callbacks:       make(map[string]lifetime.CallbackSpec),
		unknowns:        make(map[string]lifetime.IndexUnknown),
		lazyFiles:       make(map[string]bool),
		refcount:        make(map[string]lifetime.LifetimeForm),
		memberFamilies:  make(map[string][]string),
		typesWithFamily: make(map[string]bool),
		singletonTypes:  make(map[string]bool),
	}
}

func (a *analyzer) run() *lifetime.IndexOutput {
	a.scanClasses()
	// 即使目标文件里没有 new/make_shared，类定义与 Type::Method 定义
	// 也必须形成 family，否则 Factory::NewXxx 创建的类型会被整体漏掉。
	a.scanDeclarationFamilies()
	a.buildMemberFamilies()
	a.scanAllocations()
	a.scanMethodDefinitions()
	a.indexFamiliesByType()
	a.scanCallbacks()
	a.scanOwnsRelations()
	a.scanLifetimePoints()
	a.scanSyncPrimitives()
	a.scanDomains()
	a.applyLazyInitForms()
	return a.output()
}

func (a *analyzer) output() *lifetime.IndexOutput {
	out := &lifetime.IndexOutput{
		IndexQuality:   "heuristic",
		TypeResolution: "partial",
	}

	domainIDs := append([]string(nil), a.orderedDomains...)
	// 启发式：family 不挂全部域，最多前 4 个（按相关度排序）。完整域表
	// 仍由 output.Domains 与 domain_pairs 提供，避免 bootstrap 覆盖行爆炸。
	if len(domainIDs) > maxFamilyDomains {
		domainIDs = domainIDs[:maxFamilyDomains]
	}

	for _, rec := range a.families {
		forms := make([]lifetime.Form, 0, len(rec.forms))
		for f := range rec.forms {
			forms = append(forms, f)
		}
		out.ObjectFamilies = append(out.ObjectFamilies, lifetime.ObjectFamily{
			FamilyID:           rec.id,
			TypeCanonical:      rec.key.TypeCanonical,
			AllocSite:          rec.key.AllocSite,
			StorageClass:       rec.key.StorageClass,
			LifetimeForm:       rec.key.LifetimeForm,
			MemberPath:         rec.key.MemberPath,
			Domains:            append([]string(nil), domainIDs...),
			ApplicableForms:    forms,
			Confidence:         lifetime.ConfidenceLow,
			IdentityConfidence: lifetime.ConfidenceLow,
		})
	}
	for _, rel := range a.relations {
		out.ObjectRelations = append(out.ObjectRelations, rel)
	}
	for _, d := range a.domains {
		out.Domains = append(out.Domains, d)
	}
	for _, p := range a.pairs {
		out.DomainPairs = append(out.DomainPairs, p)
	}
	for _, lp := range a.points {
		out.LifetimePoints = append(out.LifetimePoints, lp)
	}
	for _, sp := range a.syncs {
		out.SyncPrimitives = append(out.SyncPrimitives, sp)
	}
	for _, cb := range a.callbacks {
		out.Callbacks = append(out.Callbacks, cb)
	}
	for _, u := range a.unknowns {
		out.Unknowns = append(out.Unknowns, u)
	}
	return out
}

func (a *analyzer) addUnknown(scope, detail string) {
	key := scope + "\x1f" + detail
	if _, ok := a.unknowns[key]; ok {
		return
	}
	a.unknowns[key] = lifetime.IndexUnknown{
		Scope:  scope,
		Reason: lifetime.UnknownIndexFailed,
		Detail: detail,
	}
}

func (a *analyzer) applyForm(familyID string, form lifetime.Form) {
	if rec, ok := a.families[familyID]; ok {
		rec.forms[form] = struct{}{}
	}
}

// ---------------------------------------------------------------------------
// 类/结构体发现与成员字段解析
// ---------------------------------------------------------------------------

var (
	reClass      = regexp.MustCompile(`(?m)(?:template\s*<[^{};]*>\s*)?\b(class|struct)\s+(?:[A-Z_][A-Z0-9_]*\s+)*([A-Za-z_][A-Za-z0-9_]*)\s*(?:final\s*)?(?:[:][^;{}]*)?\{`)
	reDeclName   = regexp.MustCompile(`^(.*?)([A-Za-z_][A-Za-z0-9_]*)\s*(?:\[[^\]]*\])?\s*$`)
	reMethodName = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*(?:::[A-Za-z_][A-Za-z0-9_]*)*)\s*$`)
	reParamName  = regexp.MustCompile(`^(.*?)([A-Za-z_][A-Za-z0-9_]*)\s*$`)
	reMacroOnly  = regexp.MustCompile(`(?s)^\s*[A-Z_][A-Z0-9_]*\s*\(.*\)\s*$`)
	reBareName   = regexp.MustCompile(`^[*&\s]*([A-Za-z_][A-Za-z0-9_]*)\s*(?:\[[^\]]*\])?\s*$`)
	reRegister   = regexp.MustCompile(`\b(Register[A-Za-z0-9_]*|AddObserver|SetCallback|AddListener)\s*\(`)
)

func (a *analyzer) scanClasses() {
	for _, sf := range a.files {
		for _, m := range reClass.FindAllStringSubmatchIndex(sf.text, -1) {
			name := sf.text[m[4]:m[5]]
			kind := sf.text[m[2]:m[3]]
			if name == "" || precedingWord(sf.text, m[0]) == "enum" {
				continue
			}
			open := m[1] - 1
			if open < 0 || open >= len(sf.text) || sf.text[open] != '{' {
				continue
			}
			close := sf.matchBrace(open)
			if close < 0 {
				a.addUnknown(sf.rel, "unterminated_class:"+name)
				continue
			}
			sf.classRanges = append(sf.classRanges, span{start: open, end: close + 1})

			header := sf.text[m[0]:open]
			if strings.HasPrefix(strings.TrimSpace(header), "template") {
				a.addUnknown(sf.rel, "template_partial:"+name)
			}
			refForm := detectRefcount(header, sf.text[open+1:close])

			if prev, ok := a.classes[name]; ok {
				// 同名（可能是不同命名空间）只保留首个定义；合并引用计数标记。
				if refForm != "" && prev.refcount == "" {
					prev.refcount = refForm
					a.noteRefcount(name, refForm)
				}
				continue
			}

			line := sf.lineOf(m[4])
			cd := &classInfo{
				name:     name,
				kind:     kind,
				file:     sf,
				open:     open,
				close:    close,
				line:     line,
				site:     fmt.Sprintf("%s:%d", sf.rel, line),
				methods:  make(map[string][]string),
				refcount: refForm,
			}
			if refForm != "" {
				a.noteRefcount(name, refForm)
			}
			a.classes[name] = cd
			a.classList = append(a.classList, cd)
			a.parseClassBody(cd)
		}
	}
}

func (a *analyzer) noteRefcount(name string, form lifetime.LifetimeForm) {
	a.refcount[name] = form
	a.refcount[normalizeTypeName(name)] = form
}

// scanDeclarationFamilies 为每个 class/struct 定义生成一个对象族。
// 目标代码常通过 Factory::NewXxx 创建对象，索引到的文件里可能只有类型
// 定义或方法实现而没有 new；若不生成声明 family，这些类型会整体漏掉。
// 顶层类按 heap/raw 处理，类内嵌套类型按 member 处理（member_path=类型名）。
func (a *analyzer) scanDeclarationFamilies() {
	for _, cd := range a.classList {
		if a.hasFamilyAtSite(cd.name, cd.site) {
			continue
		}
		storage := lifetime.StorageHeap
		memberPath := ""
		if a.enclosingClass(cd) != nil {
			storage = lifetime.StorageMember
			memberPath = cd.name
		}
		rec := a.addFamily(cd.file, cd.name, storage, lifetime.LifetimeRaw, memberPath,
			cd.line, "", lifetime.FormDanglingThis, lifetime.FormContainerInvalid)
		if rec != nil {
			a.addPoint(rec.id, cd.site)
		}
	}
}

// enclosingClass 返回直接或间接包含 cd 的最内层类定义。
func (a *analyzer) enclosingClass(cd *classInfo) *classInfo {
	var best *classInfo
	bestSpan := 1 << 30
	for _, other := range a.classList {
		if other == cd || other.file != cd.file {
			continue
		}
		if other.open < cd.open && other.close > cd.close {
			if sp := other.close - other.open; sp < bestSpan {
				best = other
				bestSpan = sp
			}
		}
	}
	return best
}

var reQualifiedMethod = regexp.MustCompile(`(?m)\b([A-Za-z_][A-Za-z0-9_]*(?:::[A-Za-z_][A-Za-z0-9_]*)*)\s*::\s*(~?[A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// scanMethodDefinitions 扫描类外方法定义 `Name::Method(`。若该 Name 尚未
// 出现在任何 family 中（典型：类声明在未索引的头文件里），则以首个方法
// 定义 site 为 alloc_site 生成 heap/raw family，形态至少含 F2/F4。
func (a *analyzer) scanMethodDefinitions() {
	for _, sf := range a.files {
		text := sf.text
		for _, m := range reQualifiedMethod.FindAllStringSubmatchIndex(text, -1) {
			prefix := text[m[2]:m[3]]
			open := m[1] - 1
			if open < 0 || open >= len(text) || text[open] != '(' {
				continue
			}
			close := matchParenStr(text, open)
			if close < 0 || !isMethodDefinitionTail(text, close+1) {
				continue // 声明/调用，不是定义
			}
			base := typeBase(prefix)
			if base == "" || isBuiltinType(base) || isKeywordName(base) {
				continue
			}
			if a.hasAnyFamilyOfType(base) {
				continue
			}
			if a.hasFamilyAtSite(base, sf.siteAt(m[0])) {
				continue
			}
			rec := a.addFamily(sf, base, lifetime.StorageHeap, lifetime.LifetimeRaw, "",
				sf.lineOf(m[0]), sf.functionAt(m[0]),
				lifetime.FormDanglingThis, lifetime.FormContainerInvalid)
			if rec != nil {
				a.addPoint(rec.id, sf.siteAt(m[0]))
			}
		}
	}
}

// isMethodDefinitionTail 判断参数列表之后是否紧跟函数体（含构造初始化
// 列表与尾置返回类型），以此区分定义与声明/调用。
func isMethodDefinitionTail(text string, pos int) bool {
	for i := 0; i < 8; i++ {
		pos = skipSpacesStr(text, pos)
		if pos >= len(text) {
			return false
		}
		switch text[pos] {
		case '{':
			return true
		case ':':
			if pos+1 >= len(text) || text[pos+1] != ':' {
				return true // 构造初始化列表
			}
			return false
		case '&':
			pos++
			continue
		case '-':
			if pos+1 < len(text) && text[pos+1] == '>' {
				for j := pos + 2; j < len(text) && j < pos+240; j++ {
					if text[j] == '{' {
						return true
					}
					if text[j] == ';' {
						return false
					}
				}
			}
			return false
		}
		w, end := wordAt(text, pos)
		switch w {
		case "const", "noexcept", "override", "final", "volatile", "mutable", "constexpr", "throw":
			pos = end
			if (w == "noexcept" || w == "throw") && pos < len(text) && text[pos] == '(' {
				if cl := matchParenStr(text, pos); cl >= 0 {
					pos = cl + 1
				}
			}
			continue
		}
		return false
	}
	return false
}

// detectRefcount 从类头/类体识别手工引用计数与 intrusive 形态（§4.1/§4.4）。
func detectRefcount(header, body string) lifetime.LifetimeForm {
	if strings.Contains(header, "Intrusive") || strings.Contains(body, "intrusive") {
		return lifetime.LifetimeIntrusive
	}
	if strings.Contains(header, "RefCounted") || strings.Contains(header, "RefCount") ||
		strings.Contains(body, "AddRef") || strings.Contains(body, "Release(") ||
		strings.Contains(body, "ref_count_") || strings.Contains(body, "refcount_") {
		return lifetime.LifetimeRefcount
	}
	return ""
}

func (a *analyzer) parseClassBody(cd *classInfo) {
	text := cd.file.text
	segStart := cd.open + 1
	depth := 0
	for i := cd.open + 1; i < cd.close; i++ {
		switch text[i] {
		case '{':
			if depth == 0 {
				prefix := strings.TrimSpace(text[segStart:i])
				if isBlockOpener(prefix) {
					depth++
				} else {
					// 花括号初始化器：跳过括号内容，segment 继续到 ';'
					bc := cd.file.matchBrace(i)
					if bc < 0 || bc >= cd.close {
						i = cd.close
						continue
					}
					i = bc
				}
			} else {
				depth++
			}
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 {
					segStart = i + 1
				}
			} else {
				segStart = i + 1
			}
		case ';':
			if depth == 0 {
				a.handleClassSegment(cd, segStart, i)
				segStart = i + 1
			}
		case ':':
			if depth == 0 && isAccessSpecifier(text, segStart, i) {
				segStart = i + 1
			}
		case '\n':
			if depth == 0 && strings.HasPrefix(strings.TrimSpace(text[segStart:i]), "#") {
				a.handleClassSegment(cd, segStart, i)
				segStart = i + 1
			}
		}
	}
}

func isBlockOpener(prefix string) bool {
	p := strings.TrimSpace(prefix)
	if p == "" {
		return true
	}
	if hasTopLevelParens(p) {
		return true // 方法/构造/嵌套类型定义
	}
	switch firstWord(p) {
	case "class", "struct", "union", "enum", "namespace", "extern", "template":
		return true
	}
	return false
}

func isAccessSpecifier(text string, segStart, colon int) bool {
	j := colon - 1
	for j >= segStart && isSpace(text[j]) {
		j--
	}
	k := j
	for k >= segStart && isIdentByte(text[k]) {
		k--
	}
	word := text[k+1 : j+1]
	switch word {
	case "public", "private", "protected", "signals", "slots", "Q_SIGNALS", "Q_SLOTS":
		return strings.TrimSpace(text[segStart:k+1]) == ""
	}
	return false
}

func isAccessSpecifierSegment(seg string) bool {
	switch strings.TrimSuffix(strings.TrimSpace(seg), ":") {
	case "public", "private", "protected", "signals", "slots", "Q_SIGNALS", "Q_SLOTS":
		return true
	}
	return false
}

func isSkippedDeclKeyword(seg string) bool {
	w := firstWord(seg)
	switch w {
	case "using", "typedef", "friend", "static_assert", "enum", "namespace",
		"template", "return", "goto", "break", "continue", "public", "private",
		"protected", "signals", "slots", "Q_OBJECT":
		return true
	}
	return false
}

func (a *analyzer) handleClassSegment(cd *classInfo, start, end int) {
	raw := cd.file.text[start:end]
	seg := strings.TrimSpace(raw)
	if seg == "" {
		return
	}
	if strings.HasPrefix(seg, "#") {
		a.addUnknown(cd.file.rel, "macro_member:"+cd.name+":"+shorten(collapseSpace(seg)))
		return
	}
	if isAccessSpecifierSegment(seg) {
		return
	}
	if hasTopLevelParens(seg) {
		if reMacroOnly.MatchString(seg) {
			a.addUnknown(cd.file.rel, "macro_member:"+cd.name+":"+shorten(collapseSpace(seg)))
			return
		}
		if name, params, ok := parseMethodSignature(seg, cd.name); ok {
			cd.methods[name] = params
		}
		return
	}
	if isSkippedDeclKeyword(seg) {
		return
	}
	if reMacroWord.MatchString(strings.TrimSpace(seg)) {
		a.addUnknown(cd.file.rel, "macro_member:"+cd.name+":"+shorten(seg))
		return
	}

	// 记录成员声明区间，供 shared_ptr/weak_ptr/static/pool 的全局扫描跳过，
	// 避免同一字段同时产生 member 与 heap/static family。
	lead := 0
	for lead < len(raw) && isSpace(raw[lead]) {
		lead++
	}
	cd.file.memberRanges = append(cd.file.memberRanges, span{start: start + lead, end: end})

	line := cd.file.lineOf(start + lead)
	for _, d := range parseFieldDeclarators(seg) {
		info := classifyType(d.typeText)
		if info.canonical == "" || isBuiltinType(info.canonical) {
			continue
		}
		storage := info.storage
		if storage == "" {
			storage = lifetime.StorageMember
		}
		form := info.form
		if form == "" {
			form = lifetime.LifetimeRaw
		}
		cd.fields = append(cd.fields, &fieldDef{
			owner:     cd,
			name:      d.name,
			typeText:  info.canonical,
			storage:   storage,
			form:      form,
			container: info.container,
			callback:  info.callback,
			site:      fmt.Sprintf("%s:%d", cd.file.rel, line),
		})
	}
}

type fieldDecl struct {
	typeText string
	name     string
}

func parseFieldDeclarators(seg string) []fieldDecl {
	parts := splitTopLevel(seg, ',')
	if len(parts) == 0 {
		return nil
	}
	firstType, firstName, ok := parseOneDeclarator(parts[0])
	if !ok || firstName == "" {
		return nil
	}
	out := []fieldDecl{{typeText: firstType, name: firstName}}
	for _, p := range parts[1:] {
		name := parseBareDeclarator(p)
		if name == "" {
			continue
		}
		out = append(out, fieldDecl{typeText: firstType, name: name})
	}
	return out
}

func parseOneDeclarator(part string) (string, string, bool) {
	s := stripDeclInitializer(part)
	s = stripDeclQualifiers(s)
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	m := reDeclName.FindStringSubmatch(s)
	if m == nil {
		return "", "", false
	}
	typeText := strings.TrimSpace(m[1])
	name := m[2]
	if typeText == "" || name == "" || isKeywordName(name) {
		return "", "", false
	}
	if hasTopLevelParens(typeText) || strings.HasSuffix(typeText, "::") {
		return "", "", false
	}
	return typeText, name, true
}

func parseBareDeclarator(part string) string {
	s := stripDeclInitializer(part)
	m := reBareName.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || isKeywordName(m[1]) {
		return ""
	}
	return m[1]
}

// stripDeclInitializer 去掉初始化器与位域，只留下 "type name"。
func stripDeclInitializer(s string) string {
	angle, paren, bracket := 0, 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			angle++
		case '>':
			if angle > 0 {
				angle--
			}
		case '(':
			paren++
		case ')':
			if paren > 0 {
				paren--
			}
		case '[':
			bracket++
		case ']':
			if bracket > 0 {
				bracket--
			}
		case '{':
			if angle == 0 && paren == 0 && bracket == 0 {
				return s[:i]
			}
		case '=':
			if angle == 0 && paren == 0 && bracket == 0 && !isComparisonAssign(s, i) {
				return s[:i]
			}
		case ':':
			if angle == 0 && paren == 0 && bracket == 0 && !isScopeColon(s, i) {
				return s[:i]
			}
		}
	}
	return s
}

func isComparisonAssign(s string, i int) bool {
	prev, next := byte(0), byte(0)
	if i > 0 {
		prev = s[i-1]
	}
	if i+1 < len(s) {
		next = s[i+1]
	}
	return prev == '=' || prev == '!' || prev == '<' || prev == '>' || next == '='
}

func isScopeColon(s string, i int) bool {
	prev, next := byte(0), byte(0)
	if i > 0 {
		prev = s[i-1]
	}
	if i+1 < len(s) {
		next = s[i+1]
	}
	return prev == ':' || next == ':'
}

func parseMethodSignature(seg, ownerName string) (string, []string, bool) {
	s := strings.TrimSpace(seg)
	if s == "" || strings.Contains(s, "operator") {
		return "", nil, false
	}
	idx := findTopLevelParen(s)
	if idx < 0 {
		return "", nil, false
	}
	close := matchParenStr(s, idx)
	if close < 0 {
		return "", nil, false
	}
	head := strings.TrimSpace(s[:idx])
	loc := reMethodName.FindStringIndex(head)
	if loc == nil {
		return "", nil, false
	}
	name := head[loc[0]:loc[1]]
	prefix := strings.TrimSpace(head[:loc[0]])
	if strings.HasSuffix(prefix, ".") || strings.HasSuffix(prefix, "->") {
		return "", nil, false
	}
	if prefix != "" && isControlKeyword(lastWord(prefix)) {
		return "", nil, false
	}
	if prefix == "" && name != ownerName {
		// 无返回类型的普通调用，不当作方法声明。
		return "", nil, false
	}
	return name, parseParamTypes(s[idx+1 : close]), true
}

func parseParamTypes(params string) []string {
	parts := splitTopLevel(params, ',')
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "void" || p == "..." {
			continue
		}
		// 去掉默认实参
		if i := topLevelByte(p, '='); i >= 0 {
			p = strings.TrimSpace(p[:i])
		}
		if m := reParamName.FindStringSubmatch(p); m != nil {
			prefix := strings.TrimSpace(m[1])
			if prefix != "" && !strings.HasSuffix(prefix, "::") {
				p = prefix
			}
		}
		info := classifyType(p)
		if info.canonical == "" || isBuiltinType(info.canonical) {
			continue
		}
		if !seen[info.canonical] {
			seen[info.canonical] = true
			out = append(out, info.canonical)
		}
	}
	return out
}

func topLevelByte(s string, want byte) int {
	angle, paren, bracket := 0, 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			angle++
		case '>':
			if angle > 0 {
				angle--
			}
		case '(':
			paren++
		case ')':
			if paren > 0 {
				paren--
			}
		case '[':
			bracket++
		case ']':
			if bracket > 0 {
				bracket--
			}
		default:
			if s[i] == want && angle == 0 && paren == 0 && bracket == 0 {
				return i
			}
		}
	}
	return -1
}

func isControlKeyword(w string) bool {
	switch w {
	case "if", "for", "while", "switch", "catch", "return", "sizeof",
		"decltype", "alignof", "static_assert", "do", "else":
		return true
	}
	return false
}

func isKeywordName(w string) bool {
	switch w {
	case "if", "for", "while", "switch", "return", "new", "delete", "sizeof",
		"const", "static", "void", "int", "char", "float", "double", "bool",
		"class", "struct", "enum", "union", "namespace", "using", "typedef",
		"public", "private", "protected", "virtual", "override", "final",
		"true", "false", "nullptr", "this":
		return true
	}
	return false
}

// signatureName 从花括号前的前缀文本中提取函数名。只接受“限定名 + 参数
// 列表 + 合法尾部（const/override/初始化列表等）”的形状，并排除控制流。
func signatureName(prefix string) string {
	depth := 0
	i := 0
	for i < len(prefix) {
		c := prefix[i]
		if c == '(' {
			depth++
			i++
			continue
		}
		if c == ')' {
			if depth > 0 {
				depth--
			}
			i++
			continue
		}
		if depth == 0 && isIdentStart(c) {
			prev := byte(0)
			if i > 0 {
				prev = prefix[i-1]
			}
			if !(isIdentByte(prev) || prev == ':' || prev == '.' || prev == '>' || prev == '~') {
				end := scanQualifiedName(prefix, i)
				k := skipSpacesStr(prefix, end)
				if k < len(prefix) && prefix[k] == '(' {
					if close := matchParenStr(prefix, k); close > 0 {
						name := prefix[i:end]
						if !isControlKeyword(baseSignatureName(name)) && isSignatureTail(prefix, close+1) {
							return name
						}
						i = close + 1
						continue
					}
				}
				i = end
				continue
			}
		}
		i++
	}
	return ""
}

func baseSignatureName(name string) string {
	if i := strings.LastIndex(name, "::"); i >= 0 {
		return name[i+2:]
	}
	return name
}

func scanQualifiedName(s string, i int) int {
	if i < len(s) && s[i] == '~' {
		i++
	}
	if i >= len(s) || !isIdentStart(s[i]) {
		return i
	}
	for i < len(s) && isIdentByte(s[i]) {
		i++
	}
	for i+1 < len(s) && s[i] == ':' && s[i+1] == ':' {
		i += 2
		if i < len(s) && s[i] == '~' {
			i++
		}
		if i >= len(s) || !isIdentStart(s[i]) {
			break
		}
		for i < len(s) && isIdentByte(s[i]) {
			i++
		}
	}
	return i
}

func isSignatureTail(s string, i int) bool {
	for n := 0; n < 12; n++ {
		i = skipSpacesStr(s, i)
		if i >= len(s) {
			return true
		}
		c := s[i]
		switch c {
		case '{':
			return true
		case ':':
			if i+1 >= len(s) || s[i+1] != ':' {
				return true // 构造初始化列表
			}
			return false
		case '&':
			i++
			continue
		case '-':
			if i+1 < len(s) && s[i+1] == '>' {
				return true // 尾置返回类型
			}
			return false
		}
		w, end := wordAt(s, i)
		switch w {
		case "const", "noexcept", "override", "final", "volatile", "mutable", "constexpr", "throw":
			i = end
			if (w == "noexcept" || w == "throw") && i < len(s) && s[i] == '(' {
				if cl := matchParenStr(s, i); cl >= 0 {
					i = cl + 1
				}
			}
			continue
		}
		return false
	}
	return false
}

// ---------------------------------------------------------------------------
// 对象族构建
// ---------------------------------------------------------------------------

func (a *analyzer) buildMemberFamilies() {
	for _, cd := range a.classList {
		for _, fd := range cd.fields {
			form := fd.form
			if form == "" || form == lifetime.LifetimeRaw {
				if lf, ok := a.refcountForm(fd.typeText); ok {
					form = lf
				}
			}
			forms := memberForms(fd, form)
			rec := a.addFamily(cd.file, fd.typeText, fd.storage, form, fd.name, cd.line, "", forms...)
			if rec == nil {
				continue
			}
			fd.familyID = rec.id
			a.addPoint(rec.id, cd.site)
			a.memberFamilies[fd.name] = append(a.memberFamilies[fd.name], rec.id)
		}
	}
}

func memberForms(fd *fieldDef, form lifetime.LifetimeForm) []lifetime.Form {
	var out []lifetime.Form
	switch form {
	case lifetime.LifetimeShared, lifetime.LifetimeWeak:
		out = append(out, lifetime.FormSharedPtrRace)
	case lifetime.LifetimeRefcount, lifetime.LifetimeIntrusive:
		out = append(out, lifetime.FormRefcountRace)
	}
	switch fd.storage {
	case lifetime.StoragePool, lifetime.StorageHandle:
		out = append(out, lifetime.FormPoolABA)
	}
	if fd.container {
		out = append(out, lifetime.FormContainerInvalid)
	}
	if fd.callback {
		out = append(out, lifetime.FormDanglingThis)
	}
	if len(out) == 0 {
		out = append(out, lifetime.FormDanglingThis)
	}
	return out
}

func (a *analyzer) addFamily(sf *sourceFile, typeName string, storage lifetime.StorageClass,
	form lifetime.LifetimeForm, memberPath string, line int, funcName string, forms ...lifetime.Form) *familyRecord {
	canon := normalizeTypeName(typeName)
	if canon == "" || isBuiltinType(canon) {
		return nil
	}
	if storage == lifetime.StorageMember && memberPath == "" {
		return nil
	}
	if form == "" {
		form = lifetime.LifetimeRaw
	}
	alloc := fmt.Sprintf("%s:%d", sf.rel, line)
	if funcName != "" && storage != lifetime.StorageMember {
		alloc += ":" + funcName
	}
	key := lifetime.FamilyKey{
		TypeCanonical: canon,
		AllocSite:     alloc,
		StorageClass:  storage,
		LifetimeForm:  form,
		MemberPath:    memberPath,
	}
	id := lifetime.FamilyID(key)
	if rec, ok := a.families[id]; ok {
		for _, f := range forms {
			rec.forms[f] = struct{}{}
		}
		return rec
	}
	rec := &familyRecord{
		id:       id,
		key:      key,
		file:     sf.rel,
		line:     line,
		funcName: funcName,
		forms:    make(map[lifetime.Form]struct{}),
	}
	for _, f := range forms {
		rec.forms[f] = struct{}{}
	}
	a.families[id] = rec
	a.typesWithFamily[canon] = true
	if base := typeBase(canon); base != canon {
		a.typesWithFamily[base] = true
	}
	if form == lifetime.LifetimeSingleton {
		a.singletonTypes[canon] = true
		a.singletonTypes[typeBase(canon)] = true
	}
	return rec
}

func (a *analyzer) hasSingletonType(typeName string) bool {
	t := normalizeTypeName(typeName)
	return a.singletonTypes[t] || a.singletonTypes[typeBase(t)]
}

func (a *analyzer) hasAnyFamilyOfType(typeName string) bool {
	t := normalizeTypeName(typeName)
	if a.typesWithFamily[t] || a.typesWithFamily[typeBase(t)] {
		return true
	}
	return false
}

// hasFamilyAtSite 实现“同 type + alloc_site 只留一个”。
func (a *analyzer) hasFamilyAtSite(typeName, site string) bool {
	canon := normalizeTypeName(typeName)
	for _, rec := range a.families {
		if rec.key.TypeCanonical == canon && rec.key.AllocSite == site {
			return true
		}
	}
	return false
}

func (a *analyzer) refcountForm(typeName string) (lifetime.LifetimeForm, bool) {
	t := normalizeTypeName(typeName)
	if f, ok := a.refcount[t]; ok {
		return f, true
	}
	if f, ok := a.refcount[typeBase(t)]; ok {
		return f, true
	}
	return "", false
}

func (a *analyzer) indexFamiliesByType() {
	a.familyByType = make(map[string][]string)
	for id, rec := range a.families {
		canon := rec.key.TypeCanonical
		a.familyByType[canon] = append(a.familyByType[canon], id)
		if base := typeBase(canon); base != canon {
			a.familyByType[base] = append(a.familyByType[base], id)
		}
	}
	for k := range a.familyByType {
		a.familyByType[k] = uniqueSortedStrings(a.familyByType[k])
	}
}

func (a *analyzer) firstFamilyOfType(typeName string) string {
	ids := a.familyIDsOfType(typeName, "")
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func (a *analyzer) familyIDsOfType(typeName string, preferForm lifetime.LifetimeForm) []string {
	t := normalizeTypeName(typeName)
	if t == "" {
		return nil
	}
	ids := append([]string(nil), a.familyByType[t]...)
	if base := typeBase(t); base != t {
		ids = append(ids, a.familyByType[base]...)
	}
	ids = uniqueSortedStrings(ids)
	if len(ids) < 2 {
		return ids
	}
	sort.SliceStable(ids, func(i, j int) bool {
		ri, rj := a.families[ids[i]], a.families[ids[j]]
		if preferForm != "" {
			pi := ri.key.LifetimeForm == preferForm
			pj := rj.key.LifetimeForm == preferForm
			if pi != pj {
				return pi
			}
		}
		ri2, rj2 := storageRank(ri.key.StorageClass), storageRank(rj.key.StorageClass)
		if ri2 != rj2 {
			return ri2 < rj2
		}
		return ids[i] < ids[j]
	})
	return ids
}

func storageRank(s lifetime.StorageClass) int {
	switch s {
	case lifetime.StorageHeap:
		return 0
	case lifetime.StoragePool:
		return 1
	case lifetime.StorageHandle:
		return 2
	case lifetime.StorageStatic:
		return 3
	case lifetime.StorageStack:
		return 4
	case lifetime.StorageMember:
		return 5
	}
	return 6
}

func (a *analyzer) addPoint(familyID, site string) {
	if familyID == "" || site == "" {
		return
	}
	key := familyID + "\x1f" + site + "\x1f" + string(lifetime.NodeLifetimePoint)
	a.points[key] = lifetime.LifetimePoint{
		FamilyID:  familyID,
		Site:      site,
		Role:      lifetime.NodeLifetimePoint,
		SourceRef: site,
	}
}

func (a *analyzer) pointsForType(typeName, site string, preferForm lifetime.LifetimeForm) {
	for _, id := range a.familyIDsOfType(typeName, preferForm) {
		a.addPoint(id, site)
	}
}

// ---------------------------------------------------------------------------
// 分配站点扫描：new / shared_ptr / static / GetInstance / Singleton / pool
// ---------------------------------------------------------------------------

var (
	reNew       = regexp.MustCompile(`\bnew\b`)
	reSmart     = regexp.MustCompile(`\b(?:std::)?(make_shared|make_unique|shared_ptr|weak_ptr|unique_ptr)\b`)
	reStatic    = regexp.MustCompile(`\bstatic\b`)
	reGetInst   = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*(?:::[A-Za-z_][A-Za-z0-9_]*)*)\s*::\s*GetInstance\s*\(`)
	reSingleton = regexp.MustCompile(`\bSingleton\s*<`)
	rePool      = regexp.MustCompile(`\b(ObjectPool|HandleTable|HandleScope|HandleMap|HandleSet)\b`)
	reLazy      = regexp.MustCompile(`\b(?:std::)?(?:call_once|once_flag)\b|\bpthread_once\b`)
)

func (a *analyzer) scanAllocations() {
	for _, sf := range a.files {
		a.scanNew(sf)
		a.scanSmartPointers(sf)
		a.scanStatics(sf)
		a.scanGetInstance(sf)
		a.scanSingleton(sf)
		a.scanPools(sf)
		if reLazy.MatchString(sf.text) {
			a.lazyFiles[sf.rel] = true
		}
	}
}

func (a *analyzer) scanNew(sf *sourceFile) {
	text := sf.text
	for _, m := range reNew.FindAllStringIndex(text, -1) {
		pos := skipSpacesStr(text, m[1])
		// placement new: new (ptr) Type(...)
		if pos < len(text) && text[pos] == '(' {
			close := matchParenStr(text, pos)
			if close < 0 {
				continue
			}
			pos = skipSpacesStr(text, close+1)
		}
		typeStart := pos
		typeName, end, ok := parseTypeExpr(text, pos)
		if !ok {
			continue
		}
		pos = skipSpacesStr(text, end)
		if pos >= len(text) || (text[pos] != '(' && text[pos] != '[' && text[pos] != '{') {
			continue
		}
		canon := normalizeTypeName(typeName)
		if canon == "" || isBuiltinType(canon) {
			continue
		}
		form := lifetime.LifetimeRaw
		forms := []lifetime.Form{lifetime.FormDanglingThis}
		// §4.1 明确 new -> heap/raw；若类型被识别为手工引用计数，
		// 只增加 F1 形态，不改变分配点的 lifetime_form。
		if lf, ok := a.refcountForm(canon); ok &&
			(lf == lifetime.LifetimeRefcount || lf == lifetime.LifetimeIntrusive) {
			forms = append(forms, lifetime.FormRefcountRace)
		}
		rec := a.addFamily(sf, canon, lifetime.StorageHeap, form, "", sf.lineOf(typeStart), sf.functionAt(typeStart), forms...)
		if rec != nil {
			a.addPoint(rec.id, sf.siteAt(typeStart))
		}
	}
}

// parseTypeExpr 从 pos 解析一个限定/模板类型表达式，返回类型文本与结束位置。
func parseTypeExpr(text string, pos int) (string, int, bool) {
	pos = skipSpacesStr(text, pos)
	start := pos
	if pos+1 < len(text) && text[pos] == ':' && text[pos+1] == ':' {
		pos += 2
	}
	first := true
	for {
		if pos >= len(text) || !isIdentStart(text[pos]) {
			if first {
				return "", 0, false
			}
			break
		}
		first = false
		for pos < len(text) && isIdentByte(text[pos]) {
			pos++
		}
		p := skipSpacesStr(text, pos)
		if p < len(text) && text[p] == '<' {
			close := matchAngleStr(text, p)
			if close < 0 {
				return "", 0, false
			}
			pos = close + 1
			p = skipSpacesStr(text, pos)
		}
		if p+1 < len(text) && text[p] == ':' && text[p+1] == ':' {
			pos = skipSpacesStr(text, p+2)
			continue
		}
		pos = p
		break
	}
	if pos <= start {
		return "", 0, false
	}
	return text[start:pos], pos, true
}

func (a *analyzer) scanSmartPointers(sf *sourceFile) {
	text := sf.text
	for _, m := range reSmart.FindAllStringSubmatchIndex(text, -1) {
		if sf.inSpan(m[0], sf.memberRanges) {
			continue // 成员字段由 member family 负责
		}
		kind := text[m[2]:m[3]]
		pos := skipSpacesStr(text, m[1])
		if pos >= len(text) || text[pos] != '<' {
			continue
		}
		close := matchAngleStr(text, pos)
		if close < 0 {
			a.addUnknown(sf.rel, "template_unparsed:"+kind)
			continue
		}
		inner := normalizeTypeName(text[pos+1 : close])
		if inner == "" || isBuiltinType(inner) {
			continue
		}
		after := skipSpacesStr(text, close+1)

		if kind == "shared_ptr" || kind == "weak_ptr" || kind == "unique_ptr" {
			if after < len(text) && text[after] == '(' {
				continue // 函数返回类型
			}
			if after < len(text) && isIdentStart(text[after]) {
				idEnd := after
				for idEnd < len(text) && isIdentByte(text[idEnd]) {
					idEnd++
				}
				next := skipSpacesStr(text, idEnd)
				if next < len(text) && text[next] == '(' {
					continue // 函数声明返回类型
				}
			}
		}

		var form lifetime.LifetimeForm
		switch kind {
		case "make_shared", "shared_ptr":
			form = lifetime.LifetimeShared
		case "weak_ptr":
			form = lifetime.LifetimeWeak
		default: // make_unique / unique_ptr
			form = lifetime.LifetimeRaw
		}
		var forms []lifetime.Form
		if form == lifetime.LifetimeShared || form == lifetime.LifetimeWeak {
			forms = append(forms, lifetime.FormSharedPtrRace)
		} else {
			forms = append(forms, lifetime.FormDanglingThis)
		}
		rec := a.addFamily(sf, inner, lifetime.StorageHeap, form, "", sf.lineOf(m[0]), sf.functionAt(m[0]), forms...)
		if rec != nil {
			a.addPoint(rec.id, sf.siteAt(m[0]))
		}
	}
}

func (a *analyzer) scanStatics(sf *sourceFile) {
	text := sf.text
	for _, m := range reStatic.FindAllStringIndex(text, -1) {
		if a.isDirectClassMember(sf, m[0]) {
			continue // 类内静态成员按 §4.1 归入 member family
		}
		pos := skipSpacesStr(text, m[1])
		for {
			w, end := wordAt(text, pos)
			switch w {
			case "const", "constexpr", "inline", "volatile", "thread_local", "mutable", "constinit", "register":
				pos = skipSpacesStr(text, end)
				continue
			}
			break
		}
		if pos >= len(text) {
			continue
		}
		if w, _ := wordAt(text, pos); w == "class" || w == "struct" || w == "union" || w == "enum" {
			continue
		}
		typeStart := pos
		typeName, end, ok := parseTypeExpr(text, pos)
		if !ok {
			continue
		}
		pos = skipSpacesStr(text, end)
		for pos < len(text) && (text[pos] == '*' || text[pos] == '&') {
			pos = skipSpacesStr(text, pos+1)
		}
		if pos >= len(text) || !isIdentStart(text[pos]) {
			continue
		}
		nameEnd := pos
		for nameEnd < len(text) && isIdentByte(text[nameEnd]) {
			nameEnd++
		}
		after := skipSpacesStr(text, nameEnd)
		if after < len(text) && text[after] == '(' {
			continue // static 函数
		}
		info := classifyType(typeName)
		if info.canonical == "" || isBuiltinType(info.canonical) {
			continue
		}
		storage := lifetime.StorageStatic
		form := lifetime.LifetimeSingleton
		forms := []lifetime.Form{lifetime.FormStaticDestroy, lifetime.FormLazyInitRace}
		if info.storage == lifetime.StoragePool || info.storage == lifetime.StorageHandle {
			storage = info.storage
			form = info.form
			forms = []lifetime.Form{lifetime.FormPoolABA}
		}
		rec := a.addFamily(sf, info.canonical, storage, form, "", sf.lineOf(typeStart), sf.functionAt(typeStart), forms...)
		if rec != nil {
			a.addPoint(rec.id, sf.siteAt(typeStart))
		}
	}
}

func (a *analyzer) scanGetInstance(sf *sourceFile) {
	text := sf.text
	for _, m := range reGetInst.FindAllStringSubmatchIndex(text, -1) {
		typeName := text[m[2]:m[3]]
		pos := skipSpacesStr(text, m[1])
		if pos >= len(text) || text[pos] != '(' {
			continue
		}
		if matchParenStr(text, pos) < 0 {
			continue
		}
		canon := normalizeTypeName(typeName)
		if canon == "" || isBuiltinType(canon) {
			continue
		}
		// Type::GetInstance 既可能是定义也可能是调用点；按类型去重，
		// 首个出现位置作为启发式 alloc_site。
		if a.hasSingletonType(canon) {
			continue
		}
		rec := a.addFamily(sf, canon, lifetime.StorageStatic, lifetime.LifetimeSingleton, "",
			sf.lineOf(m[0]), sf.functionAt(m[0]), lifetime.FormStaticDestroy, lifetime.FormLazyInitRace)
		if rec != nil {
			a.addPoint(rec.id, sf.siteAt(m[0]))
		}
	}
}

func (a *analyzer) scanSingleton(sf *sourceFile) {
	text := sf.text
	for _, m := range reSingleton.FindAllStringIndex(text, -1) {
		if sf.inSpan(m[0], sf.memberRanges) {
			continue // member 分类已给出 static/singleton
		}
		open := m[1] - 1
		close := matchAngleStr(text, open)
		if close < 0 {
			a.addUnknown(sf.rel, "template_unparsed:Singleton")
			continue
		}
		inner := normalizeTypeName(text[open+1 : close])
		if inner == "" || isBuiltinType(inner) {
			continue
		}
		if a.hasSingletonType(inner) {
			continue
		}
		rec := a.addFamily(sf, inner, lifetime.StorageStatic, lifetime.LifetimeSingleton, "",
			sf.lineOf(m[0]), sf.functionAt(m[0]), lifetime.FormStaticDestroy, lifetime.FormLazyInitRace)
		if rec != nil {
			a.addPoint(rec.id, sf.siteAt(m[0]))
		}
	}
}

func (a *analyzer) scanPools(sf *sourceFile) {
	text := sf.text
	for _, m := range rePool.FindAllStringSubmatchIndex(text, -1) {
		if sf.inSpan(m[0], sf.memberRanges) {
			continue
		}
		keyword := text[m[2]:m[3]]
		pos := skipSpacesStr(text, m[1])
		canonical := ""
		if pos < len(text) && text[pos] == '<' {
			if close := matchAngleStr(text, pos); close > pos {
				canonical = normalizeTypeName(text[pos+1 : close])
			}
		}
		if canonical == "" {
			canonical = keyword
		}
		if isBuiltinType(canonical) {
			continue
		}
		var storage lifetime.StorageClass
		var form lifetime.LifetimeForm
		if keyword == "ObjectPool" {
			storage = lifetime.StoragePool
			form = lifetime.LifetimePool
		} else {
			storage = lifetime.StorageHandle
			form = lifetime.LifetimeHandle
		}
		rec := a.addFamily(sf, canonical, storage, form, "", sf.lineOf(m[0]), sf.functionAt(m[0]), lifetime.FormPoolABA)
		if rec != nil {
			a.addPoint(rec.id, sf.siteAt(m[0]))
		}
	}
}

func (a *analyzer) isDirectClassMember(sf *sourceFile, off int) bool {
	open, _ := sf.enclosingBrace(off)
	if open < 0 {
		return false
	}
	for _, sp := range sf.classRanges {
		if sp.start == open {
			return true
		}
	}
	return false
}

func (a *analyzer) applyLazyInitForms() {
	for _, rec := range a.families {
		if !a.lazyFiles[rec.file] {
			continue
		}
		if rec.key.StorageClass == lifetime.StorageStatic ||
			rec.key.LifetimeForm == lifetime.LifetimeSingleton ||
			strings.Contains(strings.ToLower(rec.funcName), "instance") {
			rec.forms[lifetime.FormLazyInitRace] = struct{}{}
		}
	}
}

// ---------------------------------------------------------------------------
// 回调与跨 family 关系（§4.9/R-4.28）
// ---------------------------------------------------------------------------

type registerOcc struct {
	sf   *sourceFile
	off  int
	line int
	name string
}

func (a *analyzer) scanCallbacks() {
	var occs []registerOcc
	for _, sf := range a.files {
		for _, m := range reRegister.FindAllStringSubmatchIndex(sf.text, -1) {
			occs = append(occs, registerOcc{
				sf:   sf,
				off:  m[0],
				line: sf.lineOf(m[0]),
				name: sf.text[m[2]:m[3]],
			})
		}
	}
	sort.Slice(occs, func(i, j int) bool {
		if occs[i].sf.rel != occs[j].sf.rel {
			return occs[i].sf.rel < occs[j].sf.rel
		}
		if occs[i].line != occs[j].line {
			return occs[i].line < occs[j].line
		}
		return occs[i].name < occs[j].name
	})

	seen := make(map[string]bool)
	for _, occ := range occs {
		site := fmt.Sprintf("%s:%d", occ.sf.rel, occ.line)
		key := site + "\x1f" + occ.name
		if seen[key] {
			continue
		}
		seen[key] = true

		unregSite, _ := a.findUnregister(occ.name)
		capture := callbackCapture(occ.sf, occ.off)
		a.callbacks[key] = lifetime.CallbackSpec{
			RegisterSite:   site,
			UnregisterSite: unregSite,
			Capture:        capture,
			SourceRef:      site,
		}

		subjectType := a.ownerTypeAt(occ.sf, occ.off)
		subject := a.firstFamilyOfType(subjectType)
		objectType := a.paramTypeForRegister(occ.sf, occ.off, occ.name, subjectType)
		object := a.firstFamilyOfType(objectType)
		// 注册站点本身也是类型证据：若主体/回调类型尚无 family，
		// 以注册点为 alloc_site 补一个声明型 family，保证 registers
		// 关系不会因缺少对象族而消失（R-4.28/R-4.29）。
		if subject == "" && subjectType != "" {
			if rec := a.addFamily(occ.sf, subjectType, lifetime.StorageHeap, lifetime.LifetimeRaw, "",
				occ.line, occ.sf.functionAt(occ.off),
				lifetime.FormDanglingThis, lifetime.FormContainerInvalid); rec != nil {
				subject = rec.id
				a.addPoint(rec.id, site)
			}
		}
		if object == "" && objectType != "" {
			if rec := a.addFamily(occ.sf, objectType, lifetime.StorageHeap, lifetime.LifetimeRaw, "",
				occ.line, occ.sf.functionAt(occ.off),
				lifetime.FormDanglingThis, lifetime.FormContainerInvalid); rec != nil {
				object = rec.id
				a.addPoint(rec.id, site)
			}
		}

		if subject != "" {
			a.applyForm(subject, lifetime.FormDanglingThis)
		}
		if object != "" {
			a.applyForm(object, lifetime.FormDanglingThis)
		}
		if subject != "" && object != "" {
			a.addRelation("notifies", subject, object, site)
		}
		if unregSite != "" {
			if subject != "" {
				a.applyForm(subject, lifetime.FormRegisterLeak)
			}
			if object != "" {
				a.applyForm(object, lifetime.FormRegisterLeak)
			}
			if subject != "" && object != "" {
				a.addRelation("registers", subject, object, site)
			}
		}
	}
}

// findUnregister 查找与注册函数配对的 Remove*/Unregister* 首次出现。
func (a *analyzer) findUnregister(registerName string) (string, int) {
	var candidates []string
	if strings.HasPrefix(registerName, "Register") {
		candidates = append(candidates, "Unregister"+strings.TrimPrefix(registerName, "Register"))
	}
	switch registerName {
	case "AddObserver":
		candidates = append(candidates, "RemoveObserver")
	case "AddListener":
		candidates = append(candidates, "RemoveListener")
	case "SetCallback":
		candidates = append(candidates, "RemoveCallback", "ClearCallback")
	}
	for _, cand := range candidates {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(cand) + `\s*\(`)
		for _, sf := range a.files {
			if loc := re.FindStringIndex(sf.text); loc != nil {
				return sf.siteAt(loc[0]), sf.lineOf(loc[0])
			}
		}
	}
	return "", 0
}

// callbackCapture 依据注册语句附近的文本判定捕获方式；未知时回退 raw_this。
func callbackCapture(sf *sourceFile, off int) string {
	stmt := strings.ToLower(sf.statementAround(off, 512))
	switch {
	case strings.Contains(stmt, "weak"):
		return "weak"
	case strings.Contains(stmt, "shared"):
		return "shared"
	case strings.Contains(stmt, "handle"):
		return "handle"
	case strings.Contains(stmt, "this"):
		return "raw_this"
	default:
		return "raw_this"
	}
}

func (sf *sourceFile) statementAround(off, limit int) string {
	start := off
	for start > 0 && off-start < limit {
		c := sf.text[start-1]
		if c == ';' || c == '{' || c == '}' {
			break
		}
		start--
	}
	end := off
	for end < len(sf.text) && end-off < limit {
		c := sf.text[end]
		end++
		if c == ';' || c == '{' || c == '}' {
			break
		}
	}
	return sf.text[start:end]
}

func (a *analyzer) classAt(sf *sourceFile, off int) *classInfo {
	var best *classInfo
	bestSpan := 1 << 30
	for _, cd := range a.classList {
		if cd.file != sf {
			continue
		}
		if off > cd.open && off < cd.close {
			if sp := cd.close - cd.open; sp < bestSpan {
				best = cd
				bestSpan = sp
			}
		}
	}
	return best
}

func (a *analyzer) ownerTypeAt(sf *sourceFile, off int) string {
	if cd := a.classAt(sf, off); cd != nil {
		return cd.name
	}
	if fn := sf.functionAt(off); fn != "" {
		if i := strings.LastIndex(fn, "::"); i > 0 {
			return typeBase(fn[:i])
		}
	}
	return ""
}

func (a *analyzer) paramTypeForRegister(sf *sourceFile, off int, name, subjectType string) string {
	if cd := a.classAt(sf, off); cd != nil {
		if ps := cd.methods[name]; len(ps) > 0 {
			return ps[0]
		}
	}
	if subjectType != "" {
		if cd := a.classes[subjectType]; cd != nil {
			if ps := cd.methods[name]; len(ps) > 0 {
				return ps[0]
			}
		}
	}
	return ""
}

func (a *analyzer) addRelation(relType, subject, object, site string) {
	if subject == "" || object == "" || subject == object {
		return
	}
	rid := lifetime.CanonicalHash(relType, subject, object, site)
	if _, ok := a.relations[rid]; ok {
		return
	}
	a.relations[rid] = lifetime.ObjectRelation{
		RelationID:      rid,
		Type:            relType,
		SubjectFamilyID: subject,
		ObjectFamilyID:  object,
		Site:            site,
		SourceRef:       site,
		Confidence:      lifetime.ConfidenceLow,
	}
}

func (a *analyzer) scanOwnsRelations() {
	for _, cd := range a.classList {
		subject := a.firstFamilyOfType(cd.name)
		if subject == "" {
			continue
		}
		for _, fd := range cd.fields {
			if fd.familyID == "" || fd.familyID == subject {
				continue
			}
			a.addRelation("owns", subject, fd.familyID, fd.site)
		}
	}
}

// ---------------------------------------------------------------------------
// 生命周期点：new/delete/free/reset/~Type/Unregister*
// ---------------------------------------------------------------------------

var (
	reDelete     = regexp.MustCompile(`\bdelete\s*(?:\[\s*\])?\s*([A-Za-z_][A-Za-z0-9_]*)`)
	reFree       = regexp.MustCompile(`\bfree\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)?\s*\)`)
	reReset      = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*(?:\.|->)\s*reset\s*\(`)
	reDtor       = regexp.MustCompile(`~\s*([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	reUnregister = regexp.MustCompile(`\b(Unregister[A-Za-z0-9_]*|RemoveObserver|RemoveListener)\s*\(`)
	reVarDecl    = regexp.MustCompile(`(?m)([A-Za-z_][A-Za-z0-9_:]*(?:\s*<[^;{}()\n]*>)?)\s*(\*{0,2})\s*([A-Za-z_][A-Za-z0-9_]*)\s*([;=,\)\[])`)
)

func (a *analyzer) scanLifetimePoints() {
	for _, sf := range a.files {
		varTypes := buildVarTypes(sf)
		a.scanDeletes(sf, varTypes)
		a.scanFrees(sf, varTypes)
		a.scanResets(sf, varTypes)
		a.scanDestructors(sf)
		a.scanUnregisters(sf)
	}
}

func buildVarTypes(sf *sourceFile) map[string]string {
	out := make(map[string]string)
	for _, m := range reVarDecl.FindAllStringSubmatch(sf.text, -1) {
		typeText, ptr, name := m[1], m[2], m[3]
		if ptr == "" && !looksLikeClassType(typeText) {
			continue
		}
		if name == "" || isKeywordName(name) {
			continue
		}
		info := classifyType(typeText)
		if info.canonical == "" || isBuiltinType(info.canonical) {
			continue
		}
		if _, ok := out[name]; !ok {
			out[name] = info.canonical
		}
	}
	return out
}

func (a *analyzer) scanDeletes(sf *sourceFile, varTypes map[string]string) {
	for _, m := range reDelete.FindAllStringSubmatchIndex(sf.text, -1) {
		name := sf.text[m[2]:m[3]]
		site := sf.siteAt(m[0])
		if name == "this" {
			if t := a.ownerTypeAt(sf, m[0]); t != "" {
				a.pointsForType(t, site, "")
			}
			continue
		}
		if t, ok := varTypes[name]; ok {
			a.pointsForType(t, site, "")
		}
	}
}

func (a *analyzer) scanFrees(sf *sourceFile, varTypes map[string]string) {
	for _, m := range reFree.FindAllStringSubmatchIndex(sf.text, -1) {
		site := sf.siteAt(m[0])
		if m[2] >= 0 && m[3] > m[2] {
			name := sf.text[m[2]:m[3]]
			if t, ok := varTypes[name]; ok {
				a.pointsForType(t, site, "")
				continue
			}
		}
		// 无法从实参得到类型时，退化到“同文件同函数内的堆 family”。
		fn := sf.functionAt(m[0])
		for _, rec := range a.families {
			if rec.file == sf.rel && rec.key.StorageClass == lifetime.StorageHeap && rec.funcName == fn {
				a.addPoint(rec.id, site)
			}
		}
	}
}

func (a *analyzer) scanResets(sf *sourceFile, varTypes map[string]string) {
	for _, m := range reReset.FindAllStringSubmatchIndex(sf.text, -1) {
		name := sf.text[m[2]:m[3]]
		site := sf.siteAt(m[0])
		if ids := a.memberFamilies[name]; len(ids) > 0 {
			for _, id := range ids {
				a.addPoint(id, site)
			}
			continue
		}
		if t, ok := varTypes[name]; ok {
			a.pointsForType(t, site, lifetime.LifetimeShared)
		}
	}
}

func (a *analyzer) scanDestructors(sf *sourceFile) {
	for _, m := range reDtor.FindAllStringSubmatchIndex(sf.text, -1) {
		typeName := sf.text[m[2]:m[3]]
		if isKeywordName(typeName) {
			continue
		}
		canon := normalizeTypeName(typeName)
		if canon == "" || isBuiltinType(canon) {
			continue
		}
		site := sf.siteAt(m[0])
		a.pointsForType(canon, site, "")
	}
}

func (a *analyzer) scanUnregisters(sf *sourceFile) {
	for _, m := range reUnregister.FindAllStringIndex(sf.text, -1) {
		if t := a.ownerTypeAt(sf, m[0]); t != "" {
			a.pointsForType(t, sf.siteAt(m[0]), "")
		}
	}
}

// ---------------------------------------------------------------------------
// 同步原语（§8.1/R-8.12：protects 不得为空）
// ---------------------------------------------------------------------------

type syncPattern struct {
	re        *regexp.Regexp
	primitive string
	family    string
}

var syncPatterns = []syncPattern{
	{regexp.MustCompile(`\bstd::mutex\b`), "std::mutex", "mutex"},
	{regexp.MustCompile(`\bbase::Mutex\b`), "base::Mutex", "mutex"},
	{regexp.MustCompile(`\bv8::Locker\b`), "v8::Locker", "locker"},
	{regexp.MustCompile(`\bAPI_LOCK\b`), "API_LOCK", "api_lock"},
	{regexp.MustCompile(`\bstd::lock_guard\b`), "std::lock_guard", "lock_guard"},
	{regexp.MustCompile(`\bstd::unique_lock\b`), "std::unique_lock", "unique_lock"},
	{regexp.MustCompile(`\bstd::atomic\b`), "std::atomic", "atomic"},
	{regexp.MustCompile(`\bbase::LockGuard\b`), "base::LockGuard", "lock_guard"},
	{regexp.MustCompile(`\babsl::Mutex\b`), "absl::Mutex", "mutex"},
	{regexp.MustCompile(`\babsl::MutexLock\b`), "absl::MutexLock", "mutex_lock"},
	{regexp.MustCompile(`\bpthread_mutex_lock\b`), "pthread_mutex_lock", "mutex"},
	// 裸类型名兜底；同一声明行按 family 去重，std:: 版本优先。
	{regexp.MustCompile(`\bmutex\b`), "mutex", "mutex"},
	{regexp.MustCompile(`\block_guard\b`), "lock_guard", "lock_guard"},
	{regexp.MustCompile(`\bunique_lock\b`), "unique_lock", "unique_lock"},
	{regexp.MustCompile(`\batomic\b`), "atomic", "atomic"},
	{regexp.MustCompile(`\bLocker\b`), "Locker", "locker"},
	{regexp.MustCompile(`\bMutexLock\b`), "MutexLock", "mutex_lock"},
	{regexp.MustCompile(`\bAutoLock\b`), "AutoLock", "auto_lock"},
	{regexp.MustCompile(`\bSpinLock\b`), "SpinLock", "spin_lock"},
}

var reGuardArg = regexp.MustCompile(`\(\s*([A-Za-z_][A-Za-z0-9_]*(?:(?:\.|->|::)[A-Za-z_][A-Za-z0-9_]*)*)\s*\)`)

func (a *analyzer) scanSyncPrimitives() {
	for _, sf := range a.files {
		seen := make(map[string]bool)
		for _, p := range syncPatterns {
			for _, loc := range p.re.FindAllStringIndex(sf.text, -1) {
				site := sf.siteAt(loc[0])
				key := site + "\x1f" + p.family
				if seen[key] {
					continue
				}
				seen[key] = true
				scope := sf.functionAt(loc[0])
				if scope == "" {
					scope = "file"
				}
				protects := guardArgNear(sf, loc[1])
				if protects == "" {
					if scope != "file" {
						protects = scope
					} else {
						protects = "unknown"
					}
				}
				a.syncs[key] = lifetime.SyncPrimitive{
					Primitive: p.primitive,
					Scope:     scope,
					Protects:  protects,
					FileLine:  site,
				}
			}
		}
	}
}

// guardArgNear 尝试从 lock_guard/unique_lock/Locker 的构造实参得到
// protects 表达式，例如 guard(mu_) -> mu_。
func guardArgNear(sf *sourceFile, start int) string {
	end := start + 400
	if end > len(sf.text) {
		end = len(sf.text)
	}
	seg := sf.text[start:end]
	if i := strings.IndexAny(seg, ";{}"); i >= 0 {
		seg = seg[:i]
	}
	if m := reGuardArg.FindStringSubmatch(seg); m != nil {
		return m[1]
	}
	return ""
}

// ---------------------------------------------------------------------------
// 并发域与 domain pair（§4.2）
// ---------------------------------------------------------------------------

type domainPattern struct {
	re   *regexp.Regexp
	kind lifetime.DomainKind
}

var domainPatterns = []domainPattern{
	{regexp.MustCompile(`\bstd::thread\b`), lifetime.DomainThread},
	{regexp.MustCompile(`\bstd::jthread\b`), lifetime.DomainThread},
	{regexp.MustCompile(`\bpthread_create\s*\(`), lifetime.DomainThread},
	{regexp.MustCompile(`\bbase::Thread\b`), lifetime.DomainThread},
	{regexp.MustCompile(`\bThreadPool\b`), lifetime.DomainExecutor},
	{regexp.MustCompile(`\bTaskRunner\b`), lifetime.DomainExecutor},
	{regexp.MustCompile(`\bPostTask\s*\(`), lifetime.DomainQueue},
	{regexp.MustCompile(`\bScheduleWork\s*\(`), lifetime.DomainQueue},
	{regexp.MustCompile(`\bRunLoop\b`), lifetime.DomainQueue},
	// 注意：不识别 Isolate。V8 几乎每个文件都有 Isolate，把它当域会
	// 产生大量噪声域与 pair，bootstrap 覆盖行会爆炸。
}

const (
	// maxDomains/maxPairs 是启发式上限：宁可少报域，也不让覆盖账本爆炸。
	maxDomains       = 8
	maxPairs         = 8
	maxFamilyDomains = 4
)

func (a *analyzer) scanDomains() {
	type domainCandidate struct {
		domain lifetime.Domain
		hits   int
	}
	candidates := make(map[string]*domainCandidate)

	for _, sf := range a.files {
		for _, p := range domainPatterns {
			for _, loc := range p.re.FindAllStringIndex(sf.text, -1) {
				site := sf.siteAt(loc[0])
				id := lifetime.DomainClassID(p.kind, site, "")
				if c, ok := candidates[id]; ok {
					c.hits++
					continue
				}
				candidates[id] = &domainCandidate{
					domain: lifetime.Domain{
						DomainClassID:     id,
						Kind:              p.kind,
						InstanceSite:      site,
						Serialization:     lifetime.SerializationUnknown,
						ConcurrencyDegree: lifetime.ConcurrencyUnknown,
					},
					hits: 1,
				}
			}
		}
	}

	// 超过上限时保留命中次数最多的域（同分按 instance_site/kind/id 排序），
	// 而不是按文件遍历顺序随机截断。
	list := make([]*domainCandidate, 0, len(candidates))
	for _, c := range candidates {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].hits != list[j].hits {
			return list[i].hits > list[j].hits
		}
		if list[i].domain.InstanceSite != list[j].domain.InstanceSite {
			return list[i].domain.InstanceSite < list[j].domain.InstanceSite
		}
		if list[i].domain.Kind != list[j].domain.Kind {
			return list[i].domain.Kind < list[j].domain.Kind
		}
		return list[i].domain.DomainClassID < list[j].domain.DomainClassID
	})
	if len(list) > maxDomains {
		list = list[:maxDomains]
		a.addUnknown("domains", "domain_cap")
	}

	a.orderedDomains = make([]string, 0, len(list)+1)
	for _, c := range list {
		a.domains[c.domain.DomainClassID] = c.domain
		a.orderedDomains = append(a.orderedDomains, c.domain.DomainClassID)
	}
	if len(a.domains) == 0 {
		site := a.sourceRoot + ":1"
		id := lifetime.DomainClassID(lifetime.DomainThread, site, "")
		a.domains[id] = lifetime.Domain{
			DomainClassID:     id,
			Kind:              lifetime.DomainThread,
			InstanceSite:      site,
			Serialization:     lifetime.SerializationUnknown,
			ConcurrencyDegree: lifetime.ConcurrencyUnknown,
		}
		a.orderedDomains = []string{id}
	}

	// pair 生成：优先 (first,first)，再 first 与其他域的跨域 pair，
	// 最后才补剩余组合，总数 cap 到 maxPairs。first 取相关度最高的域，
	// 作为 main 域的启发式代理。
	ids := a.orderedDomains
	added := 0
	totalPairs := len(ids) * (len(ids) + 1) / 2
	addPair := func(x, y string) {
		if added >= maxPairs {
			return
		}
		aID, bID := x, y
		if aID > bID {
			aID, bID = bID, aID
		}
		pid := lifetime.PairClassID(aID, bID)
		if _, ok := a.pairs[pid]; ok {
			return
		}
		a.pairs[pid] = lifetime.DomainPair{
			PairClassID:      pid,
			DomainA:          aID,
			DomainB:          bID,
			DomainConfidence: lifetime.DomainConfidenceLow,
			PairBasisOptions: []lifetime.PairBasis{
				lifetime.PairBasisLogical,
				lifetime.PairBasisExecution,
			},
		}
		added++
	}
	addPair(ids[0], ids[0])
	for j := 1; j < len(ids) && added < maxPairs; j++ {
		addPair(ids[0], ids[j])
	}
	for i := 1; i < len(ids) && added < maxPairs; i++ {
		for j := i; j < len(ids) && added < maxPairs; j++ {
			addPair(ids[i], ids[j])
		}
	}
	if added < totalPairs {
		a.addUnknown("domains", "domain_pairs_cap")
	}
}
