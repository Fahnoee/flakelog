package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/Fahnoee/flakelog/internal/forge"
	"github.com/Fahnoee/flakelog/internal/lock"
	"github.com/Fahnoee/flakelog/internal/report"
)

// TermOptions controls terminal output.
type TermOptions struct {
	Color bool
	// Notable is how many notable commits to list per input.
	Notable int
}

type palette struct{ on bool }

func (p palette) wrap(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p palette) bold(s string) string    { return p.wrap("1", s) }
func (p palette) dim(s string) string     { return p.wrap("2", s) }
func (p palette) red(s string) string     { return p.wrap("31", s) }
func (p palette) green(s string) string   { return p.wrap("32", s) }
func (p palette) yellow(s string) string  { return p.wrap("33", s) }
func (p palette) magenta(s string) string { return p.wrap("35", s) }
func (p palette) cyan(s string) string    { return p.wrap("36", s) }

func (p palette) tag(t forge.Tag) string {
	label := fmt.Sprintf("%-8s", t)
	switch t {
	case forge.Security:
		return p.red(label)
	case forge.Breaking:
		return p.magenta(label)
	case forge.Revert:
		return p.yellow(label)
	default:
		panic(fmt.Sprintf("unhandled tag %q", t))
	}
}

// Term writes a human-readable report.
func Term(w io.Writer, r *report.Report, opts TermOptions) {
	p := palette{opts.Color}
	fmt.Fprintf(w, "%s %s %s %s\n", p.bold("flakelog"), r.From, p.dim("→"), r.To)
	fmt.Fprintln(w, p.dim(countsLine(r)))

	for _, e := range r.Entries {
		fmt.Fprintln(w)
		termEntry(w, p, e, opts)
	}
	if r.HiddenTransitive > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.dim(fmt.Sprintf("+ %s of inputs also changed (--all to show)", plural(r.HiddenTransitive, "input"))))
	}
	for _, warn := range r.Warnings {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.yellow("warning: ")+warn)
	}
}

func termEntry(w io.Writer, p palette, e *report.Entry, opts TermOptions) {
	name := p.bold(e.Path)
	switch e.Kind {
	case lock.Added:
		fmt.Fprintf(w, "%s %s  %s  %s\n", p.green("+"), name, sourceLabel(e.New), p.dim("added at "+revLabel(e.New.Locked)))
		return
	case lock.Removed:
		fmt.Fprintf(w, "%s %s  %s  %s\n", p.red("-"), name, sourceLabel(e.Old), p.dim("removed"))
		return
	case lock.Retargeted:
		fmt.Fprintf(w, "%s %s  %s %s %s\n", p.yellow("~"), name, sourceLabel(e.Old), p.dim("→"), sourceLabel(e.New))
		return
	case lock.Updated:
	default:
		panic(fmt.Sprintf("unhandled change kind %q", e.Kind))
	}

	fmt.Fprintf(w, "%s %s  %s\n", p.cyan("●"), name, p.dim(sourceLabel(e.New)))
	facts := []string{revLabel(e.Old.Locked) + " → " + revLabel(e.New.Locked)}
	if c := commitsLabel(e.Range); c != "" {
		facts = append(facts, c)
	}
	if j := jumpLabel(e); j != "" {
		facts = append(facts, j)
	}
	fmt.Fprintf(w, "  %s\n", strings.Join(facts, p.dim(" · ")))

	if e.RangeError != "" {
		fmt.Fprintf(w, "  %s\n", p.dim("commit details unavailable: "+e.RangeError))
	}
	if s := tagSummary(e.Range); s != "" {
		note := ""
		if n := scannedNote(e.Range); n != "" {
			note = p.dim(" (" + n + ")")
		}
		fmt.Fprintf(w, "  %s%s\n", s, note)
	}
	if e.Range != nil {
		for i, c := range e.Range.Notable {
			if i == opts.Notable {
				fmt.Fprintf(w, "    %s\n", p.dim(fmt.Sprintf("… %d more", len(e.Range.Notable)-i)))
				break
			}
			title := truncate(c.Title, 72)
			if ids := bodyAdvisories(c); len(ids) > 0 {
				title += " " + p.red("("+strings.Join(ids, ", ")+")")
			}
			fmt.Fprintf(w, "    %s %s %s\n", p.tag(c.Tags[0]), title, p.dim(shortRev(c.SHA)))
		}
	}
	if e.CompareURL != "" {
		fmt.Fprintf(w, "  %s\n", p.dim(p.link(e.CompareURL, "compare "+revLabel(e.Old.Locked)+"…"+revLabel(e.New.Locked))))
	}
}

// link renders an OSC 8 hyperlink in color mode and the bare URL otherwise,
// so piped output stays copyable.
func (p palette) link(url, text string) string {
	if !p.on {
		return url
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
