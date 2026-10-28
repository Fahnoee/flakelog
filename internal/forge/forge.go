// Package forge fetches the commits between two locked revisions from the
// hosting forge (GitHub or GitLab) and picks out the ones worth reading.
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Fahnoee/flakelog/internal/lock"
)

// Commit is one commit in a range, reduced to what a reviewer needs.
type Commit struct {
	SHA    string    `json:"sha"`
	Title  string    `json:"title"`
	Author string    `json:"author,omitempty"`
	Date   time.Time `json:"date"`
	URL    string    `json:"url,omitempty"`
	Tags   []Tag     `json:"tags,omitempty"`
	// Advisories are CVE and GHSA IDs mentioned in the message.
	Advisories []string `json:"advisories,omitempty"`
	Bump       bool     `json:"bump,omitempty"`
	rank       int
}

// Range is the commit history between two revisions of the same repo.
type Range struct {
	// Status is "ahead", "behind", "diverged" or "identical" (GitHub only).
	Status string `json:"status,omitempty"`
	// Ahead is the number of commits in new that are not in old.
	Ahead int `json:"ahead"`
	// Behind is the number of commits in old that are not in new (a rollback).
	Behind int `json:"behind,omitempty"`
	// Commits are the scanned commits, at most Client.MaxScan of them.
	Commits []Commit    `json:"-"`
	Notable []Commit    `json:"notable,omitempty"`
	Counts  map[Tag]int `json:"counts,omitempty"`
	Bumps   int         `json:"bumps,omitempty"`
	Scanned int         `json:"scanned"`
}

// Truncated reports whether only part of the range was scanned.
func (r *Range) Truncated() bool { return r.Scanned < r.Ahead }

// ErrUnsupported is returned for sources without a known compare API.
var ErrUnsupported = errors.New("no compare API for this source")

// Client talks to forge APIs. The zero value is not usable; use New.
type Client struct {
	HTTP        *http.Client
	GitHubToken string
	GitLabToken string
	// GitHubAPI overrides the api.github.com base URL (used in tests).
	GitHubAPI string
	// MaxScan caps how many commits are fetched per input.
	MaxScan int
}

// New returns a client with sensible defaults.
func New(githubToken, gitlabToken string) *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: 30 * time.Second},
		GitHubToken: githubToken,
		GitLabToken: gitlabToken,
		MaxScan:     500,
	}
}

// CompareURL returns a web page showing the changes between two revisions,
// or "" when the forge has none.
func CompareURL(o, n *lock.Source) string {
	if !lock.SameRepo(o, n) || o.Rev == "" || n.Rev == "" || o.Rev == n.Rev {
		return ""
	}
	switch o.Type {
	case "github":
		return fmt.Sprintf("https://%s/%s/%s/compare/%s...%s", hostOr(o.Host, "github.com"), o.Owner, o.Repo, o.Rev, n.Rev)
	case "gitlab":
		return fmt.Sprintf("https://%s/%s/%s/-/compare/%s...%s", hostOr(o.Host, "gitlab.com"), o.Owner, o.Repo, o.Rev, n.Rev)
	case "sourcehut":
		return fmt.Sprintf("https://%s/%s/%s/log/%s", hostOr(o.Host, "git.sr.ht"), o.Owner, o.Repo, n.Rev)
	}
	return ""
}

// CommitURL returns a web page for a single revision, or "".
func CommitURL(s *lock.Source) string {
	if s == nil || s.Rev == "" {
		return ""
	}
	switch s.Type {
	case "github":
		return fmt.Sprintf("https://%s/%s/%s/commit/%s", hostOr(s.Host, "github.com"), s.Owner, s.Repo, s.Rev)
	case "gitlab":
		return fmt.Sprintf("https://%s/%s/%s/-/commit/%s", hostOr(s.Host, "gitlab.com"), s.Owner, s.Repo, s.Rev)
	case "sourcehut":
		return fmt.Sprintf("https://%s/%s/%s/commit/%s", hostOr(s.Host, "git.sr.ht"), s.Owner, s.Repo, s.Rev)
	}
	return ""
}

func hostOr(host, def string) string {
	if host == "" {
		return def
	}
	return host
}

// Compare fetches the commits between two revisions of the same repository.
func (c *Client) Compare(ctx context.Context, o, n *lock.Source) (*Range, error) {
	if !lock.SameRepo(o, n) || o.Rev == "" || n.Rev == "" {
		return nil, ErrUnsupported
	}
	var (
		r   *Range
		err error
	)
	switch o.Type {
	case "github":
		r, err = c.github(ctx, o, n)
	case "gitlab":
		r, err = c.gitlab(ctx, o, n)
	default:
		return nil, ErrUnsupported
	}
	if err != nil {
		return nil, err
	}
	r.summarize()
	return r, nil
}

