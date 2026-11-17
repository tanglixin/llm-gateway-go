// Command airatio reports a declared, auditable AI-code ratio for git repositories.
//
// Git cannot tell whether a line was written by a human or a model: the author is
// always the committer. Instead of guessing from code style, this tool reads
// explicit trailers in commit messages and reports a lower/upper bound plus the
// labelling coverage. A low coverage means the number is a floor, not a fact.
//
// Declared trailers (anywhere in the commit message body):
//
//	AI-Generated-By: claude-code | cursor | copilot | ...   AI took part
//	AI-Scope: full | partial                              full = whole commit generated
//	Human-Only: yes                                       explicitly human-written
//	AI-Reviewed: yes                                      human review recorded (optional)
//
// Usage:
//
//	go run ./cmd/airatio
//	go run ./cmd/airatio -format=markdown -out=docs/ai-code-report.md
//	go run ./cmd/airatio -strict -min-coverage=0.8
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// model
// ---------------------------------------------------------------------------

type scope uint8

const (
	scopeUnknown scope = iota // no trailer at all
	scopeHuman                // Human-Only: yes
	scopePartial              // AI-Generated-By present, AI-Scope partial/absent
	scopeFull                 // AI-Scope: full
)

func (s scope) label() string {
	switch s {
	case scopeHuman:
		return "人工"
	case scopePartial:
		return "AI 部分"
	case scopeFull:
		return "AI 完全"
	default:
		return "未标注"
	}
}

type commit struct {
	Repo     string
	Hash     string
	Date     string
	Subject  string
	Tool     string
	Scope    scope
	Conflict bool
	Add      int
	Del      int
	Layers   map[string]int
}

type layerStat struct {
	Layer     string  `json:"layer"`
	Commits   int     `json:"commits"`
	Added     int     `json:"added"`
	FullAdded int     `json:"full_added"`
	Partial   int     `json:"partial_added"`
	Lower     float64 `json:"lower_pct"`
	Upper     float64 `json:"upper_pct"`
}

type report struct {
	GeneratedAt   string      `json:"generated_at"`
	Method        string      `json:"method"`
	Repos         []string    `json:"repos"`
	SkippedRepos  []string    `json:"skipped_repos"`
	Baseline      string      `json:"baseline"`
	Range         string      `json:"range"`
	Commits       int         `json:"commits"`
	NoCodeCommits int         `json:"no_code_commits"`
	Labeled       int         `json:"labeled_commits"`
	Unknown       int         `json:"unlabeled_commits"`
	Human         int         `json:"human_commits"`
	AICommits     int         `json:"ai_commits"`
	Full          int         `json:"ai_full_commits"`
	Partial       int         `json:"ai_partial_commits"`
	Conflicts     int         `json:"conflicting_commits"`
	TotalAdded    int         `json:"total_added_lines"`
	TotalDeleted  int         `json:"total_deleted_lines"`
	FullAdded     int         `json:"ai_full_added_lines"`
	PartialAdded  int         `json:"ai_partial_added_lines"`
	Coverage      float64     `json:"label_coverage_pct"`
	AICommitPct   float64     `json:"ai_commit_pct"`
	Lower         float64     `json:"lower_pct"`
	Upper         float64     `json:"upper_pct"`
	Estimate      float64     `json:"estimate_pct"`
	PartialWeight float64     `json:"partial_weight"`
	Layers        []layerStat `json:"layers"`
	Warnings      []string    `json:"warnings"`
}

type options struct {
	repos         []string
	since         string
	until         string
	baseline      string
	format        string
	out           string
	partialWeight float64
	minCoverage   float64
	strict        bool
	exts          map[string]bool
}

// ---------------------------------------------------------------------------
// pure helpers (unit tested)
// ---------------------------------------------------------------------------

var trailerRE = regexp.MustCompile(`(?i)^\s*(AI-Generated-By|AI-Scope|Human-Only|AI-Reviewed)\s*:\s*(.+?)\s*$`)
var hashRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

