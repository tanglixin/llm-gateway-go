package main

import (
	"strings"
	"testing"
)

func testExts() map[string]bool {
	m := map[string]bool{}
	for _, e := range defaultExts {
		m[e] = true
	}
	return m
}

func TestParseTrailers(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		want     scope
		tool     string
		reviewed bool
		conflict bool
	}{
		{"empty", "feat: add handler", scopeUnknown, "", false, false},
		{"ai only", "feat: add handler\n\nAI-Generated-By: claude-code", scopePartial, "claude-code", false, false},
		{"ai full", "feat: add handler\n\nAI-Generated-By: cursor\nAI-Scope: full", scopeFull, "cursor", false, false},
		{"ai partial explicit", "feat: x\nAI-Generated-By: copilot\nAI-Scope: partial", scopePartial, "copilot", false, false},
		{"ai reviewed", "feat: x\nAI-Generated-By: codex\nAI-Scope: full\nAI-Reviewed: yes", scopeFull, "codex", true, false},
		{"human only", "fix: typo\n\nHuman-Only: yes", scopeHuman, "", false, false},
		{"human true", "fix: typo\nHuman-Only: true", scopeHuman, "", false, false},
		{"conflict", "feat: x\nAI-Generated-By: claude-code\nHuman-Only: yes", scopeUnknown, "claude-code", false, true},
		{"scope without tool", "feat: x\nAI-Scope: full", scopeUnknown, "", false, false},
		{"case insensitive", "feat: x\nai-generated-by: Claude-Code\nAI-SCOPE: FULL", scopeFull, "Claude-Code", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tool, got, reviewed, conflict := parseTrailers(c.body)
			if got != c.want || tool != c.tool || reviewed != c.reviewed || conflict != c.conflict {
				t.Fatalf("got (tool=%q scope=%v reviewed=%v conflict=%v), want (tool=%q scope=%v reviewed=%v conflict=%v)",
					tool, got, reviewed, conflict, c.tool, c.want, c.reviewed, c.conflict)
			}
		})
	}
}

