package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/Fahnoee/flakelog/internal/lock"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		title, body string
		tags        []Tag
		rank        int
		advisories  []string
		bump        bool
	}{
		{title: "openssl: fix CVE-2026-1234", tags: []Tag{Security}, rank: 0, advisories: []string{"CVE-2026-1234"}},
		{title: "sudo: patch security issue", tags: []Tag{Security}, rank: 1},
		{title: "libfoo: 1.2 -> 1.3", body: "Fixes CVE-2026-9999 and CVE-2026-1111.", tags: []Tag{Security}, rank: 2,
			advisories: []string{"CVE-2026-1111", "CVE-2026-9999"}, bump: true},
		{title: "libfoo: update", body: "See changelog for security fixes", rank: 10},
		{title: "exploitdb: 2026-10-22 -> 2026-10-23", rank: 10, bump: true},
		{title: "btrfs-heatmap: wrap script for security.wrappers", rank: 10},
		{title: "feat!: x", tags: []Tag{Breaking}, rank: 3},
		{title: "feat(api)!: drop v1", tags: []Tag{Breaking}, rank: 3},
		{title: "lib: rework options", body: "BREAKING CHANGE: foo removed", tags: []Tag{Breaking}, rank: 3},
		{title: `Revert "foo: 1.2 -> 1.3"`, tags: []Tag{Revert}, rank: 4},
		{title: "foo: 1.2 -> 1.3", rank: 10, bump: true},
		{title: "python3Packages.foo: 1.2 -> 1.3", rank: 10, bump: true},
		{title: "build(deps): bump foo from 1 to 2", rank: 10, bump: true},
		{title: "docs: tweak readme", rank: 10},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			c := Commit{Title: tt.title}
			classify(&c, tt.body)
			if !slices.Equal(c.Tags, tt.tags) || c.rank != tt.rank || c.Bump != tt.bump || !slices.Equal(c.Advisories, tt.advisories) {
				t.Errorf("got tags=%v rank=%d bump=%v advisories=%v, want tags=%v rank=%d bump=%v advisories=%v",
					c.Tags, c.rank, c.Bump, c.Advisories, tt.tags, tt.rank, tt.bump, tt.advisories)
			}
		})
	}
}

func classified(title, body string) Commit {
	c := Commit{SHA: title, Title: title}
	classify(&c, body)
	return c
}

func TestSummarize(t *testing.T) {
	r := &Range{Commits: []Commit{
		classified("Revert \"bar: 2 -> 3\"", ""),
		classified("feat!: drop thing", ""),
		classified("foo: fix CVE-2026-1234", ""),
		classified("baz: 1 -> 2", "Fixes CVE-2026-5555"),
		classified("foo: fix CVE-2026-1234 (#42)", ""),
		classified("qux: harden against exploits", ""),
		classified("a: 1 -> 2", ""),
		classified("docs: typo", ""),
	}}
	r.summarize()

	var titles []string
	for _, c := range r.Notable {
		titles = append(titles, c.Title)
	}
	want := []string{
		"foo: fix CVE-2026-1234 (#42)",
		"qux: harden against exploits",
		"baz: 1 -> 2",
		"feat!: drop thing",
		"Revert \"bar: 2 -> 3\"",
	}
	if !slices.Equal(titles, want) {
		t.Errorf("notable =\n%q\nwant\n%q", titles, want)
	}
	wantCounts := map[Tag]int{Security: 3, Breaking: 1, Revert: 1}
	for tag, n := range wantCounts {
		if r.Counts[tag] != n {
			t.Errorf("Counts[%s] = %d, want %d", tag, r.Counts[tag], n)
		}
	}
	if r.Bumps != 2 {
		t.Errorf("Bumps = %d, want 2", r.Bumps)
	}
}

