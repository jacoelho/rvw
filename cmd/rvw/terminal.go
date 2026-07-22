package main

import (
	"errors"
	"os"
)

const interactiveTerminalDiagnostic = "interactive mode requires terminal stdin and stdout; use --stdout or --json for noninteractive output"

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
) error {
	if invocation.Output != OutputInteractive {
		return nil
	}
	if !capabilities.has(terminalInput) || !capabilities.has(terminalOutput) {
		return errors.New(interactiveTerminalDiagnostic)
	}
	return nil
}