var defaultExts = []string{
	"go", "ts", "tsx", "js", "jsx", "vue", "sql", "proto",
	"sh", "yaml", "yml", "html", "css",
}

var excludedDirs = []string{
	"web/", "dist/", "build/", "bin/", "vendor/", "node_modules/",
	"coverage/", ".git/", ".idea/", ".vscode/",
}

var excludedFiles = []string{"go.sum", "go.mod", "package-lock.json", "yarn.lock", "pnpm-lock.yaml"}

// parseTrailers resolves the AI declaration from a commit message body.
// A commit declaring both AI and Human-Only is a conflict and stays unlabelled.
func parseTrailers(body string) (tool string, s scope, reviewed bool, conflict bool) {
	s = scopeUnknown
	var human, ai bool
	for _, line := range strings.Split(body, "\n") {
		m := trailerRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key, val := strings.ToLower(m[1]), strings.ToLower(strings.TrimSpace(m[2]))
		switch key {
		case "ai-generated-by":
			ai = true
			tool = strings.TrimSpace(m[2])
		case "ai-scope":
			if val == "full" {
				s = scopeFull
			} else if val == "partial" {
				s = scopePartial
			}
		case "human-only":
			if val == "yes" || val == "true" || val == "1" {
				human = true
			}
		case "ai-reviewed":
			if val == "yes" || val == "true" || val == "1" {
				reviewed = true
			}
		}
	}
	if ai && human {
		return tool, scopeUnknown, reviewed, true
	}
	if human {
		return tool, scopeHuman, reviewed, false
	}
	if ai {
		if s != scopeFull {
			s = scopePartial
		}
		return tool, s, reviewed, false
	}
	return tool, scopeUnknown, reviewed, false
}

// countedPath reports whether a changed file counts as authored source code.
func countedPath(path string, exts map[string]bool) bool {
	p := strings.ReplaceAll(path, "\\", "/")
	for _, d := range excludedDirs {
		if strings.HasPrefix(p, d) || strings.Contains(p, "/"+d) {
			return false
		}
	}
	base := filepath.Base(p)
	for _, f := range excludedFiles {
		if base == f {
			return false
		}
	}
	if strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".lock") {
		return false
	}
	idx := strings.LastIndex(base, ".")
	if idx < 0 {
		return false
	}
	return exts[strings.ToLower(base[idx+1:])]
}

// repoLabel turns ".", ".." or an absolute path into a readable directory name.
func repoLabel(repo string) string {
	if abs, err := filepath.Abs(repo); err == nil {
		return filepath.Base(abs)
	}
	return filepath.Base(filepath.Clean(repo))
}

// layerOf maps a path to a coarse architectural layer.
func layerOf(repo, path string) string {
	p := strings.ReplaceAll(path, "\\", "/")
	parts := strings.Split(strings.TrimPrefix(p, "./"), "/")
	prefix := repoLabel(repo)
	if len(parts) >= 2 {
		return prefix + "/" + parts[0] + "/" + parts[1]
	}
	if len(parts) == 1 && parts[0] != "" {
		return prefix + "/" + parts[0]
	}
	return prefix + "/(root)"
}

// parseNumstat reads one git-log record: first line is the hash, the rest are
// "added\tdeleted\tpath" lines. Binary files show "-" and are ignored.
func parseNumstat(repo, chunk string, exts map[string]bool) (add, del int, layers map[string]int) {
	layers = map[string]int{}
	lines := strings.Split(strings.TrimSpace(chunk), "\n")
	if len(lines) == 0 || !hashRE.MatchString(strings.TrimSpace(lines[0])) {
		return 0, 0, layers
	}
	for _, line := range lines[1:] {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			continue
		}
		a, errA := strconv.Atoi(parts[0])
		d, errD := strconv.Atoi(parts[1])
		if errA != nil || errD != nil { // binary
			continue
		}
		path := parts[2]
		if !countedPath(path, exts) {
			continue
		}
		add += a
		del += d
		layers[layerOf(repo, path)] += a
	}
	return add, del, layers
}