func TestSummarizeDuplicateKeepsTags(t *testing.T) {
	r := &Range{Commits: []Commit{
		classified("srelay: 0.4.8 -> 0.4.9", "Fixes CVE-2024-25398"),
		classified("srelay: 0.4.8 -> 0.4.9 (#578062)", ""),
	}}
	r.summarize()
	if len(r.Notable) != 1 {
		t.Fatalf("notable = %d, want 1", len(r.Notable))
	}
	got := r.Notable[0]
	if got.Title != "srelay: 0.4.8 -> 0.4.9 (#578062)" || !slices.Equal(got.Tags, []Tag{Security}) ||
		!slices.Equal(got.Advisories, []string{"CVE-2024-25398"}) {
		t.Errorf("got %+v, want the PR-linked title with the original's tags and advisories", got)
	}
}

func TestSummarizeDuplicateTaggedLater(t *testing.T) {
	r := &Range{Commits: []Commit{
		classified("openssl: 3.0.1 -> 3.0.2", ""),
		classified("openssl: 3.0.1 -> 3.0.2 (#999)", "Fixes CVE-2026-1234"),
	}}
	r.summarize()
	if len(r.Notable) != 1 || r.Counts[Security] != 1 || r.Bumps != 1 {
		t.Fatalf("notable=%d counts=%v bumps=%d, want 1 security fix and 1 bump", len(r.Notable), r.Counts, r.Bumps)
	}
	if got := r.Notable[0].Title; got != "openssl: 3.0.1 -> 3.0.2 (#999)" {
		t.Errorf("title = %q", got)
	}
}

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"x\x1b]52;c;Zm9v\x07":   "x ]52;c;Zm9v",
		"x\r# injected heading": "x # injected heading",
		"  plain  ":             "plain",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTokenOnlyForGitHubDotCom(t *testing.T) {
	c := New("secret", "")
	for host, want := range map[string]bool{"": true, "github.com": true, "attacker.example": false} {
		if got := c.githubHeaders(host).Get("Authorization") != ""; got != want {
			t.Errorf("host %q: token sent = %v, want %v", host, got, want)
		}
	}
}

// compareServer emulates GitHub's paginated compare API for a range of
// ahead commits named c0, c1, ...
func compareServer(t *testing.T, ahead int, requests *[]int) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/NixOS/nixpkgs/compare/{range}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("range") != "aaa...bbb" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected request %s", r.URL)
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		mu.Lock()
		*requests = append(*requests, page)
		mu.Unlock()
		type commit struct {
			SHA    string         `json:"sha"`
			Commit map[string]any `json:"commit"`
		}
		var commits []commit
		for i := (page - 1) * 100; i < min(page*100, ahead); i++ {
			commits = append(commits, commit{SHA: fmt.Sprintf("c%d", i), Commit: map[string]any{"message": fmt.Sprintf("pkg%d: 1 -> 2\n\nbody", i)}})
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ahead", "ahead_by": ahead, "commits": commits})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testSources() (*lock.Source, *lock.Source) {
	return &lock.Source{Type: "github", Owner: "NixOS", Repo: "nixpkgs", Rev: "aaa"},
		&lock.Source{Type: "github", Owner: "NixOS", Repo: "nixpkgs", Rev: "bbb"}
}

func TestCompareGitHubPagination(t *testing.T) {
	tests := []struct {
		maxScan     int
		wantScanned int
		wantPages   []int
	}{
		{maxScan: 500, wantScanned: 250, wantPages: []int{1, 2, 3}},
		{maxScan: 150, wantScanned: 150, wantPages: []int{1, 2}},
		{maxScan: 100, wantScanned: 100, wantPages: []int{1}},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.maxScan), func(t *testing.T) {
			var pages []int
			srv := compareServer(t, 250, &pages)
			c := &Client{HTTP: srv.Client(), GitHubAPI: srv.URL, MaxScan: tt.maxScan}
			o, n := testSources()
			r, err := c.Compare(context.Background(), o, n)
			if err != nil {
				t.Fatal(err)
			}
			if r.Ahead != 250 || r.Scanned != tt.wantScanned || len(r.Commits) != tt.wantScanned {
				t.Fatalf("Ahead=%d Scanned=%d len(Commits)=%d", r.Ahead, r.Scanned, len(r.Commits))
			}
			if got, want := r.Truncated(), tt.wantScanned < 250; got != want {
				t.Errorf("Truncated() = %v, want %v", got, want)
			}
			for i, c := range r.Commits {
				if want := fmt.Sprintf("c%d", i); c.SHA != want {
					t.Fatalf("Commits[%d].SHA = %s, want %s", i, c.SHA, want)
				}
			}
			if r.Commits[0].Title != "pkg0: 1 -> 2" || r.Bumps != tt.wantScanned {
				t.Errorf("title=%q bumps=%d", r.Commits[0].Title, r.Bumps)
			}
			slices.Sort(pages)
			if !slices.Equal(pages, tt.wantPages) {
				t.Errorf("pages requested = %v, want %v", pages, tt.wantPages)
			}
		})
	}
}

