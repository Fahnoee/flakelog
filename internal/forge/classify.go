package forge

import (
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Tag marks a commit worth a reviewer's attention.
type Tag string

const (
	Security Tag = "security"
	Breaking Tag = "breaking"
	Revert   Tag = "revert"
)

var (
	advisoryRe = regexp.MustCompile(`\b(?:CVE-\d{4}-\d{4,}|GHSA(?:-[23456789cfghjmpqrvwx]{4}){3})\b`)
	securityRe = regexp.MustCompile(`(?i)\bsecurity\b|\bvulnerab|\bexploit(s|able|ed)?\b`)
	breakingRe = regexp.MustCompile(`(?i)\bbreaking\b|^\w+(\([^)]*\))?!:`)
	// NixOS option paths such as security.wrappers or security.sudo.
	optionPathRe = regexp.MustCompile(`\bsecurity\.[\w.-]+`)
	revertRe     = regexp.MustCompile(`(?i)^revert\b`)
	prSuffixRe   = regexp.MustCompile(`\s*\(#\d+\)$`)
	// nixpkgs style "foo: 1.2 -> 1.3" and dependabot style "bump foo from 1.2 to 1.3".
	bumpRe = regexp.MustCompile(`^[\w.+-]+(/[\w.+-]+)*: \S+ -> \S+|(?i)\bbump \S+ from \S+ to \S+`)
)

// classify tags a commit. Bodies often link changelogs that merely mention
// "security", so in the body only advisory IDs count.
func classify(c *Commit, body string) {
	c.Advisories = advisories(c.Title + "\n" + body)
	switch {
	case advisoryRe.MatchString(c.Title):
		c.rank = 0
	case securityRe.MatchString(optionPathRe.ReplaceAllString(c.Title, "")):
		c.rank = 1
	case len(c.Advisories) > 0:
		c.rank = 2
	default:
		c.rank = 10
	}
	if c.rank < 10 {
		c.Tags = append(c.Tags, Security)
	}
	if breakingRe.MatchString(c.Title) || strings.Contains(body, "BREAKING CHANGE") {
		c.Tags = append(c.Tags, Breaking)
		c.rank = min(c.rank, 3)
	}
	if revertRe.MatchString(c.Title) {
		c.Tags = append(c.Tags, Revert)
		c.rank = min(c.rank, 4)
	}
	c.Bump = bumpRe.MatchString(c.Title)
}

func advisories(text string) []string {
	ids := advisoryRe.FindAllString(text, -1)
	slices.Sort(ids)
	return slices.Compact(ids)
}

// summarize fills in the notable commits and counters of a range.
func (r *Range) summarize() {
	r.Notable = nil
	r.Counts = map[Tag]int{}
	r.Bumps = 0
	// Squash-merge workflows land "foo: fix" and then "foo: fix (#123)";
	// merge them into one change, keeping whichever copy has tags and the
	// title that links the PR.
	index := map[string]int{}
	var changes []Commit
	for _, c := range r.Commits {
		key := prSuffixRe.ReplaceAllString(c.Title, "")
		i, dup := index[key]
		if !dup {
			index[key] = len(changes)
			changes = append(changes, c)
			continue
		}
		g := &changes[i]
		if len(g.Tags) == 0 && len(c.Tags) > 0 {
			g.Tags, g.Advisories, g.rank = c.Tags, c.Advisories, c.rank
		}
		if c.Title != key {
			g.SHA, g.Title, g.URL = c.SHA, c.Title, c.URL
		}
		g.Bump = g.Bump || c.Bump
	}
	for _, c := range changes {
		for _, t := range c.Tags {
			r.Counts[t]++
		}
		if len(c.Tags) > 0 {
			r.Notable = append(r.Notable, c)
		}
		if c.Bump {
			r.Bumps++
		}
	}
	sort.SliceStable(r.Notable, func(i, j int) bool { return r.Notable[i].rank < r.Notable[j].rank })
}
