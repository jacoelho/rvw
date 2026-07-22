package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalAdmissionRequiresInputAndOutputCapabilities(t *testing.T) {
	interactive, err := parseArgs([]string{"diff"})
	if err != nil {
		t.Fatal(err)
	}
	noninteractive, err := parseArgs([]string{"diff", "--stdout"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		members []terminalCapability
		wantErr bool
	}{
		{name: "neither", wantErr: true},
		{name: "input only", members: []terminalCapability{terminalInput}, wantErr: true},
		{name: "output only", members: []terminalCapability{terminalOutput}, wantErr: true},
		{
			name:    "input and output",
			members: []terminalCapability{terminalInput, terminalOutput},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			capabilities := testTerminalCapabilities(t, test.members...)
			err := validateTerminalAdmission(interactive, capabilities)
			if (err != nil) != test.wantErr {
				t.Fatalf("interactive admission error = %v", err)
			}
			if err := validateTerminalAdmission(noninteractive, capabilities); err != nil {
				t.Fatalf("noninteractive admission error = %v", err)
			}
		})
	}
}

func TestTerminalCapabilitiesRejectUnknownMember(t *testing.T) {
	if _, err := newTerminalCapabilities(terminalCapability(255)); err == nil {
		t.Fatal("unknown terminal capability was accepted")
	}
}

func TestRunRejectsInteractiveBeforeRepositoryDiscovery(t *testing.T) {
	var stdout, stderr bytes.Buffer
	status := run(runRequest{
		ctx:    t.Context(),
		args:   []string{"diff"},
		cwd:    t.TempDir(),
		stdin:  strings.NewReader("q"),
		stdout: &stdout,
		stderr: &stderr,
	})
	want := "rvw: " + interactiveTerminalDiagnostic + "\n"
	if status != 1 || stdout.Len() != 0 || stderr.String() != want {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

func TestRunRejectsNonterminalPullRequestWithoutCallingGitHub(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(bin, "gh-called")
	gh := filepath.Join(bin, "gh")
	writeRunFile(t, gh, "#!/bin/sh\nprintf called > "+marker+"\n")
	if err := os.Chmod(gh, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	status := run(runRequest{
		ctx:    t.Context(),
		args:   []string{"pr", "1", "--repo", "owner/repo"},
		cwd:    t.TempDir(),
		stdin:  strings.NewReader("q"),
		stdout: &stdout,
		stderr: &stderr,
	})
	if status != 1 || stdout.Len() != 0 ||
		!strings.Contains(stderr.String(), interactiveTerminalDiagnostic) {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("gh was invoked before terminal admission: %v", err)
	}
}

func TestTermiosDetectorRejectsPipesAndNullDevice(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if isTerminal(reader) || isTerminal(writer) {
		t.Fatal("pipe was detected as a terminal")
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if isTerminal(null) || isTerminal(nil) {
		t.Fatal("null or nil file was detected as a terminal")
	}
}

func testTerminalCapabilities(
	t *testing.T,
	members ...terminalCapability,
) terminalCapabilities {
	t.Helper()
	capabilities, err := newTerminalCapabilities(members...)
	if err != nil {
		t.Fatal(err)
	}
	return capabilities
}
