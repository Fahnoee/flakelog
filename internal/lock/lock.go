// Package lock parses flake.lock files and diffs them input by input.
package lock

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// File is the on-disk shape of a flake.lock (version 5–7).
type File struct {
	Nodes   map[string]Node `json:"nodes"`
	Root    string          `json:"root"`
	Version int             `json:"version"`
}

// Node is one entry under "nodes".
type Node struct {
	Inputs   map[string]InputRef `json:"inputs"`
	Locked   *Source             `json:"locked"`
	Original *Source             `json:"original"`
	Flake    *bool               `json:"flake"`
}

// InputRef is either a node name or a "follows" path such as ["nixpkgs"].
type InputRef struct {
	Node    string
	Follows []string
}

func (r *InputRef) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '[' {
		return json.Unmarshal(b, &r.Follows)
	}
	return json.Unmarshal(b, &r.Node)
}

// Source is a "locked" or "original" flake reference.
type Source struct {
	Type         string `json:"type"`
	Owner        string `json:"owner,omitempty"`
	Repo         string `json:"repo,omitempty"`
	Host         string `json:"host,omitempty"`
	Ref          string `json:"ref,omitempty"`
	Rev          string `json:"rev,omitempty"`
	Dir          string `json:"dir,omitempty"`
	URL          string `json:"url,omitempty"`
	Path         string `json:"path,omitempty"`
	ID           string `json:"id,omitempty"`
	NarHash      string `json:"narHash,omitempty"`
	LastModified int64  `json:"lastModified,omitempty"`
}

// Time returns when the locked revision was last modified, or the zero time.
func (s *Source) Time() time.Time {
	if s == nil || s.LastModified == 0 {
		return time.Time{}
	}
	return time.Unix(s.LastModified, 0).UTC()
}

// String renders a short flake reference, e.g. "github:NixOS/nixpkgs/nixos-unstable".
func (s *Source) String() string {
	if s == nil {
		return ""
	}
	var ref string
	switch s.Type {
	case "github", "gitlab", "sourcehut":
		ref = s.Type + ":" + s.Owner + "/" + s.Repo
		if s.Ref != "" {
			ref += "/" + s.Ref
		}
		if s.Host != "" {
			ref += "?host=" + s.Host
		}
	case "git", "hg", "tarball", "file":
		ref = s.Type + "+" + s.URL
		if s.Ref != "" {
			ref += "?ref=" + s.Ref
		}
	case "path":
		ref = "path:" + s.Path
	case "indirect":
		ref = "flake:" + s.ID
		if s.Ref != "" {
			ref += "/" + s.Ref
		}
	default:
		ref = s.Type
	}
	if s.Dir != "" {
		ref += "?dir=" + s.Dir
	}
	return ref
}

// SameRepo reports whether two sources point at the same repository, so a
// commit range between their revisions is meaningful.
func SameRepo(a, b *Source) bool {
	if a == nil || b == nil || a.Type != b.Type {
		return false
	}
	switch a.Type {
	case "github", "gitlab", "sourcehut":
		return strings.EqualFold(a.Owner, b.Owner) && strings.EqualFold(a.Repo, b.Repo) && a.Host == b.Host
	case "git", "hg":
		return a.URL == b.URL
	default:
		return false
	}
}

// Parse decodes a flake.lock.
func Parse(data []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse flake.lock: %w", err)
	}
	if f.Root == "" {
		f.Root = "root"
	}
	if _, ok := f.Nodes[f.Root]; !ok {
		return nil, fmt.Errorf("parse flake.lock: root node %q not found", f.Root)
	}
	return &f, nil
}

// Input is a reachable input, addressed by its path from the root flake.
type Input struct {
	// Path is the input path, e.g. "nixpkgs" or "home-manager/nixpkgs".
	Path string `json:"path"`
	// Depth is 1 for direct inputs of the root flake.
	Depth int `json:"depth"`
	// Follows is set when the input is a "follows" alias, e.g. "nixpkgs".
	Follows  string  `json:"follows,omitempty"`
	Locked   *Source `json:"locked,omitempty"`
	Original *Source `json:"original,omitempty"`
}

// Direct reports whether the input is declared by the root flake itself.
func (in Input) Direct() bool { return in.Depth == 1 }

// Inputs walks the input graph breadth-first from the root. Each node's own
// inputs are expanded only at its first (shallowest) path, which keeps the
// walk linear even when many flakes share a dependency.
func (f *File) Inputs() []Input {
	type item struct {
		node  string
		path  string
		depth int
	}
	var out []Input
	expanded := map[string]bool{f.Root: true}
	queue := []item{{node: f.Root}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		n := f.Nodes[cur.node]
		names := make([]string, 0, len(n.Inputs))
		for name := range n.Inputs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			ref := n.Inputs[name]
			path := name
			if cur.path != "" {
				path = cur.path + "/" + name
			}
			in := Input{Path: path, Depth: cur.depth + 1}
			if ref.Follows != nil {
				in.Follows = strings.Join(ref.Follows, "/")
				if in.Follows == "" {
					in.Follows = "(root)"
				}
				out = append(out, in)
				continue
			}
			child, ok := f.Nodes[ref.Node]
			if !ok {
				continue
			}
			in.Locked, in.Original = child.Locked, child.Original
			out = append(out, in)
			if !expanded[ref.Node] {
				expanded[ref.Node] = true
				queue = append(queue, item{node: ref.Node, path: path, depth: cur.depth + 1})
			}
		}
	}
	return out
}
