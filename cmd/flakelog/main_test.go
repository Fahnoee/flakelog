package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const (
	oldLock = "../../testdata/old.lock"
	newLock = "../../testdata/new.lock"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr string
	}{
		{name: "version", args: []string{"-version"}, want: "flakelog dev\n"},
		{name: "markdown", args: []string{"-offline", "-f", "md", oldLock, newLock}, want: "### flake.lock: 6 added"},
		{name: "flags after args", args: []string{oldLock, newLock, "-offline", "-color", "never"}, want: "6 added"},
		{name: "unknown format", args: []string{"-offline", "-f", "xml", oldLock, newLock}, wantErr: `unknown format "xml"`},
		{name: "too many args", args: []string{"-offline", oldLock, newLock, newLock}, wantErr: "at most two arguments"},
		{name: "age bad format", args: []string{"age", "-f", "md", newLock}, wantErr: "age supports"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := run(context.Background(), tt.args, &buf)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(buf.String(), tt.want) {
				t.Errorf("output missing %q:\n%s", tt.want, buf.String())
			}
		})
	}
}

func TestRunJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := run(context.Background(), []string{"-offline", "-f", "json", oldLock, newLock}, &buf); err != nil {
		t.Fatal(err)
	}
	var got struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Changes []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"`
		HiddenTransitive int `json:"hiddenTransitive"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if got.From != oldLock || got.To != newLock || len(got.Changes) != 6 || got.HiddenTransitive != 10 {
		t.Errorf("from=%q to=%q changes=%d hidden=%d", got.From, got.To, len(got.Changes), got.HiddenTransitive)
	}
	for _, c := range got.Changes {
		if c.Kind != "added" {
			t.Errorf("%s: kind %q, want added", c.Path, c.Kind)
		}
	}
}