func TestCompareAPIErrors(t *testing.T) {
	tests := []struct {
		status      int
		body        string
		rateLimited bool
	}{
		{http.StatusForbidden, `{"message":"API rate limit exceeded for 1.2.3.4."}`, true},
		{http.StatusTooManyRequests, ``, true},
		{http.StatusForbidden, `{"message":"Resource not accessible"}`, false},
		{http.StatusNotFound, `{"message":"Not Found"}`, false},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.status)+tt.body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()
			c := &Client{HTTP: srv.Client(), GitHubAPI: srv.URL, MaxScan: 100}
			o, n := testSources()
			_, err := c.Compare(context.Background(), o, n)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want *APIError", err)
			}
			if apiErr.Status != tt.status || apiErr.RateLimited() != tt.rateLimited {
				t.Errorf("Status=%d RateLimited=%v, want %d %v", apiErr.Status, apiErr.RateLimited(), tt.status, tt.rateLimited)
			}
		})
	}
}

func TestCompareUnsupported(t *testing.T) {
	gh := func(repo, rev string) *lock.Source {
		return &lock.Source{Type: "github", Owner: "o", Repo: repo, Rev: rev}
	}
	tests := []struct {
		name string
		o, n *lock.Source
	}{
		{"different repos", gh("a", "1"), gh("b", "2")},
		{"missing rev", gh("a", ""), gh("a", "2")},
		{"git url", &lock.Source{Type: "git", URL: "u", Rev: "1"}, &lock.Source{Type: "git", URL: "u", Rev: "2"}},
	}
	c := &Client{MaxScan: 100}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := c.Compare(context.Background(), tt.o, tt.n); !errors.Is(err, ErrUnsupported) {
				t.Errorf("err = %v, want ErrUnsupported", err)
			}
		})
	}
}

func TestCompareURL(t *testing.T) {
	src := func(typ, host, owner, repo, rev string) *lock.Source {
		return &lock.Source{Type: typ, Host: host, Owner: owner, Repo: repo, Rev: rev}
	}
	tests := []struct {
		name string
		o, n *lock.Source
		want string
	}{
		{"github", src("github", "", "NixOS", "nixpkgs", "a"), src("github", "", "NixOS", "nixpkgs", "b"),
			"https://github.com/NixOS/nixpkgs/compare/a...b"},
		{"github enterprise", src("github", "ghe.example.com", "o", "r", "a"), src("github", "ghe.example.com", "o", "r", "b"),
			"https://ghe.example.com/o/r/compare/a...b"},
		{"gitlab", src("gitlab", "", "o", "r", "a"), src("gitlab", "", "o", "r", "b"),
			"https://gitlab.com/o/r/-/compare/a...b"},
		{"different repos", src("github", "", "o", "r1", "a"), src("github", "", "o", "r2", "b"), ""},
		{"different types", src("github", "", "o", "r", "a"), src("gitlab", "", "o", "r", "b"), ""},
		{"no rev", src("github", "", "o", "r", ""), src("github", "", "o", "r", "b"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompareURL(tt.o, tt.n); got != tt.want {
				t.Errorf("CompareURL = %q, want %q", got, tt.want)
			}
		})
	}
}
