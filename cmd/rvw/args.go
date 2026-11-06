//go:build darwin

package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jacoelho/rvw/internal/source"
)

const usage = `Usage:
  rvw last                                review the last commit
  rvw branch [<base-ref>] [--uncommitted] review the commits since <base-ref>, optionally
                                          with uncommitted changes on top
  rvw uncommitted                         review uncommitted changes, untracked files included

The base ref defaults to the target of origin/HEAD. It goes directly after branch; write a ref
named like an option in full (refs/heads/--json).

Options:
  --json             print the review result as JSON instead of Markdown
  --seed <file|->    add agent comments from a JSON seed document (- reads stdin;
                     name a file starting with - as ./-x)
  -h, --help         show this help

rvw serves a review page on 127.0.0.1, opens it in the default browser, and blocks
until the human finishes (exit 0, result on stdout), cancels (exit 3), closes the
review page (exit 4, saved draft kept) or rvw is interrupted (exit 130 or 143, draft
kept). Errors exit 1; usage errors exit 2.
`

type command struct {
	seedPath string
	request  request
	json     bool
	help     bool
}

// request is the source the command line asks for. When originHead is set, the base ref was
// omitted and is origin/HEAD's target, which only the repository can tell; otherwise exact is the
// source.
type request struct {
	exact                   source.Source
	originHead, uncommitted bool
}

// isOption reports whether tok is one of rvw's options. It is the one list of options: the
// argument after "branch" is the base ref unless it is one of these.
func isOption(tok string) bool {
	switch tok {
	case "--uncommitted", "--json", "--seed", "-h", "--help":
		return true
	}
	return strings.HasPrefix(tok, "--seed=")
}

// parseArgs parses the command line. The token directly after "branch" is the base ref unless it
// is absent or an option, so a ref starting with "-" that is not an option is still a ref
// (Definitions: base ref).
func parseArgs(args []string) (command, error) {
	var (
		cmd         command
		seedSet     bool
		kind        string
		baseRef     string
		refGiven    bool
		uncommitted bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		// Checked first, so an option this switch handles but isOption lacks fails here at once
		// rather than being taken as a base ref after branch.
		case !isOption(a) && strings.HasPrefix(a, "-") && a != "-":
			return command{}, fmt.Errorf("unknown option %s", a)
		case a == "-h" || a == "--help":
			cmd.help = true
		case a == "--json":
			cmd.json = true
		case a == "--uncommitted":
			uncommitted = true
		case a == "--seed" || strings.HasPrefix(a, "--seed="):
			if seedSet {
				return command{}, errors.New("--seed given more than once")
			}
			seedSet = true
			path, joined := strings.CutPrefix(a, "--seed=")
			if !joined {
				// A separate value that looks like an option is most likely a missing value; a
				// file named like one is given as --seed=-x or --seed ./-x.
				if i+1 >= len(args) || (strings.HasPrefix(args[i+1], "-") && args[i+1] != "-") {
					return command{}, errors.New("--seed needs a file name or -")
				}
				i++
				path = args[i]
			}
			if path == "" {
				return command{}, errors.New("--seed needs a file name or -")
			}
			cmd.seedPath = path
		case kind == "":
			kind = a
			if kind == "branch" && i+1 < len(args) && !isOption(args[i+1]) {
				i++
				baseRef, refGiven = args[i], true
			}
		default:
			return command{}, fmt.Errorf("unexpected argument %q", a)
		}
	}
	if cmd.help {
		return cmd, nil
	}
	switch kind {
	case "last":
		cmd.request.exact = source.Last()
	case "uncommitted":
		cmd.request.exact = source.Uncommitted()
	case "branch":
		if !refGiven {
			cmd.request = request{originHead: true, uncommitted: uncommitted}
			return cmd, nil
		}
		src, err := source.Branch(baseRef, uncommitted)
		if err != nil {
			return command{}, err
		}
		cmd.request.exact = src
		return cmd, nil
	case "":
		return command{}, errors.New("name a source: last, branch [<base-ref>] or uncommitted")
	default:
		return command{}, fmt.Errorf("unknown source %q", kind)
	}
	if uncommitted {
		return command{}, errors.New("--uncommitted applies only to rvw branch")
	}
	return cmd, nil
}
