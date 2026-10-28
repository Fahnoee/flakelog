// Package report joins a lock diff with commit history from the forges.
package report

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Fahnoee/flakelog/internal/forge"
	"github.com/Fahnoee/flakelog/internal/lock"
)

// Report is everything the renderers need.
type Report struct {
	From    string   `json:"from"`
	To      string   `json:"to"`
	Entries []*Entry `json:"changes"`
	// HiddenTransitive counts changed inputs-of-inputs left out of Entries.
	HiddenTransitive int `json:"hiddenTransitive,omitempty"`
	// Warnings are problems that did not stop the report, e.g. rate limits.
	Warnings []string `json:"warnings,omitempty"`
}

// Entry is one changed input plus what flakelog learned about it.
type Entry struct {
	lock.Change
	CompareURL string       `json:"compareUrl,omitempty"`
	Range      *forge.Range `json:"range,omitempty"`
	RangeError string       `json:"rangeError,omitempty"`
}

// Jump is how much newer the new revision is than the old one.
// It is negative for a rollback and zero when unknown.
func (e *Entry) Jump() time.Duration {
	if e.Old == nil || e.New == nil || e.Old.Locked == nil || e.New.Locked == nil {
		return 0
	}
	o, n := e.Old.Locked.Time(), e.New.Locked.Time()
	if o.IsZero() || n.IsZero() {
		return 0
	}
	return n.Sub(o)
}

// Counts tallies entries by kind.
func (r *Report) Counts() map[lock.Kind]int {
	m := map[lock.Kind]int{}
	for _, e := range r.Entries {
		m[e.Kind]++
	}
	return m
}

// Options controls Build.
type Options struct {
	// Client fetches commit ranges; nil means offline.
	Client *forge.Client
	// Transitive includes inputs of inputs.
	Transitive bool
	// Concurrency caps parallel API requests.
	Concurrency int
}

// Build diffs two lock files and, unless offline, fetches commit ranges.
func Build(ctx context.Context, from, to string, oldFile, newFile *lock.File, opts Options) *Report {
	r := &Report{From: from, To: to}
	for _, c := range lock.Diff(oldFile, newFile) {
		if !opts.Transitive && !c.Direct() {
			r.HiddenTransitive++
			continue
		}
		e := &Entry{Change: c}
		if c.Kind == lock.Updated {
			e.CompareURL = forge.CompareURL(c.Old.Locked, c.New.Locked)
		}
		r.Entries = append(r.Entries, e)
	}
	if opts.Client != nil {
		r.fetch(ctx, opts)
	}
	return r
}

type rangeKey struct{ repo, from, to string }

type rangeResult struct {
	r   *forge.Range
	err error
}

func (r *Report) fetch(ctx context.Context, opts Options) {
	n := opts.Concurrency
	if n <= 0 {
		n = 4
	}
	jobs := map[rangeKey][]*Entry{}
	var order []rangeKey
	for _, e := range r.Entries {
		if e.Kind != lock.Updated || !lock.SameRepo(e.Old.Locked, e.New.Locked) || e.Old.Locked.Rev == e.New.Locked.Rev {
			continue
		}
		k := rangeKey{e.New.Locked.String(), e.Old.Locked.Rev, e.New.Locked.Rev}
		if _, ok := jobs[k]; !ok {
			order = append(order, k)
		}
		jobs[k] = append(jobs[k], e)
	}

	results := make(map[rangeKey]rangeResult, len(order))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, n)
	for _, k := range order {
		e := jobs[k][0]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rg, err := opts.Client.Compare(ctx, e.Old.Locked, e.New.Locked)
			mu.Lock()
			results[k] = rangeResult{rg, err}
			mu.Unlock()
		}()
	}
	wg.Wait()

	rateLimited := false
	for _, k := range order {
		res := results[k]
		for _, e := range jobs[k] {
			switch {
			case res.err == nil:
				e.Range = res.r
			case errors.Is(res.err, forge.ErrUnsupported):
			default:
				e.RangeError = res.err.Error()
				var apiErr *forge.APIError
				if errors.As(res.err, &apiErr) && apiErr.RateLimited() {
					rateLimited = true
				}
			}
		}
	}
	if rateLimited {
		r.Warnings = append(r.Warnings, "GitHub rate limit hit; set GITHUB_TOKEN (or log in with `gh auth login`) for commit details")
	}
}
