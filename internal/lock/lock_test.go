package lock

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func mustParse(t *testing.T, s string) *File {
	t.Helper()
	f, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"valid", `{"nodes":{"root":{"inputs":{"nixpkgs":"nixpkgs"}},"nixpkgs":{}},"root":"root","version":7}`, ""},
		{"default root", `{"nodes":{"root":{}},"version":7}`, ""},
		{"missing root", `{"nodes":{"nixpkgs":{}},"root":"root","version":7}`, `root node "root" not found`},
		{"invalid json", `{`, "parse flake.lock"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseFollows(t *testing.T) {
	f := mustParse(t, `{"nodes":{
		"root":{"inputs":{"hm":"home-manager"}},
		"home-manager":{"inputs":{"nixpkgs":["nixpkgs"],"utils":["a","b"]}}
	},"root":"root","version":7}`)
	hm := f.Nodes["home-manager"].Inputs
	if got := hm["nixpkgs"]; got.Node != "" || !slices.Equal(got.Follows, []string{"nixpkgs"}) {
		t.Errorf("nixpkgs ref = %+v", got)
	}
	if got := hm["utils"].Follows; !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("utils follows = %v", got)
	}
	if got := f.Nodes["root"].Inputs["hm"]; got.Node != "home-manager" || got.Follows != nil {
		t.Errorf("hm ref = %+v", got)
	}
}

const graph = `{"nodes":{
	"root":{"inputs":{"home-manager":"home-manager","nixpkgs":"nixpkgs","a":"a","b":"b"}},
	"nixpkgs":{"locked":{"type":"github","owner":"NixOS","repo":"nixpkgs","rev":"n1"}},
	"home-manager":{"inputs":{"nixpkgs":["nixpkgs"]},"locked":{"type":"github","owner":"nix-community","repo":"home-manager","rev":"h1"}},
	"a":{"inputs":{"shared":"shared"},"locked":{"type":"github","owner":"o","repo":"a","rev":"a1"}},
	"b":{"inputs":{"shared":"shared"},"locked":{"type":"github","owner":"o","repo":"b","rev":"b1"}},
	"shared":{"inputs":{"deep":"deep"},"locked":{"type":"github","owner":"o","repo":"shared","rev":"s1"}},
	"deep":{"locked":{"type":"github","owner":"o","repo":"deep","rev":"d1"}}
},"root":"root","version":7}`

