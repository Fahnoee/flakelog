package render

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Fahnoee/flakelog/internal/lock"
)

// AgeRow is one locked input and how old its revision is.
type AgeRow struct {
	Path     string    `json:"path"`
	Source   string    `json:"source"`
	Rev      string    `json:"rev,omitempty"`
	Modified time.Time `json:"lastModified"`
	AgeDays  int       `json:"ageDays"`
}

// AgeRows lists the locked inputs of f, oldest first. Follows aliases are skipped.
func AgeRows(f *lock.File, transitive bool, now time.Time) []AgeRow {
	var rows []AgeRow
	for _, in := range f.Inputs() {
		if in.Follows != "" || in.Locked == nil || (!transitive && !in.Direct()) {
			continue
		}
		t := in.Locked.Time()
		row := AgeRow{Path: in.Path, Source: sourceLabel(&in), Rev: in.Locked.Rev, Modified: t}
		if !t.IsZero() {
			row.AgeDays = int(now.Sub(t).Hours() / 24)
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].AgeDays > rows[j].AgeDays })
	return rows
}

// AgeTerm prints the rows as an aligned table, coloring stale inputs.
func AgeTerm(w io.Writer, rows []AgeRow, color bool, staleDays int) {
	p := palette{color}
	width := len("INPUT")
	for _, r := range rows {
		width = max(width, len(r.Path))
	}
	fmt.Fprintf(w, "%s  %s  %s  %s\n", p.dim(pad("INPUT", width)), p.dim(pad("AGE", 13)), p.dim(pad("REV", 7)), p.dim("SOURCE"))
	stale := 0
	for _, r := range rows {
		age := "unknown"
		if !r.Modified.IsZero() {
			age = humanAge(time.Duration(r.AgeDays) * 24 * time.Hour)
		}
		ageCol := pad(age, 13)
		switch {
		case r.Modified.IsZero():
			ageCol = p.dim(ageCol)
		case r.AgeDays >= staleDays*3:
			ageCol = p.red(ageCol)
			stale++
		case r.AgeDays >= staleDays:
			ageCol = p.yellow(ageCol)
			stale++
		default:
			ageCol = p.green(ageCol)
		}
		rev := shortRev(r.Rev)
		if rev == "" {
			rev = "-"
		}
		fmt.Fprintf(w, "%s  %s  %-7s  %s\n", p.bold(pad(r.Path, width)), ageCol, rev, p.dim(r.Source))
	}
	if stale > 0 {
		fmt.Fprintf(w, "\n%s older than %d days. Update with `nix flake update`.\n", plural(stale, "input"), staleDays)
	}
}

// AgeJSON writes the rows as indented JSON.
func AgeJSON(w io.Writer, rows []AgeRow) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
