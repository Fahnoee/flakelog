// Command flakelog explains what changed in a flake.lock.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/Fahnoee/flakelog/internal/forge"
	"github.com/Fahnoee/flakelog/internal/lock"
	"github.com/Fahnoee/flakelog/internal/render"
	"github.com/Fahnoee/flakelog/internal/report"
)

var version = "dev"

const usage = `flakelog: a readable changelog for flake.lock updates

Usage:
  flakelog [flags]              changes since the last commit (HEAD vs working tree)
  flakelog [flags] REV          changes since a git revision, e.g. HEAD~3 or main
  flakelog [flags] OLD NEW      compare two lock files or git revisions
  flakelog age [flags]          how old each locked input is

OLD and NEW are file paths if they exist, otherwise git revisions of the lock file
(REV or REV:path). Commit details come from the GitHub and GitLab APIs; set
GITHUB_TOKEN (or log in with gh) to avoid rate limits.

Flags:
`

type options struct {
	format    string
	lockPath  string
	all       bool
	offline   bool
	notable   int
	maxScan   int
	color     string
	heading   string
	staleDays int
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "flakelog:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	cmd := "diff"
	if len(args) > 0 && (args[0] == "age" || args[0] == "diff") {
		cmd, args = args[0], args[1:]
	}

	var o options
	fs := flag.NewFlagSet("flakelog", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}
	fs.StringVar(&o.format, "format", "term", "output format: term, markdown, json")
	fs.StringVar(&o.format, "f", "term", "shorthand for -format")
	fs.StringVar(&o.lockPath, "lock", "flake.lock", "lock file used for git revisions and the working tree")
	fs.BoolVar(&o.all, "all", false, "include inputs of inputs")
	fs.BoolVar(&o.offline, "offline", false, "don't call forge APIs (no commit details)")
	fs.IntVar(&o.notable, "notable", 8, "notable commits to list per input")
	fs.IntVar(&o.maxScan, "max-scan", 0, "commits to scan per input (default 3000 with a GitHub token, 100 without)")
	fs.StringVar(&o.color, "color", "auto", "color output: auto, always, never")
	fs.StringVar(&o.heading, "heading", "", "markdown heading (default: a summary of the changes)")
	fs.IntVar(&o.staleDays, "stale", 30, "age: days after which an input counts as stale")
	showVersion := fs.Bool("version", false, "print the version and exit")

	positional, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Fprintln(stdout, "flakelog", version)
		return nil
	}

	switch cmd {
	case "age":
		return runAge(stdout, o, positional)
	case "diff":
		return runDiff(ctx, stdout, o, positional)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func runDiff(ctx context.Context, stdout io.Writer, o options, args []string) error {
	var oldSpec, newSpec string
	switch len(args) {
	case 0:
		oldSpec = "HEAD"
	case 1:
		oldSpec = args[0]
	case 2:
		oldSpec, newSpec = args[0], args[1]
	default:
		return errors.New("expected at most two arguments (OLD NEW)")
	}

	oldFile, oldLabel, oldPath, err := load(oldSpec, o.lockPath)
	if err != nil {
		return err
	}
	// "flakelog HEAD:sub/flake.lock" compares against sub/flake.lock in the working tree.
	newLock := o.lockPath
	if newSpec == "" {
		newLock = oldPath
	}
	newFile, newLabel, _, err := load(newSpec, newLock)
	if err != nil {
		return err
	}

	opts := report.Options{Transitive: o.all}
	if !o.offline {
		token := githubToken()
		c := forge.New(token, os.Getenv("GITLAB_TOKEN"))
		switch {
		case o.maxScan > 0:
			c.MaxScan = o.maxScan
		case token != "":
			c.MaxScan = 3000
		default:
			// 60 requests an hour unauthenticated: one page per input.
			c.MaxScan = 100
		}
		opts.Client = c
	}
	r := report.Build(ctx, oldLabel, newLabel, oldFile, newFile, opts)

	switch o.format {
	case "term", "text":
		render.Term(stdout, r, render.TermOptions{Color: useColor(o.color, stdout), Notable: o.notable})
	case "markdown", "md":
		render.Markdown(stdout, r, render.MarkdownOptions{Notable: o.notable, Heading: o.heading})
	case "json":
		return render.JSON(stdout, r)
	default:
		return fmt.Errorf("unknown format %q (want term, markdown or json)", o.format)
	}
	return nil
}

func runAge(stdout io.Writer, o options, args []string) error {
	spec := ""
	switch len(args) {
	case 0:
	case 1:
		spec = args[0]
	default:
		return errors.New("age takes at most one argument (a lock file or git revision)")
	}
	f, _, _, err := load(spec, o.lockPath)
	if err != nil {
		return err
	}
	rows := render.AgeRows(f, o.all, time.Now())
	switch o.format {
	case "term", "text":
		render.AgeTerm(stdout, rows, useColor(o.color, stdout), o.staleDays)
	case "json":
		return render.AgeJSON(stdout, rows)
	default:
		return fmt.Errorf("age supports -format term or json, not %q", o.format)
	}
	return nil
}

// load reads a lock file from a path, a git revision ("REV" or "REV:path"),
// or, for an empty spec, the working tree. It returns the file, a label and
// the lock file path it resolved to.
func load(spec, lockPath string) (*lock.File, string, string, error) {
	if spec == "" {
		data, err := os.ReadFile(lockPath)
		if err != nil {
			return nil, "", "", err
		}
		f, err := lock.Parse(data)
		return f, "working tree", lockPath, err
	}
	if st, err := os.Stat(spec); err == nil && !st.IsDir() {
		data, err := os.ReadFile(spec)
		if err != nil {
			return nil, "", "", err
		}
		f, err := lock.Parse(data)
		if err != nil {
			return nil, "", "", fmt.Errorf("%s: %w", spec, err)
		}
		return f, spec, lockPath, nil
	}

	rev, path := spec, lockPath
	if r, p, ok := strings.Cut(spec, ":"); ok && p != "" {
		rev, path = r, p
	}
	data, err := gitShow(rev, path)
	if err != nil {
		return nil, "", "", err
	}
	f, err := lock.Parse(data)
	if err != nil {
		return nil, "", "", fmt.Errorf("%s: %w", spec, err)
	}
	return f, rev, path, nil
}

func gitShow(rev, path string) ([]byte, error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	var stderr bytes.Buffer
	cmd := exec.Command("git", "-C", dir, "show", rev+":./"+base)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s is not a file, and git can't read %s at %s: %s", rev, path, rev, msg)
	}
	return out, nil
}

func githubToken() string {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return ""
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func useColor(mode string, w io.Writer) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// parseInterspersed lets flags appear before, between or after positional args.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) < len(args) && args[len(args)-len(rest)-1] == "--" {
			return append(positional, rest...), nil
		}
		args = rest
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}