func TestInputs(t *testing.T) {
	type row struct {
		path    string
		depth   int
		follows string
		rev     string
	}
	var got []row
	for _, in := range mustParse(t, graph).Inputs() {
		r := row{path: in.Path, depth: in.Depth, follows: in.Follows}
		if in.Locked != nil {
			r.rev = in.Locked.Rev
		}
		got = append(got, r)
	}
	// "shared" is reachable via a and b but only expanded under a, so
	// "b/shared/deep" never appears.
	want := []row{
		{"a", 1, "", "a1"},
		{"b", 1, "", "b1"},
		{"home-manager", 1, "", "h1"},
		{"nixpkgs", 1, "", "n1"},
		{"a/shared", 2, "", "s1"},
		{"b/shared", 2, "", "s1"},
		{"home-manager/nixpkgs", 2, "nixpkgs", ""},
		{"a/shared/deep", 3, "", "d1"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Inputs() =\n%v\nwant\n%v", got, want)
	}
}

func lockWith(nodes string) string {
	return `{"nodes":{` + nodes + `},"root":"root","version":7}`
}

func TestDiffKinds(t *testing.T) {
	const gh = `"type":"github","owner":"o","repo":"r"`
	tests := []struct {
		name     string
		old, new string
		want     []Kind
	}{
		{
			name: "unchanged",
			old:  `"root":{"inputs":{"x":"x"}},"x":{"locked":{` + gh + `,"rev":"1"}}`,
			new:  `"root":{"inputs":{"x":"x"}},"x":{"locked":{` + gh + `,"rev":"1"}}`,
		},
		{
			name: "added",
			old:  `"root":{}`,
			new:  `"root":{"inputs":{"x":"x"}},"x":{"locked":{` + gh + `,"rev":"1"}}`,
			want: []Kind{Added},
		},
		{
			name: "removed",
			old:  `"root":{"inputs":{"x":"x"}},"x":{"locked":{` + gh + `,"rev":"1"}}`,
			new:  `"root":{}`,
			want: []Kind{Removed},
		},
		{
			name: "updated rev",
			old:  `"root":{"inputs":{"x":"x"}},"x":{"locked":{` + gh + `,"rev":"1"},"original":{` + gh + `}}`,
			new:  `"root":{"inputs":{"x":"x"}},"x":{"locked":{` + gh + `,"rev":"2"},"original":{` + gh + `}}`,
			want: []Kind{Updated},
		},
		{
			name: "updated narHash only",
			old:  `"root":{"inputs":{"x":"x"}},"x":{"locked":{"type":"path","path":"/p","narHash":"sha256-a"}}`,
			new:  `"root":{"inputs":{"x":"x"}},"x":{"locked":{"type":"path","path":"/p","narHash":"sha256-b"}}`,
			want: []Kind{Updated},
		},
		{
			name: "retargeted branch",
			old:  `"root":{"inputs":{"x":"x"}},"x":{"locked":{` + gh + `,"rev":"1"},"original":{` + gh + `,"ref":"release-25.05"}}`,
			new:  `"root":{"inputs":{"x":"x"}},"x":{"locked":{` + gh + `,"rev":"2"},"original":{` + gh + `,"ref":"release-25.11"}}`,
			want: []Kind{Retargeted},
		},
		{
			name: "retargeted follows",
			old:  `"root":{"inputs":{"a":"a","b":"b","x":"x"}},"a":{},"b":{},"x":{"inputs":{"dep":["a"]}}`,
			new:  `"root":{"inputs":{"a":"a","b":"b","x":"x"}},"a":{},"b":{},"x":{"inputs":{"dep":["b"]}}`,
			want: []Kind{Retargeted},
		},
		{
			name: "follows to locked",
			old:  `"root":{"inputs":{"a":"a","x":"x"}},"a":{},"x":{"inputs":{"dep":["a"]}}`,
			new:  `"root":{"inputs":{"a":"a","x":"x"}},"a":{},"x":{"inputs":{"dep":"d"}},"d":{"locked":{` + gh + `,"rev":"1"}}`,
			want: []Kind{Retargeted},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []Kind
			for _, c := range Diff(mustParse(t, lockWith(tt.old)), mustParse(t, lockWith(tt.new))) {
				got = append(got, c.Kind)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("kinds = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiffOrdering(t *testing.T) {
	oldFile := mustParse(t, lockWith(`"root":{}`))
	newFile := mustParse(t, lockWith(`
		"root":{"inputs":{"zeta":"z","alpha":"a"}},
		"z":{"inputs":{"dep":"d"}},
		"a":{"inputs":{"dep":"d2"}},
		"d":{},"d2":{}`))
	var got []string
	for _, c := range Diff(oldFile, newFile) {
		got = append(got, c.Path)
	}
	want := []string{"alpha", "zeta", "alpha/dep", "zeta/dep"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestDiffFixtures(t *testing.T) {
	read := func(name string) *File {
		data, err := os.ReadFile("../../testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	var direct []string
	for _, c := range Diff(read("old.lock"), read("new.lock")) {
		if c.Kind != Added {
			t.Errorf("%s: kind %s, want added", c.Path, c.Kind)
		}
		if c.Direct() {
			direct = append(direct, c.Path)
		}
	}
	want := []string{"deploy-rs", "home-manager-unstable", "microvm", "nix-darwin", "nixos-wsl", "nixpkgs-unstable"}
	if !slices.Equal(direct, want) {
		t.Errorf("direct added = %v, want %v", direct, want)
	}
}

func TestSourceString(t *testing.T) {
	tests := []struct {
		src  *Source
		want string
	}{
		{nil, ""},
		{&Source{Type: "github", Owner: "NixOS", Repo: "nixpkgs"}, "github:NixOS/nixpkgs"},
		{&Source{Type: "github", Owner: "NixOS", Repo: "nixpkgs", Ref: "nixos-unstable"}, "github:NixOS/nixpkgs/nixos-unstable"},
		{&Source{Type: "gitlab", Owner: "o", Repo: "r", Host: "gitlab.gnome.org"}, "gitlab:o/r?host=gitlab.gnome.org"},
		{&Source{Type: "git", URL: "https://example.com/r.git"}, "git+https://example.com/r.git"},
		{&Source{Type: "git", URL: "https://example.com/r.git", Ref: "main"}, "git+https://example.com/r.git?ref=main"},
		{&Source{Type: "path", Path: "/home/me/flake"}, "path:/home/me/flake"},
		{&Source{Type: "indirect", ID: "nixpkgs"}, "flake:nixpkgs"},
		{&Source{Type: "indirect", ID: "nixpkgs", Ref: "nixos-25.05"}, "flake:nixpkgs/nixos-25.05"},
	}
	for _, tt := range tests {
		if got := tt.src.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}
