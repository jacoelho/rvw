// Package main provides the rvw command-line application.
package main

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/config"
	"github.com/jacoelho/rvw/internal/source"
)

const (
	commandHelp    = "help"
	commandDiff    = "diff"
	flagHelp       = "--help"
	flagStdout     = "--stdout"
	flagJSON       = "--json"
	flagStaged     = "--staged"
	flagRange      = "--range"
	flagRepository = "--repo"
	flagInclude    = "--include"
	flagExclude    = "--exclude"
)

// ErrUsage identifies an usage failure.
var ErrUsage = errors.New("invalid command line")

// OutputMode describes output mode.
type OutputMode uint8

// OutputInteractive identifies a supported output interactive value.
const (
	OutputInteractive OutputMode = iota
	OutputHuman
	OutputJSON
)

// HelpTopic represents help topic.
type HelpTopic uint8

// HelpGlobal identifies a supported help global value.
const (
	HelpGlobal HelpTopic = iota
	HelpDiff
	HelpPullRequest
)

// Invocation represents invocation.
type Invocation struct {
	Target    source.Target
	Overrides config.Overrides
	Output    OutputMode
	Help      bool
	HelpTopic HelpTopic
}

type argumentState struct {
	repositoryText string
	command        string
	rangeText      string
	pullSelector   string
	invocation     Invocation
	repositorySet  bool
	rangeSet       bool
	pullSet        bool
	staged         bool
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
	if args[0] == commandHelp {
		return parseHelpArgs(args)
	}

	state := argumentState{
		invocation: Invocation{Output: OutputInteractive, HelpTopic: HelpGlobal},
	}
	consumedThrough := -1
	for index := range args {
		if index <= consumedThrough {
			continue
		}
		consumedThrough = index
		if err := state.consume(args, &consumedThrough); err != nil {
			return Invocation{}, err
		}
	}
	return state.finish()
}

func parseHelpArgs(args []string) (Invocation, error) {
	switch {
	case len(args) == 1:
		return Invocation{Help: true, HelpTopic: HelpGlobal}, nil
	case len(args) == 2 && args[1] == commandDiff:
		return Invocation{Help: true, HelpTopic: HelpDiff}, nil
	case len(args) == 2 && args[1] == "pr":
		return Invocation{Help: true, HelpTopic: HelpPullRequest}, nil
	default:
		return Invocation{}, fmt.Errorf("%w: help accepts only diff or pr", ErrUsage)
	}
}

func (state *argumentState) consume(args []string, index *int) error {
	handled, err := state.consumeCommonOption(args, index)
	if handled || err != nil {
		return err
	}
	return state.consumeTargetArgument(args, index)
}

func (state *argumentState) consumeCommonOption(args []string, index *int) (bool, error) {
	argument := args[*index]
	switch argument {
	case "-h", flagHelp:
		state.invocation.Help = true
		switch state.command {
		case commandDiff:
			state.invocation.HelpTopic = HelpDiff
		case "pr":
			state.invocation.HelpTopic = HelpPullRequest
		}
	case flagStdout:
		state.invocation.Output = OutputHuman
	case flagJSON:
		state.invocation.Output = OutputJSON
	case "--no-mouse":
		state.invocation.Overrides.DisableMouse()
	case "--theme":
		value, next, err := optionValue(args, *index, argument)
		if err != nil {
			return true, err
		}
		*index = next
		theme, err := config.NewThemeName(value)
		if err != nil {
			return true, err
		}
		state.invocation.Overrides.SetTheme(theme)
	case "--appearance":
		value, next, err := optionValue(args, *index, argument)
		if err != nil {
			return true, err
		}
		*index = next
		appearance, err := config.ParseAppearance(value)
		if err != nil {
			return true, err
		}
		state.invocation.Overrides.SetAppearance(appearance)
	case flagInclude, flagExclude:
		value, next, err := optionValue(args, *index, argument)
		if err != nil {
			return true, err
		}
		*index = next
		pattern, err := config.NewPattern(value)
		if err != nil {
			return true, err
		}
		if argument == flagInclude {
			state.invocation.Overrides.AddInclude(pattern)
		} else {
			state.invocation.Overrides.AddExclude(pattern)
		}
	default:
		return false, nil
	}
	return true, nil
}

func (state *argumentState) consumeTargetArgument(args []string, index *int) error {
	argument := args[*index]
	switch argument {
	case flagStaged:
		if state.command != commandDiff {
			return fmt.Errorf("%w: --staged is valid only after diff", ErrUsage)
		}
		state.staged = true
	case flagRange:
		if state.command != commandDiff {
			return fmt.Errorf("%w: --range is valid only after diff", ErrUsage)
		}
		value, next, err := optionValue(args, *index, argument)
		if err != nil {
			return err
		}
		*index = next
		state.rangeText = value
		state.rangeSet = true
	case flagRepository:
		if state.command != "pr" {
			return fmt.Errorf("%w: --repo is valid only after pr", ErrUsage)
		}
		value, next, err := optionValue(args, *index, argument)
		if err != nil {
			return err
		}
		*index = next
		state.repositoryText = value
		state.repositorySet = true
	default:
		return state.consumePositional(argument)
	}
	return nil
}

func (state *argumentState) consumePositional(argument string) error {
	switch {
	case argument == commandDiff || argument == "pr":
		if state.command != "" {
			return fmt.Errorf("%w: multiple subcommands", ErrUsage)
		}
		state.command = argument
		if state.command == commandDiff {
			state.invocation.HelpTopic = HelpDiff
		} else {
			state.invocation.HelpTopic = HelpPullRequest
		}
	case argument != "" && argument[0] == '-':
		return fmt.Errorf("%w: unknown option %s", ErrUsage, argument)
	case state.command == "pr" && !state.pullSet:
		state.pullSelector = argument
		state.pullSet = true
	default:
		return fmt.Errorf("%w: unexpected argument %q", ErrUsage, argument)
	}
	return nil
}

func (state *argumentState) finish() (Invocation, error) {
	if state.invocation.Help {
		return state.invocation, nil
	}
	switch state.command {
	case commandDiff:
		if state.staged && state.rangeSet {
			return Invocation{}, fmt.Errorf("%w: --staged and --range are mutually exclusive", ErrUsage)
		}
		switch {
		case state.staged:
			state.invocation.Target = source.StagedTarget()
		case state.rangeSet:
			target, err := source.NewRangeTarget(state.rangeText)
			if err != nil {
				return Invocation{}, err
			}
			state.invocation.Target = target
		default:
			state.invocation.Target = source.WorktreeTarget()
		}
	case "pr":
		if !state.pullSet || state.pullSelector == "" {
			return Invocation{}, fmt.Errorf("%w: pr requires a selector", ErrUsage)
		}
		var repository *source.OwnerRepository
		if state.repositorySet {
			parsed, err := source.NewOwnerRepository(state.repositoryText)
			if err != nil {
				return Invocation{}, err
			}
			repository = new(parsed)
		}
		selector, err := source.NewPullRequestSelector(state.pullSelector, repository)
		if err != nil {
			return Invocation{}, err
		}
		target, err := source.NewPullRequestTarget(selector)
		if err != nil {
			return Invocation{}, err
		}
		state.invocation.Target = target
	default:
		return Invocation{}, fmt.Errorf("%w: expected diff or pr", ErrUsage)
	}
	return state.invocation, nil
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
		return diffHelpText
	case HelpPullRequest:
		return pullRequestHelpText
	default:
		return globalHelpText
	}
}
