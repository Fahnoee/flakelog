package lock

import "sort"

// Kind classifies how an input changed between two lock files.
type Kind string

const (
	Added   Kind = "added"
	Removed Kind = "removed"
	Updated Kind = "updated"
	// Retargeted means the input now points at a different source
	// (another repo, a new branch, or switched to/from "follows").
	Retargeted Kind = "retargeted"
)

// Change is one input that differs between the old and new lock.
type Change struct {
	Path  string `json:"path"`
	Depth int    `json:"depth"`
	Kind  Kind   `json:"kind"`
	Old   *Input `json:"old,omitempty"`
	New   *Input `json:"new,omitempty"`
}

// Direct reports whether the changed input is declared by the root flake.
func (c Change) Direct() bool { return c.Depth == 1 }

// Diff compares two lock files and returns every changed input, direct
// inputs first, then by path.
func Diff(oldFile, newFile *File) []Change {
	oldInputs := index(oldFile)
	newInputs := index(newFile)

	var changes []Change
	for path, n := range newInputs {
		o, ok := oldInputs[path]
		if !ok {
			changes = append(changes, Change{Path: path, Depth: n.Depth, Kind: Added, New: n})
			continue
		}
		if kind, changed := compare(o, n); changed {
			changes = append(changes, Change{Path: path, Depth: n.Depth, Kind: kind, Old: o, New: n})
		}
	}
	for path, o := range oldInputs {
		if _, ok := newInputs[path]; !ok {
			changes = append(changes, Change{Path: path, Depth: o.Depth, Kind: Removed, Old: o})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Depth != changes[j].Depth {
			return changes[i].Depth < changes[j].Depth
		}
		return changes[i].Path < changes[j].Path
	})
	return changes
}

func index(f *File) map[string]*Input {
	m := map[string]*Input{}
	if f == nil {
		return m
	}
	for _, in := range f.Inputs() {
		m[in.Path] = &in
	}
	return m
}

func compare(o, n *Input) (Kind, bool) {
	if o.Follows != "" || n.Follows != "" {
		return Retargeted, o.Follows != n.Follows
	}
	if o.Locked == nil || n.Locked == nil {
		return Retargeted, (o.Locked == nil) != (n.Locked == nil)
	}
	if o.Original.String() != n.Original.String() {
		return Retargeted, true
	}
	if o.Locked.Rev != n.Locked.Rev || o.Locked.NarHash != n.Locked.NarHash {
		return Updated, true
	}
	return "", false
}
