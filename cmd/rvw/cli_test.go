package main

import (
	"math"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

func TestParseArgsAcceptedFormsAndLastOutputWins(t *testing.T) {
	tests := []struct {
		name string
		args []string
		kind source.TargetKind
		mode OutputMode
	}{
		{name: "worktree", args: []string{"diff", "--stdout"}, kind: source.TargetWorktree, mode: OutputHuman},
		{name: "staged", args: []string{"--json", "diff", "--staged"}, kind: source.TargetStaged, mode: OutputJSON},
		{
			name: "range and global filters",
			args: []string{"--include", "src/**", "diff", "--range", "main..HEAD", "--stdout", "--json"},
			kind: source.TargetRange,
			mode: OutputJSON,
		},
		{
			name: "pull request",
			args: []string{"--stdout", "pr", "17", "--repo", "owner/repo", "--json", "--stdout"},
			kind: source.TargetPullRequest,
			mode: OutputHuman,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invocation, err := parseArgs(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if invocation.Target.Kind() != test.kind || invocation.Output != test.mode {
				t.Fatalf("invocation = %+v", invocation)
			}
		})
	}
}

func TestParseArgsPullRequestBoundaries(t *testing.T) {
	maximum := "18446744073709551615"
	for _, selector := range []string{
		"1",
		maximum,
		"https://github.com/owner/repo_name/pull/" + maximum,
	} {
		invocation, err := parseArgs([]string{"pr", selector, "--stdout"})
		if err != nil {
			t.Fatalf("%s: %v", selector, err)
		}
		pull, ok := invocation.Target.PullRequest()
		if !ok || pull.Number().Uint64() == 0 {
			t.Fatalf("%s: invalid target", selector)
		}
		if selector == maximum && pull.Number().Uint64() != math.MaxUint64 {
			t.Fatalf("number = %d", pull.Number().Uint64())
		}
	}
}

func TestParseArgsRejectsInvalidGrammar(t *testing.T) {
	tests := [][]string{
		nil,
		{"diff", "--staged", "--range", "a..b"},
		{"diff", "--range", "a...b"},
		{"diff", "--range", "..b"},
		{"pr", "0"},
		{"pr", "+1"},
		{"pr", "18446744073709551616"},
		{"pr", "https://github.com/-owner/repo/pull/1"},
		{"pr", "1", "--repo", "owner/.."},
		{"diff", "--unknown"},
		{"--staged", "diff"},
	}
	for _, args := range tests {
		if invocation, err := parseArgs(args); err == nil {
			t.Fatalf("parseArgs(%q) = %+v, want error", args, invocation)
		}
	}
}

func TestParseArgsRejectsMismatchedURLRepository(t *testing.T) {
	_, err := parseArgs([]string{
		"pr",
		"https://github.com/owner/repo/pull/1",
		"--repo",
		"other/repo",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("parseArgs() error = %v", err)
	}
}

func TestParseArgsHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"diff", "-h"}, {"help", "pr"}} {
		invocation, err := parseArgs(args)
		if err != nil {
			t.Fatal(err)
		}
		if !invocation.Help {
			t.Fatalf("parseArgs(%q) did not select help", args)
		}
	}
}