func pctRaw(num, den int) float64 {
	if den <= 0 {
		return 0
	}
	return float64(num) * 100 / float64(den)
}

func pct(num, den int) float64 {
	return round2(pctRaw(num, den))
}

func round2(v float64) float64 {
	r, _ := strconv.ParseFloat(fmt.Sprintf("%.2f", v), 64)
	return r
}

// ---------------------------------------------------------------------------
// git access
// ---------------------------------------------------------------------------

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func usableRepo(repo string) bool {
	_, err := git(repo, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

func hasCommit(repo, hash string) bool {
	_, err := git(repo, "cat-file", "-e", hash+"^{commit}")
	return err == nil
}

func logArgs(o options, baseline string) []string {
	args := []string{}
	if o.since != "" {
		args = append(args, "--since="+o.since)
	}
	if o.until != "" {
		args = append(args, "--until="+o.until)
	}
	if baseline != "" {
		args = append(args, baseline+"..HEAD")
	}
	return args
}

func collect(repo string, o options, baseline string) ([]commit, error) {
	meta, err := git(repo, append([]string{"log", "--format=\x1e%H\x1f%aI\x1f%B"}, logArgs(o, baseline)...)...)
	if err != nil {
		return nil, err
	}
	nums, err := git(repo, append([]string{"log", "--numstat", "--no-renames", "--format=\x1e%H"}, logArgs(o, baseline)...)...)
	if err != nil {
		return nil, err
	}

	byHash := map[string]*commit{}
	for _, raw := range strings.Split(meta, "\x1e") {
		parts := strings.SplitN(strings.TrimSpace(raw), "\x1f", 3)
		if len(parts) < 3 || !hashRE.MatchString(parts[0]) {
			continue
		}
		body := strings.TrimSpace(parts[2])
		tool, s, _, conflict := parseTrailers(body)
		subject := strings.SplitN(body, "\n", 2)[0]
		byHash[parts[0]] = &commit{
			Repo: repo, Hash: parts[0], Date: parts[1], Subject: subject,
			Tool: tool, Scope: s, Conflict: conflict, Layers: map[string]int{},
		}
	}

	for _, raw := range strings.Split(nums, "\x1e") {
		lines := strings.Split(strings.TrimSpace(raw), "\n")
		if len(lines) == 0 {
			continue
		}
		hash := strings.TrimSpace(lines[0])
		c, ok := byHash[hash]
		if !ok {
			continue
		}
		c.Add, c.Del, c.Layers = parseNumstat(repo, raw, o.exts)
	}

	out := make([]commit, 0, len(byHash))
	for _, c := range byHash {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	return out, nil
}

// ---------------------------------------------------------------------------
// aggregation
// ---------------------------------------------------------------------------

func build(commits []commit, o options, repos, skipped []string, baseline string) report {
	r := report{
		GeneratedAt:   time.Now().Format("2006-01-02 15:04:05"),
		Method:        "declared-trailer interval: lower=AI-Scope:full added lines, upper=full+partial added lines",
		Repos:         repos,
		SkippedRepos:  skipped,
		Baseline:      baseline,
		Range:         rangeLabel(o, baseline),
		PartialWeight: o.partialWeight,
	}
	layerAgg := map[string]*layerStat{}
	for _, c := range commits {
		r.Commits++
		if c.Add == 0 {
			r.NoCodeCommits++
		}
		if c.Conflict {
			r.Conflicts++
		}
		switch c.Scope {
		case scopeHuman:
			r.Human++
			r.Labeled++
		case scopePartial:
			r.Partial++
			r.AICommits++
			r.Labeled++
		case scopeFull:
			r.Full++
			r.AICommits++
			r.Labeled++
		default:
			r.Unknown++
		}
		r.TotalAdded += c.Add
		r.TotalDeleted += c.Del
		if c.Scope == scopeFull {
			r.FullAdded += c.Add
		}
		if c.Scope == scopePartial {
			r.PartialAdded += c.Add
		}
		for name, added := range c.Layers {
			l, ok := layerAgg[name]
			if !ok {
				l = &layerStat{Layer: name}
				layerAgg[name] = l
			}
			l.Commits++
			l.Added += added
			if c.Scope == scopeFull {
				l.FullAdded += added
			}
			if c.Scope == scopePartial {
				l.Partial += added
			}
		}
	}
	r.Coverage = pct(r.Labeled, r.Commits)
	r.AICommitPct = pct(r.AICommits, r.Labeled)
	r.Lower = pct(r.FullAdded, r.TotalAdded)
	r.Upper = pct(r.FullAdded+r.PartialAdded, r.TotalAdded)
	rawLower := pctRaw(r.FullAdded, r.TotalAdded)
	rawUpper := pctRaw(r.FullAdded+r.PartialAdded, r.TotalAdded)
	r.Estimate = round2(rawLower + (rawUpper-rawLower)*o.partialWeight)
	for _, l := range layerAgg {
		l.Lower = pct(l.FullAdded, l.Added)
		l.Upper = pct(l.FullAdded+l.Partial, l.Added)
		r.Layers = append(r.Layers, *l)
	}
	sort.Slice(r.Layers, func(i, j int) bool { return r.Layers[i].Added > r.Layers[j].Added })

	if r.Commits == 0 {
		r.Warnings = append(r.Warnings, "统计区间内没有提交；如已设置基线，请确认基线之后有新提交。")
	}
	if r.Coverage < o.minCoverage*100 {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"标注覆盖率 %.1f%% 低于阈值 %.0f%%：当前数字是下限，不能代表真实 AI 使用率。请补齐提交 trailer 或执行 make ai-ratio-baseline 从现在开始统计。",
			r.Coverage, o.minCoverage*100))
	}
	if r.TotalAdded == 0 {
		r.Warnings = append(r.Warnings, "统计区间内没有计入的有效代码新增行（可能全是文档或构建产物）。")
	}
	if r.Conflicts > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"有 %d 个提交同时声明 AI-Generated-By 与 Human-Only，已按未标注处理，请修正提交信息。", r.Conflicts))
	}
	for _, s := range skipped {
		r.Warnings = append(r.Warnings, "仓库已跳过（无 .git 或不可访问）："+s)
	}
	return r
}

