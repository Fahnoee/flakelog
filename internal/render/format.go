// Package render prints reports for terminals, pull requests and machines.
package render

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Fahnoee/flakelog/internal/forge"
	"github.com/Fahnoee/flakelog/internal/lock"
	"github.com/Fahnoee/flakelog/internal/report"
)

func shortRev(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// revLabel names a locked revision: the short rev, else the date, else "?".
func revLabel(s *lock.Source) string {
	switch {
	case s == nil:
		return "?"
	case s.Rev != "":
		return shortRev(s.Rev)
	case !s.Time().IsZero():
		return s.Time().Format("2006-01-02")
	case s.NarHash != "":
		h := strings.TrimPrefix(s.NarHash, "sha256-")
		if len(h) > 8 {
			h = h[:8]
		}
		return h
	}
	return "?"
}

// sourceLabel is the reference the user wrote (original), falling back to locked.
func sourceLabel(in *lock.Input) string {
	if in == nil {
		return ""
	}
	if in.Follows != "" {
		return "follows " + in.Follows
	}
	if in.Original != nil {
		return in.Original.String()
	}
	return in.Locked.String()
}

// humanAge renders a duration in the largest sensible unit: "3 hours", "12 days", "4 months".
func humanAge(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	hours := int(d.Hours())
	days := hours / 24
	switch {
	case hours < 1:
		return "under an hour"
	case hours < 48:
		return plural(hours, "hour")
	case days < 60:
		return plural(days, "day")
	case days < 730:
		return plural(days/30, "month")
	}
	return plural(days/365, "year")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return commas(n) + " " + unit + "s"
}

// commas formats 1284 as "1,284".
func commas(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + commas(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func jumpLabel(e *report.Entry) string {
	j := e.Jump()
	switch {
	case j == 0:
		return ""
	case j < 0:
		return humanAge(j) + " older"
	}
	return humanAge(j) + " newer"
}

func commitsLabel(r *forge.Range) string {
	if r == nil {
		return ""
	}
	switch {
	case r.Behind > 0 && r.Ahead == 0:
		return plural(r.Behind, "commit") + " back"
	case r.Behind > 0:
		return fmt.Sprintf("%s, %s back (diverged)", plural(r.Ahead, "commit"), commas(r.Behind))
	}
	return plural(r.Ahead, "commit")
}

// tagSummary renders "2 security, 1 revert, 214 version bumps".
func tagSummary(r *forge.Range) string {
	if r == nil {
		return ""
	}
	var parts []string
	for _, t := range []forge.Tag{forge.Security, forge.Breaking, forge.Revert} {
		if n := r.Counts[t]; n > 0 {
			label := string(t)
			if t == forge.Revert && n > 1 {
				label = "reverts"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	if r.Bumps > 0 {
		parts = append(parts, plural(r.Bumps, "version bump"))
	}
	return strings.Join(parts, ", ")
}

// bodyAdvisories lists the advisory IDs that only appear in the commit body.
func bodyAdvisories(c forge.Commit) []string {
	var ids []string
	for _, id := range c.Advisories {
		if !strings.Contains(c.Title, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func scannedNote(r *forge.Range) string {
	if r == nil || !r.Truncated() {
		return ""
	}
	return fmt.Sprintf("scanned first %s of %s commits", commas(r.Scanned), commas(r.Ahead))
}

func countsLine(r *report.Report) string {
	c := r.Counts()
	var parts []string
	for _, k := range []lock.Kind{lock.Updated, lock.Retargeted, lock.Added, lock.Removed} {
		if c[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c[k], k))
		}
	}
	if len(parts) == 0 && r.HiddenTransitive > 0 {
		return plural(r.HiddenTransitive, "input") + " of inputs changed"
	}
	if len(parts) == 0 {
		return "no input changes"
	}
	return strings.Join(parts, ", ")
}
