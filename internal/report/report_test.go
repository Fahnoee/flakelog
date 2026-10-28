package report

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Fahnoee/flakelog/internal/forge"
	"github.com/Fahnoee/flakelog/internal/lock"
)

// lockFile has two direct inputs on the same nixpkgs revision plus one
// transitive input, all at revision rev.
func lockFile(t *testing.T, rev string) *lock.File {
	t.Helper()
	f, err := lock.Parse([]byte(fmt.Sprintf(`{"nodes":{
		"root":{"inputs":{"nixpkgs":"nixpkgs","nixpkgs-small":"nixpkgs_2","tool":"tool"}},
		"nixpkgs":{"locked":{"type":"github","owner":"NixOS","repo":"nixpkgs","rev":"%[1]s"}},
		"nixpkgs_2":{"locked":{"type":"github","owner":"NixOS","repo":"nixpkgs","rev":"%[1]s"}},
		"tool":{"inputs":{"dep":"dep"}},
		"dep":{"locked":{"type":"github","owner":"o","repo":"dep","rev":"%[1]s"}}
	},"root":"root","version":7}`, rev)))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func newClient(t *testing.T, requests *atomic.Int32, handler http.HandlerFunc) *forge.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return &forge.Client{HTTP: srv.Client(), GitHubAPI: srv.URL, MaxScan: 100}
}

func TestBuild(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ahead","ahead_by":1,"commits":[{"sha":"c1","commit":{"message":"foo: fix CVE-2026-1234"}}]}`)
	}
	rateLimited := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
	}
	tests := []struct {
		name         string
		handler      http.HandlerFunc
		transitive   bool
		wantEntries  int
		wantHidden   int
		wantRequests int32
		wantWarning  bool
	}{
		{name: "dedupes identical ranges", handler: ok, wantEntries: 2, wantHidden: 1, wantRequests: 1},
		{name: "transitive", handler: ok, transitive: true, wantEntries: 3, wantRequests: 2},
		{name: "rate limited", handler: rateLimited, wantEntries: 2, wantHidden: 1, wantRequests: 1, wantWarning: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			client := newClient(t, &requests, tt.handler)
			r := Build(context.Background(), "a", "b", lockFile(t, "aaa"), lockFile(t, "bbb"),
				Options{Client: client, Transitive: tt.transitive})

			if len(r.Entries) != tt.wantEntries || r.HiddenTransitive != tt.wantHidden {
				t.Fatalf("entries=%d hidden=%d, want %d %d", len(r.Entries), r.HiddenTransitive, tt.wantEntries, tt.wantHidden)
			}
			if got := requests.Load(); got != tt.wantRequests {
				t.Errorf("requests = %d, want %d", got, tt.wantRequests)
			}
			if got := len(r.Warnings) > 0; got != tt.wantWarning {
				t.Errorf("warnings = %q, want present=%v", r.Warnings, tt.wantWarning)
			}
			for _, e := range r.Entries {
				if !strings.HasPrefix(e.CompareURL, "https://github.com/") {
					t.Errorf("%s: CompareURL = %q", e.Path, e.CompareURL)
				}
				if tt.wantWarning {
					if e.Range != nil || !strings.Contains(e.RangeError, "rate limit") {
						t.Errorf("%s: Range=%v RangeError=%q", e.Path, e.Range, e.RangeError)
					}
				} else if e.Range == nil || e.Range.Counts[forge.Security] != 1 {
					t.Errorf("%s: Range = %+v, RangeError=%q", e.Path, e.Range, e.RangeError)
				}
			}
		})
	}
}

func TestBuildOffline(t *testing.T) {
	r := Build(context.Background(), "a", "b", lockFile(t, "aaa"), lockFile(t, "bbb"), Options{})
	if len(r.Entries) != 2 || r.HiddenTransitive != 1 || len(r.Warnings) != 0 {
		t.Fatalf("entries=%d hidden=%d warnings=%v", len(r.Entries), r.HiddenTransitive, r.Warnings)
	}
	for _, e := range r.Entries {
		if e.Range != nil || e.RangeError != "" || e.CompareURL == "" {
			t.Errorf("%s: %+v", e.Path, e)
		}
	}
	if got := r.Counts()[lock.Updated]; got != 2 {
		t.Errorf("Counts()[updated] = %d, want 2", got)
	}
}