func TestCountedPath(t *testing.T) {
	exts := testExts()
	cases := []struct {
		path string
		want bool
	}{
		{"internal/rag/retrieval.go", true},
		{"cmd/server/main.go", true},
		{"src/views/Chat.vue", true},
		{"deploy/docker-compose.yml", true},
		{"docs/architecture.md", false},
		{"go.sum", false},
		{"go.mod", false},
		{"web/assets/index-a1b2.js", false},
		{"dist/bundle.min.js", false},
		{"vendor/gin/gin.go", false},
		{"node_modules/pkg/index.ts", false},
		{"internal/web/handler.go", false},
		{"README", false},
	}
	for _, c := range cases {
		if got := countedPath(c.path, exts); got != c.want {
			t.Errorf("countedPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestLayerOf(t *testing.T) {
	cases := []struct {
		repo, path, want string
	}{
		{"/repos/llm-gateway", "internal/rag/retrieval.go", "llm-gateway/internal/rag"},
		{"/repos/llm-gateway-ui", "src/api/client.ts", "llm-gateway-ui/src/api"},
		{"/repos/llm-gateway", "main.go", "llm-gateway/main.go"},
	}
	for _, c := range cases {
		if got := layerOf(c.repo, c.path); got != c.want {
			t.Errorf("layerOf(%q, %q) = %q, want %q", c.repo, c.path, got, c.want)
		}
	}
	if strings.HasPrefix(layerOf(".", "internal/rag/a.go"), "./") {
		t.Error("layer prefix should be a real directory name, not '.'")
	}
}

func TestParseNumstat(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	chunk := hash + "\n\n" +
		"120\t3\tinternal/rag/retrieval.go\n" +
		"30\t0\tinternal/rag/embedder.go\n" +
		"10\t0\tinternal/gateway/router.go\n" +
		"999\t0\tdocs/architecture.md\n" +
		"500\t0\tweb/assets/app.js\n" +
		"-\t-\tlogo.png\n"
	add, del, layers := parseNumstat("/repos/llm-gateway", chunk, testExts())
	if add != 160 || del != 3 {
		t.Fatalf("add=%d del=%d, want 160/3", add, del)
	}
	if got := layers["llm-gateway/internal/rag"]; got != 150 {
		t.Errorf("rag layer = %d, want 150", got)
	}
	if _, ok := layers["llm-gateway/docs"]; ok {
		t.Error("markdown should not be counted as a layer")
	}
	if _, ok := layers["llm-gateway/web"]; ok {
		t.Error("build output should not be counted as a layer")
	}
}

func TestParseNumstatBadChunk(t *testing.T) {
	add, del, layers := parseNumstat("/tmp/repo", "not-a-hash\n120\t0\ta.go", testExts())
	if add != 0 || del != 0 || len(layers) != 0 {
		t.Fatalf("bad chunk should be ignored, got %d/%d/%v", add, del, layers)
	}
}

func TestBuildInterval(t *testing.T) {
	// 4 commits: 1 AI-full (100 lines), 1 AI-partial (100), 1 human (100), 1 unlabelled (100)
	commits := []commit{
		{Scope: scopeFull, Add: 100, Layers: map[string]int{"repo/internal/rag": 100}},
		{Scope: scopePartial, Add: 100, Layers: map[string]int{"repo/internal/rag": 100}},
		{Scope: scopeHuman, Add: 100, Layers: map[string]int{"repo/internal/auth": 100}},
		{Scope: scopeUnknown, Add: 100, Layers: map[string]int{"repo/internal/auth": 100}},
	}
	o := options{partialWeight: 0.5, minCoverage: 0.8, exts: testExts()}
	r := build(commits, o, []string{"repo"}, nil, "")
	if r.Commits != 4 || r.Labeled != 3 || r.Unknown != 1 {
		t.Fatalf("commits=%d labeled=%d unknown=%d", r.Commits, r.Labeled, r.Unknown)
	}
	if r.AICommits != 2 || r.Full != 1 || r.Partial != 1 || r.Human != 1 {
		t.Fatalf("scope counts wrong: ai=%d full=%d partial=%d human=%d", r.AICommits, r.Full, r.Partial, r.Human)
	}
	if r.TotalAdded != 400 || r.Lower != 25 || r.Upper != 50 || r.Estimate != 37.5 {
		t.Fatalf("interval wrong: total=%d lower=%.2f upper=%.2f estimate=%.2f", r.TotalAdded, r.Lower, r.Upper, r.Estimate)
	}
	if r.Coverage != 75 {
		t.Errorf("coverage = %.2f, want 75", r.Coverage)
	}
	if len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], "低于阈值") {
		t.Errorf("expected low-coverage warning, got %v", r.Warnings)
	}
	if len(r.Layers) != 2 {
		t.Fatalf("layers = %d, want 2", len(r.Layers))
	}
	var rag *layerStat
	for i := range r.Layers {
		if r.Layers[i].Layer == "repo/internal/rag" {
			rag = &r.Layers[i]
		}
	}
	if rag == nil {
		t.Fatal("rag layer missing")
	}
	if rag.Lower != 50 || rag.Upper != 100 {
		t.Errorf("rag layer interval = [%.1f, %.1f], want [50, 100]", rag.Lower, rag.Upper)
	}
}

func TestBuildEmpty(t *testing.T) {
	o := options{partialWeight: 0.5, minCoverage: 0.8, exts: testExts()}
	r := build(nil, o, []string{"."}, []string{"../missing"}, "")
	if r.Commits != 0 || r.Lower != 0 || r.Upper != 0 {
		t.Fatalf("empty report should be all zero, got %+v", r)
	}
	if len(r.Warnings) < 2 {
		t.Errorf("expected empty-range, coverage, no-line and skipped warnings, got %v", r.Warnings)
	}
}

func TestRenderers(t *testing.T) {
	o := options{partialWeight: 0.5, minCoverage: 0.8, exts: testExts()}
	r := build([]commit{
		{Scope: scopeFull, Add: 50, Layers: map[string]int{"repo/internal/rag": 50}},
		{Scope: scopeHuman, Add: 50, Layers: map[string]int{"repo/internal/auth": 50}},
	}, o, []string{"repo"}, nil, "")
	table := renderTable(r, o)
	if !strings.Contains(table, "AI 代码率区间") || !strings.Contains(table, "[50.0%, 50.0%]") {
		t.Errorf("table missing interval: %s", table)
	}
	md := renderMarkdown(r, o)
	if !strings.Contains(md, "| AI 代码率区间 | [50.0%, 50.0%] |") || !strings.Contains(md, "## 口径说明") {
		t.Errorf("markdown missing key sections: %s", md)
	}
}