func rangeLabel(o options, baseline string) string {
	parts := []string{}
	if baseline != "" {
		parts = append(parts, baseline[:min(8, len(baseline))]+"..HEAD")
	} else {
		parts = append(parts, "全部历史")
	}
	if o.since != "" {
		parts = append(parts, "since="+o.since)
	}
	if o.until != "" {
		parts = append(parts, "until="+o.until)
	}
	return strings.Join(parts, " / ")
}

// ---------------------------------------------------------------------------
// rendering
// ---------------------------------------------------------------------------

func renderTable(r report, o options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "AI 代码率报告  ·  %s\n", r.GeneratedAt)
	fmt.Fprintf(&b, "口径：%s\n", r.Method)
	fmt.Fprintf(&b, "范围：%s  |  仓库：%s\n", r.Range, strings.Join(r.Repos, ", "))
	b.WriteString(strings.Repeat("─", 62) + "\n")
	fmt.Fprintf(&b, "提交总数            %5d   （其中无有效代码变更 %d）\n", r.Commits, r.NoCodeCommits)
	fmt.Fprintf(&b, "已标注提交          %5d   （%.1f%%）\n", r.Labeled, r.Coverage)
	fmt.Fprintf(&b, "  人工              %5d\n", r.Human)
	fmt.Fprintf(&b, "  AI 完全生成       %5d\n", r.Full)
	fmt.Fprintf(&b, "  AI 部分参与       %5d\n", r.Partial)
	fmt.Fprintf(&b, "  未标注            %5d   （其中声明冲突 %d）\n", r.Unknown, r.Conflicts)
	fmt.Fprintf(&b, "AI 参与提交占比      %5.1f%%  （AI 提交 / 已标注提交）\n", r.AICommitPct)
	b.WriteString(strings.Repeat("─", 62) + "\n")
	fmt.Fprintf(&b, "有效新增行          %5d   （删除 %d 行，不计入分母）\n", r.TotalAdded, r.TotalDeleted)
	fmt.Fprintf(&b, "AI 完全生成行       %5d\n", r.FullAdded)
	fmt.Fprintf(&b, "AI 部分参与行       %5d\n", r.PartialAdded)
	fmt.Fprintf(&b, "AI 代码率区间        [%.1f%%, %.1f%%]\n", r.Lower, r.Upper)
	fmt.Fprintf(&b, "点估计（partial 权重 %.2f）  %.1f%%\n", o.partialWeight, r.Estimate)
	if len(r.Layers) > 0 {
		b.WriteString(strings.Repeat("─", 62) + "\n")
		b.WriteString("分层（按新增行排序）\n")
		n := len(r.Layers)
		if n > 8 {
			n = 8
		}
		for _, l := range r.Layers[:n] {
			fmt.Fprintf(&b, "  %-34s %6d 行  [%5.1f%%, %5.1f%%]\n", l.Layer, l.Added, l.Lower, l.Upper)
		}
	}
	if len(r.Warnings) > 0 {
		b.WriteString(strings.Repeat("─", 62) + "\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "! %s\n", w)
		}
	}
	b.WriteString("\n说明：git 无法识别代码作者，本工具统计的是「提交时声明」的 AI 参与情况；\n")
	b.WriteString("区间下界只算 AI-Scope: full，上界把 partial 也算进去，真实值在两者之间。\n")
	return b.String()
}