type githubCompare struct {
	Status   string `json:"status"`
	AheadBy  int    `json:"ahead_by"`
	BehindBy int    `json:"behind_by"`
	Commits  []struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Message string `json:"message"`
			Author  struct {
				Name string    `json:"name"`
				Date time.Time `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	} `json:"commits"`
}

func (c *Client) github(ctx context.Context, o, n *lock.Source) (*Range, error) {
	api := c.GitHubAPI
	if api == "" {
		api = "https://api.github.com"
		if o.Host != "" && o.Host != "github.com" {
			api = "https://" + o.Host + "/api/v3"
		}
	}
	const perPage = 100
	fetch := func(page int) (*githubCompare, error) {
		u := fmt.Sprintf("%s/repos/%s/%s/compare/%s...%s?per_page=%d&page=%d",
			strings.TrimRight(api, "/"), url.PathEscape(o.Owner), url.PathEscape(o.Repo), o.Rev, n.Rev, perPage, page)
		var resp githubCompare
		if err := c.getJSON(ctx, u, c.githubHeaders(o.Host), &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	}

	first, err := fetch(1)
	if err != nil {
		return nil, err
	}
	r := &Range{Status: first.Status, Ahead: first.AheadBy, Behind: first.BehindBy}
	pages := []*githubCompare{first}

	// Page 1 reports the total, so the remaining pages can be fetched in parallel.
	want := min(r.Ahead, c.MaxScan)
	if len(first.Commits) == perPage && want > perPage {
		rest := make([]*githubCompare, (want+perPage-1)/perPage-1)
		errs := make([]error, len(rest))
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for i := range rest {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				rest[i], errs[i] = fetch(i + 2)
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return nil, err
			}
		}
		pages = append(pages, rest...)
	}

	for _, p := range pages {
		for _, gc := range p.Commits {
			if len(r.Commits) == c.MaxScan {
				break
			}
			title, body, _ := strings.Cut(gc.Commit.Message, "\n")
			cm := Commit{
				SHA:    gc.SHA,
				Title:  cleanTitle(title),
				Author: gc.Commit.Author.Name,
				Date:   gc.Commit.Author.Date,
				URL:    gc.HTMLURL,
			}
			classify(&cm, body)
			r.Commits = append(r.Commits, cm)
		}
	}
	r.Scanned = len(r.Commits)
	return r, nil
}

// githubHeaders only attaches the token for github.com: lock files of
// third-party flakes can name any host, and must not receive it.
func (c *Client) githubHeaders(host string) http.Header {
	h := http.Header{}
	h.Set("Accept", "application/vnd.github+json")
	h.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.GitHubToken != "" && (host == "" || host == "github.com") {
		h.Set("Authorization", "Bearer "+c.GitHubToken)
	}
	return h
}

var controlRe = regexp.MustCompile(`\p{Cc}+`)

// cleanTitle drops control characters: titles are untrusted and end up in
// terminals (escape sequences) and Markdown (a lone \r starts a new block).
func cleanTitle(s string) string {
	return strings.TrimSpace(controlRe.ReplaceAllString(s, " "))
}

type gitlabCompare struct {
	Commits []struct {
		ID         string    `json:"id"`
		Title      string    `json:"title"`
		Message    string    `json:"message"`
		AuthorName string    `json:"author_name"`
		CreatedAt  time.Time `json:"created_at"`
		WebURL     string    `json:"web_url"`
	} `json:"commits"`
}

func (c *Client) gitlab(ctx context.Context, o, n *lock.Source) (*Range, error) {
	project := url.PathEscape(o.Owner + "/" + o.Repo)
	u := fmt.Sprintf("https://%s/api/v4/projects/%s/repository/compare?from=%s&to=%s&straight=true",
		hostOr(o.Host, "gitlab.com"), project, o.Rev, n.Rev)
	h := http.Header{}
	if c.GitLabToken != "" && (o.Host == "" || o.Host == "gitlab.com") {
		h.Set("PRIVATE-TOKEN", c.GitLabToken)
	}
	var resp gitlabCompare
	if err := c.getJSON(ctx, u, h, &resp); err != nil {
		return nil, err
	}
	r := &Range{Ahead: len(resp.Commits)}
	// GitLab lists newest first; keep oldest first like GitHub.
	for i := len(resp.Commits) - 1; i >= 0; i-- {
		gc := resp.Commits[i]
		_, body, _ := strings.Cut(gc.Message, "\n")
		cm := Commit{SHA: gc.ID, Title: cleanTitle(gc.Title), Author: gc.AuthorName, Date: gc.CreatedAt, URL: gc.WebURL}
		classify(&cm, body)
		r.Commits = append(r.Commits, cm)
		if len(r.Commits) >= c.MaxScan {
			break
		}
	}
	r.Scanned = len(r.Commits)
	return r, nil
}

// APIError is a non-2xx response from a forge.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// RateLimited reports whether the forge refused the request for rate limiting.
func (e *APIError) RateLimited() bool {
	return e.Status == http.StatusTooManyRequests ||
		(e.Status == http.StatusForbidden && strings.Contains(strings.ToLower(e.Message), "rate limit"))
}

func (c *Client) getJSON(ctx context.Context, u string, h http.Header, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header = h
	req.Header.Set("User-Agent", "flakelog")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &msg)
		return &APIError{Status: resp.StatusCode, Message: msg.Message}
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
