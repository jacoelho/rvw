package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	interactiveTerminalDiagnostic = "interactive mode requires terminal stdin and stdout; use --stdout or --json for noninteractive output"
	dumbTerminalDiagnostic        = "interactive mode does not support TERM=dumb; use --stdout or --json for noninteractive output"
)

type terminalCapability uint8

const (
	terminalInput terminalCapability = iota + 1
	terminalOutput
)

type terminalCapabilities struct {
	bits uint8
}

func newTerminalCapabilities(
	members ...terminalCapability,
) (terminalCapabilities, error) {
	capabilities := terminalCapabilities{}
	for _, member := range members {
		bit, ok := terminalCapabilityBit(member)
		if !ok {
			return terminalCapabilities{}, errors.New("unknown terminal capability")
		}
		capabilities.bits |= bit
	}
	return capabilities, nil
}

func detectTerminalCapabilities(input, output *os.File) terminalCapabilities {
	members := make([]terminalCapability, 0, 2)
	if isTerminal(input) {
		members = append(members, terminalInput)
	}
	if isTerminal(output) {
		members = append(members, terminalOutput)
	}
	capabilities, err := newTerminalCapabilities(members...)
	if err != nil {
		return terminalCapabilities{}
	}
	return capabilities
}

func (capabilities terminalCapabilities) has(member terminalCapability) bool {
	bit, ok := terminalCapabilityBit(member)
	return ok && capabilities.bits&bit != 0
}

func terminalCapabilityBit(member terminalCapability) (uint8, bool) {
	switch member {
	case terminalInput:
		return 1 << 0, true
	case terminalOutput:
		return 1 << 1, true
	default:
		return 0, false
	}
}

func validateTerminalAdmission(
	invocation Invocation,
	capabilities terminalCapabilities,
	environment []string,
	seeded bool,
) error {
	if invocation.Output != OutputInteractive {
		return nil
	}
	if term, _ := environmentValue(environment, "TERM"); term == "dumb" {
		return errors.New(dumbTerminalDiagnostic)
	}
	if !capabilities.has(terminalOutput) || !seeded && !capabilities.has(terminalInput) {
		return errors.New(interactiveTerminalDiagnostic)
	}
	return nil
}

func openControllingTerminal() (io.ReadCloser, error) {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	if !isTerminal(terminal) {
		closeErr := terminal.Close()
		return nil, errors.Join(fmt.Errorf("/dev/tty is not a terminal"), closeErr)
	}
	return terminal, nil
}