func renderMarkdown(r report, o options) string {
	var b strings.Builder
	b.WriteString("# AI 代码率报告\n\n")
	fmt.Fprintf(&b, "> 生成时间：%s  \n> 统计范围：%s  \n> 口径：%s\n\n", r.GeneratedAt, r.Range, r.Method)
	b.WriteString("| 指标 | 值 |\n| --- | --- |\n")
	rows := [][2]string{
		{"提交总数", strconv.Itoa(r.Commits)},
		{"已标注提交 / 覆盖率", fmt.Sprintf("%d / %.1f%%", r.Labeled, r.Coverage)},
		{"AI 完全生成提交", strconv.Itoa(r.Full)},
		{"AI 部分参与提交", strconv.Itoa(r.Partial)},
		{"纯人工提交", strconv.Itoa(r.Human)},
		{"未标注提交", strconv.Itoa(r.Unknown)},
		{"有效新增行", strconv.Itoa(r.TotalAdded)},
		{"AI 代码率区间", fmt.Sprintf("[%.1f%%, %.1f%%]", r.Lower, r.Upper)},
		{"点估计", fmt.Sprintf("%.1f%% (partial 权重 %.2f)", r.Estimate, o.partialWeight)},
	}
	for _, row := range rows {
		fmt.Fprintf(&b, "| %s | %s |\n", row[0], row[1])
	}
	if len(r.Layers) > 0 {
		b.WriteString("\n## 分层\n\n| 层 | 提交 | 新增行 | 下界 | 上界 |\n| --- | --- | --- | --- | --- |\n")
		n := len(r.Layers)
		if n > 12 {
			n = 12
		}
		for _, l := range r.Layers[:n] {
			fmt.Fprintf(&b, "| `%s` | %d | %d | %.1f%% | %.1f%% |\n", l.Layer, l.Commits, l.Added, l.Lower, l.Upper)
		}
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\n## 提示\n\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}
	b.WriteString("\n## 口径说明\n\n")
	b.WriteString("Git 的提交作者永远是提交人，无法从代码本身反推是否由 AI 生成。本报表采用**声明式区间口径**：\n\n")
	b.WriteString("- 下界 = `AI-Scope: full` 提交的有效新增行 / 全部有效新增行；\n")
	b.WriteString("- 上界 = (`full` + `partial` 提交的有效新增行) / 全部有效新增行；\n")
	b.WriteString("- 点估计按 partial 权重折算，仅为报表便利，不是实测值；\n")
	b.WriteString("- **标注覆盖率低于 100% 时，所有数字都是下限**，须结合覆盖率一起阅读。\n\n")
	b.WriteString("提交规范、排除规则与复现命令见 [docs/ai-code-metrics.md](ai-code-metrics.md)。\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// entry point
// ---------------------------------------------------------------------------

func main() {
	var reposFlag, extFlag string
	o := options{}
	flag.StringVar(&reposFlag, "repos", ".", "逗号分隔的 git 仓库路径")
	flag.StringVar(&o.since, "since", "", "只统计该日期之后的提交（git --since）")
	flag.StringVar(&o.until, "until", "", "只统计该日期之前的提交（git --until）")
	flag.StringVar(&o.baseline, "baseline", "", "基线 commit/tag，只统计其后的提交")
	flag.StringVar(&o.format, "format", "table", "输出格式：table | json | markdown")
	flag.StringVar(&o.out, "out", "", "写入文件（默认输出到 stdout）")
	flag.StringVar(&extFlag, "ext", strings.Join(defaultExts, ","), "计入统计的代码后缀，逗号分隔")
	flag.Float64Var(&o.partialWeight, "partial-weight", 0.5, "partial 提交折算为 AI 行的权重（点估计用）")
	flag.Float64Var(&o.minCoverage, "min-coverage", 0.8, "标注覆盖率阈值（0~1），低于则告警")
	flag.BoolVar(&o.strict, "strict", false, "覆盖率低于阈值时以非零码退出（CI 用）")
	flag.Parse()

	for _, p := range strings.Split(reposFlag, ",") {
		if p = strings.TrimSpace(p); p != "" {
			o.repos = append(o.repos, p)
		}
	}
	o.exts = map[string]bool{}
	for _, e := range strings.Split(extFlag, ",") {
		if e = strings.TrimSpace(strings.TrimPrefix(e, ".")); e != "" {
			o.exts[strings.ToLower(e)] = true
		}
	}

	// A baseline file makes "start measuring from today" a one-liner.
	baseline := o.baseline
	if baseline == "" {
		if raw, err := os.ReadFile(".ai-ratio-baseline"); err == nil {
			baseline = strings.TrimSpace(string(raw))
		}
	}

	var all []commit
	var used, skipped []string
	for _, repo := range o.repos {
		if !usableRepo(repo) {
			skipped = append(skipped, repo)
			continue
		}
		used = append(used, repoLabel(repo))
		repoBaseline := baseline
		if repoBaseline != "" && !hasCommit(repo, repoBaseline) {
			repoBaseline = "" // baseline belongs to another repo; fall back to full history
		}
		cs, err := collect(repo, o, repoBaseline)
		if err != nil {
			fmt.Fprintf(os.Stderr, "仓库 %s 统计失败：%v\n", repo, err)
			os.Exit(2)
		}
		all = append(all, cs...)
	}

	r := build(all, o, used, skipped, baseline)
	var text string
	switch o.format {
	case "json":
		b, _ := json.MarshalIndent(r, "", "  ")
		text = string(b) + "\n"
	case "markdown", "md":
		text = renderMarkdown(r, o)
	default:
		text = renderTable(r, o)
	}

	if o.out != "" {
		if err := os.MkdirAll(filepath.Dir(o.out), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "创建输出目录失败：", err)
			os.Exit(2)
		}
		if err := os.WriteFile(o.out, []byte(text), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "写文件失败：", err)
			os.Exit(2)
		}
		fmt.Printf("已写入 %s\n", o.out)
	} else {
		fmt.Print(text)
	}
	if o.strict && r.Coverage < o.minCoverage*100 && r.Commits > 0 {
		fmt.Fprintf(os.Stderr, "strict 校验失败：标注覆盖率 %.1f%% < %.0f%%\n", r.Coverage, o.minCoverage*100)
		os.Exit(1)
	}
}
