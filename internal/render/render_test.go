package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Fahnoee/flakelog/internal/forge"
	"github.com/Fahnoee/flakelog/internal/lock"
	"github.com/Fahnoee/flakelog/internal/report"
)

func sampleReport() *report.Report {
	src := func(rev string, modified int64) *lock.Source {
		return &lock.Source{Type: "github", Owner: "NixOS", Repo: "nixpkgs", Rev: rev, LastModified: modified}
	}
	orig := &lock.Source{Type: "github", Owner: "NixOS", Repo: "nixpkgs", Ref: "nixos-unstable"}
	commit := func(title string, tag forge.Tag) forge.Commit {
		return forge.Commit{SHA: "0123456789abcdef", Title: title, Tags: []forge.Tag{tag}, URL: "https://github.com/NixOS/nixpkgs/commit/0123456"}
	}
	return &report.Report{
		From: "old", To: "new",
		Entries: []*report.Entry{
			{
				Change: lock.Change{
					Path: "nixpkgs", Depth: 1, Kind: lock.Updated,
					Old: &lock.Input{Path: "nixpkgs", Depth: 1, Locked: src("aaaaaaaaaa", 1_700_000_000), Original: orig},
					New: &lock.Input{Path: "nixpkgs", Depth: 1, Locked: src("bbbbbbbbbb", 1_700_000_000+3*86400), Original: orig},
				},
				CompareURL: "https://github.com/NixOS/nixpkgs/compare/aaaaaaaaaa...bbbbbbbbbb",
				Range: &forge.Range{
					Ahead:   1284,
					Scanned: 1284,
					Counts:  map[forge.Tag]int{forge.Security: 1, forge.Revert: 2},
					Bumps:   214,
					Notable: []forge.Commit{
						commit("openssl: fix CVE-2026-1234 (#123)", forge.Security),
						commit("Revert a | b, thanks @someone", forge.Revert),
						commit("Revert foo", forge.Revert),
					},
				},
			},
			{Change: lock.Change{
				Path: "nixpkgs/dep", Depth: 2, Kind: lock.Added,
				New: &lock.Input{Path: "nixpkgs/dep", Depth: 2, Follows: "nixpkgs"},
			}},
		},
		Warnings: []string{"rate limited"},
	}
}

func TestMarkdown(t *testing.T) {
	var buf bytes.Buffer
	Markdown(&buf, sampleReport(), MarkdownOptions{Notable: 8})
	out := buf.String()
	for _, want := range []string{
		"### flake.lock: 1 updated, 1 added",
		"[`aaaaaaa` → `bbbbbbb`](https://github.com/NixOS/nixpkgs/compare/aaaaaaaaaa...bbbbbbbbbb)",
		"| 1,284 commits | 3 days newer | 1 security, 2 reverts, 214 version bumps |",
		"openssl: fix CVE-2026-1234 ([#123](https://redirect.github.com/NixOS/nixpkgs/issues/123))",
		"Revert a \\| b, thanks `@someone`",
		"<details><summary>1 input of inputs changed</summary>",
		"> [!WARNING]\n> rate limited",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, " #123") {
		t.Errorf("bare issue reference left in output:\n%s", out)
	}
}

func TestMarkdownNotableLimit(t *testing.T) {
	var buf bytes.Buffer
	Markdown(&buf, sampleReport(), MarkdownOptions{Notable: 1})
	if out := buf.String(); !strings.Contains(out, "- … and 2 more") || strings.Contains(out, "Revert foo") {
		t.Errorf("notable limit not applied:\n%s", out)
	}
}

func TestCommitTitle(t *testing.T) {
	tests := []struct {
		title, repo, want string
	}{
		{"foo (#123)", "NixOS/nixpkgs", "foo ([#123](https://redirect.github.com/NixOS/nixpkgs/issues/123))"},
		{"#5 first", "o/r", "[#5](https://redirect.github.com/o/r/issues/5) first"},
		{"fix [#7]", "o/r", `fix \[[#7](https://redirect.github.com/o/r/issues/7)\]`},
		{"foo (#123)", "", "foo (`#123`)"},
		{"see x#12", "o/r", "see x#12"},
		{"cc @alice and @org/team", "", "cc `@alice` and `@org/team`"},
		{`Revert "@alice's change"`, "", "Revert \"`@alice`'s change\""},
		{"cc:@carol", "", "cc:`@carol`"},
		{"mail me@example.com", "", "mail me@example.com"},
		{"a | b *c* <d>", "", `a \| b \*c\* \<d>`},
	}
	for _, tt := range tests {
		if got := commitTitle(tt.title, tt.repo); got != tt.want {
			t.Errorf("commitTitle(%q, %q) = %q, want %q", tt.title, tt.repo, got, tt.want)
		}
	}
}

func TestTerm(t *testing.T) {
	tests := []struct {
		color   bool
		escapes bool
	}{{false, false}, {true, true}}
	for _, tt := range tests {
		var buf bytes.Buffer
		Term(&buf, sampleReport(), TermOptions{Color: tt.color, Notable: 8})
		out := buf.String()
		if got := strings.Contains(out, "\x1b["); got != tt.escapes {
			t.Errorf("Color=%v: contains escape codes = %v\n%q", tt.color, got, out)
		}
		for _, want := range []string{"aaaaaaa → bbbbbbb", "1,284 commits", "openssl: fix CVE-2026-1234 (#123)", "warning: "} {
			if !strings.Contains(out, want) {
				t.Errorf("Color=%v: output missing %q\n%s", tt.color, want, out)
			}
		}
	}
}

func TestHumanAge(t *testing.T) {
	day := 24 * time.Hour
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "under an hour"},
		{59 * time.Minute, "under an hour"},
		{time.Hour, "1 hour"},
		{47 * time.Hour, "47 hours"},
		{2 * day, "2 days"},
		{59 * day, "59 days"},
		{60 * day, "2 months"},
		{729 * day, "24 months"},
		{730 * day, "2 years"},
		{-3 * day, "3 days"},
	}
	for _, tt := range tests {
		if got := humanAge(tt.d); got != tt.want {
			t.Errorf("humanAge(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestCommas(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{1284, "1,284"},
		{1234567, "1,234,567"},
		{-1234, "-1,234"},
	}
	for _, tt := range tests {
		if got := commas(tt.n); got != tt.want {
			t.Errorf("commas(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
