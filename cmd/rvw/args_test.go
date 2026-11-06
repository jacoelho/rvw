//go:build darwin

package main

import (
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

// TestSeedArgument pins which --seed forms name a seed and which are usage errors.
func TestSeedArgument(t *testing.T) {
	t.Parallel()
	accepted := []struct {
		args []string
		want string
	}{
		{[]string{"last", "--seed", "-"}, "-"},
		{[]string{"last", "--seed", "a.json"}, "a.json"},
		{[]string{"last", "--seed=a.json"}, "a.json"},
		{[]string{"last", "--seed", "./-x"}, "./-x"},
		{[]string{"last", "--seed=-x"}, "-x"},
	}
	for _, tc := range accepted {
		cmd, err := parseArgs(tc.args)
		if err != nil {
			t.Errorf("parseArgs(%q): %v; want seed %q", tc.args, err, tc.want)
			continue
		}
		if cmd.seedPath != tc.want {
			t.Errorf("parseArgs(%q) seed = %q; want %q", tc.args, cmd.seedPath, tc.want)
		}
	}

	rejected := [][]string{
		{"last", "--seed"},
		{"last", "--seed", ""},
		{"last", "--seed="},
		{"last", "--seed", "-x"},
		{"last", "--seed", "--json"},
		{"last", "--seed", "a", "--seed", "b"},
		{"last", "--seed=a", "--seed", "b"},
		{"last", "--seed", "a", "--seed=a"},
	}
	for _, args := range rejected {
		if cmd, err := parseArgs(args); err == nil {
			t.Errorf("parseArgs(%q) = seed %q; want a usage error", args, cmd.seedPath)
		} else if !strings.Contains(err.Error(), "--seed") {
			t.Errorf("parseArgs(%q) error %q does not name --seed", args, err)
		}
	}
}

func mustBranch(t *testing.T, ref string, uncommitted bool) source.Source {
	t.Helper()
	src, err := source.Branch(ref, uncommitted)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// TestBranchBaseRefArgument pins which token after branch is the base ref: the next one unless it
// is absent or one of rvw's options, in which case the base ref is origin/HEAD's target.
func TestBranchBaseRefArgument(t *testing.T) {
	t.Parallel()
	originHead := request{originHead: true}
	accepted := []struct {
		args []string
		want request
		json bool
		seed string
	}{
		{args: []string{"branch"}, want: originHead},
		{args: []string{"branch", "--uncommitted"}, want: request{originHead: true, uncommitted: true}},
		{args: []string{"branch", "--json"}, want: originHead, json: true},
		{args: []string{"branch", "--seed", "x"}, want: originHead, seed: "x"},
		{args: []string{"branch", "--seed=x"}, want: originHead, seed: "x"},
		{args: []string{"branch", "refs/heads/--json"}, want: request{exact: mustBranch(t, "refs/heads/--json", false)}},
		{args: []string{"branch", "-x"}, want: request{exact: mustBranch(t, "-x", false)}},
		{args: []string{"branch", "--seedling"}, want: request{exact: mustBranch(t, "--seedling", false)}},
		{args: []string{"--json", "branch"}, want: originHead, json: true},
		{args: []string{"--json", "branch", "main", "--uncommitted"}, want: request{exact: mustBranch(t, "main", true)}, json: true},
	}
	for _, tc := range accepted {
		cmd, err := parseArgs(tc.args)
		if err != nil {
			t.Errorf("parseArgs(%q): %v", tc.args, err)
			continue
		}
		if cmd.help || cmd.request != tc.want || cmd.json != tc.json || cmd.seedPath != tc.seed {
			t.Errorf("parseArgs(%q) = request %+v, json %v, seed %q, help %v; want request %+v, json %v, seed %q",
				tc.args, cmd.request, cmd.json, cmd.seedPath, cmd.help, tc.want, tc.json, tc.seed)
		}
	}

	for _, args := range [][]string{{"branch", "--help"}, {"branch", "-h"}} {
		if cmd, err := parseArgs(args); err != nil || !cmd.help {
			t.Errorf("parseArgs(%q) = help %v, %v; want help", args, cmd.help, err)
		}
	}

	rejected := []struct {
		args []string
		want string
	}{
		{[]string{"branch", "main", "extra"}, "unexpected argument"},
		{[]string{"branch", "--uncommitted", "main"}, "unexpected argument"},
		{[]string{"branch", ""}, "base ref"},
	}
	for _, tc := range rejected {
		if cmd, err := parseArgs(tc.args); err == nil {
			t.Errorf("parseArgs(%q) = request %+v; want a usage error", tc.args, cmd.request)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parseArgs(%q) error %q; want it to contain %q", tc.args, err, tc.want)
		}
	}
}
