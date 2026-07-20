package main

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/config"
	"github.com/jacoelho/rvw/internal/source"
)

var ErrUsage = errors.New("invalid command line")

type OutputMode uint8

const (
	OutputInteractive OutputMode = iota
	OutputHuman
	OutputJSON
)

type HelpTopic uint8

const (
	HelpGlobal HelpTopic = iota
	HelpDiff
	HelpPullRequest
)

type Invocation struct {
	Target    source.Target
	Output    OutputMode
	Overrides config.Overrides
	Help      bool
	HelpTopic HelpTopic
}

func parseArgs(args []string) (Invocation, error) {
	for _, argument := range args {
		if !utf8.ValidString(argument) {
			return Invocation{}, fmt.Errorf("%w: argument is not valid UTF-8", ErrUsage)
		}
	}
	if len(args) == 0 {
		return Invocation{}, fmt.Errorf("%w: expected diff, pr, or help", ErrUsage)
	}
	if args[0] == "help" {
		switch {
		case len(args) == 1:
			return Invocation{Help: true, HelpTopic: HelpGlobal}, nil
		case len(args) == 2 && args[1] == "diff":
			return Invocation{Help: true, HelpTopic: HelpDiff}, nil
		case len(args) == 2 && args[1] == "pr":
			return Invocation{Help: true, HelpTopic: HelpPullRequest}, nil
		default:
			return Invocation{}, fmt.Errorf("%w: help accepts only diff or pr", ErrUsage)
		}
	}

	invocation := Invocation{Output: OutputInteractive, HelpTopic: HelpGlobal}
	var command string
	var staged bool
	var rangeText string
	var pullSelector string
	var repository *source.OwnerRepository

	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch argument {
		case "-h", "--help":
			invocation.Help = true
			switch command {
			case "diff":
				invocation.HelpTopic = HelpDiff
			case "pr":
				invocation.HelpTopic = HelpPullRequest
			}
		case "--stdout":
			invocation.Output = OutputHuman
		case "--json":
			invocation.Output = OutputJSON
		case "--no-mouse":
			invocation.Overrides.DisableMouse()
		case "--theme":
			value, next, err := optionValue(args, index, argument)
			if err != nil {
				return Invocation{}, err
			}
			index = next
			theme, err := config.NewThemeName(value)
			if err != nil {
				return Invocation{}, err
			}
			invocation.Overrides.SetTheme(theme)
		case "--appearance":
			value, next, err := optionValue(args, index, argument)
			if err != nil {
				return Invocation{}, err
			}
			index = next
			appearance, err := config.ParseAppearance(value)
			if err != nil {
				return Invocation{}, err
			}
			invocation.Overrides.SetAppearance(appearance)
		case "--include":
			value, next, err := optionValue(args, index, argument)
			if err != nil {
				return Invocation{}, err
			}
			index = next
			pattern, err := config.NewPattern(value)
			if err != nil {
				return Invocation{}, err
			}
			invocation.Overrides.AddInclude(pattern)
		case "--exclude":
			value, next, err := optionValue(args, index, argument)
			if err != nil {
				return Invocation{}, err
			}
			index = next
			pattern, err := config.NewPattern(value)
			if err != nil {
				return Invocation{}, err
			}
			invocation.Overrides.AddExclude(pattern)
		case "--staged":
			if command != "diff" {
				return Invocation{}, fmt.Errorf("%w: --staged is valid only after diff", ErrUsage)
			}
			staged = true
		case "--range":
			if command != "diff" {
				return Invocation{}, fmt.Errorf("%w: --range is valid only after diff", ErrUsage)
			}
			value, next, err := optionValue(args, index, argument)
			if err != nil {
				return Invocation{}, err
			}
			index = next
			rangeText = value
		case "--repo":
			if command != "pr" {
				return Invocation{}, fmt.Errorf("%w: --repo is valid only after pr", ErrUsage)
			}
			value, next, err := optionValue(args, index, argument)
			if err != nil {
				return Invocation{}, err
			}
			index = next
			parsed, err := source.NewOwnerRepository(value)
			if err != nil {
				return Invocation{}, err
			}
			repository = new(parsed)
		default:
			switch {
			case argument == "diff" || argument == "pr":
				if command != "" {
					return Invocation{}, fmt.Errorf("%w: multiple subcommands", ErrUsage)
				}
				command = argument
				if command == "diff" {
					invocation.HelpTopic = HelpDiff
				} else {
					invocation.HelpTopic = HelpPullRequest
				}
			case len(argument) > 0 && argument[0] == '-':
				return Invocation{}, fmt.Errorf("%w: unknown option %s", ErrUsage, argument)
			case command == "pr" && pullSelector == "":
				pullSelector = argument
			default:
				return Invocation{}, fmt.Errorf("%w: unexpected argument %q", ErrUsage, argument)
			}
		}
	}

	if invocation.Help {
		return invocation, nil
	}
	switch command {
	case "diff":
		if staged && rangeText != "" {
			return Invocation{}, fmt.Errorf("%w: --staged and --range are mutually exclusive", ErrUsage)
		}
		switch {
		case staged:
			invocation.Target = source.StagedTarget()
		case rangeText != "":
			target, err := source.NewRangeTarget(rangeText)
			if err != nil {
				return Invocation{}, err
			}
			invocation.Target = target
		default:
			invocation.Target = source.WorktreeTarget()
		}
	case "pr":
		if pullSelector == "" {
			return Invocation{}, fmt.Errorf("%w: pr requires a selector", ErrUsage)
		}
		selector, err := source.NewPullRequestSelector(pullSelector, repository)
		if err != nil {
			return Invocation{}, err
		}
		target, err := source.NewPullRequestTarget(selector)
		if err != nil {
			return Invocation{}, err
		}
		invocation.Target = target
	default:
		return Invocation{}, fmt.Errorf("%w: expected diff or pr", ErrUsage)
	}
	return invocation, nil
}

func optionValue(args []string, index int, option string) (string, int, error) {
	next := index + 1
	if next >= len(args) {
		return "", index, fmt.Errorf("%w: %s requires a value", ErrUsage, option)
	}
	return args[next], next, nil
}

func helpText(topic HelpTopic) string {
	switch topic {
	case HelpDiff:
		return "usage: rvw diff [--staged | --range <base..head>] [common options]\n"
	case HelpPullRequest:
		return "usage: rvw pr <number | url> [--repo <owner/repository>] [common options]\n"
	default:
		return "usage:\n" +
			"  rvw diff [--staged | --range <base..head>] [common options]\n" +
			"  rvw pr <number | url> [--repo <owner/repository>] [common options]\n\n" +
			"common options: --stdout --json --no-mouse --theme <name> " +
			"--appearance <dark|light|system> --include <pattern> --exclude <pattern>\n"
	}
}
