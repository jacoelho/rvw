package tui

import "testing"

func TestParseResolveDraftCommandRequiresExplicitChoice(t *testing.T) {
	for _, choice := range []string{"local", "remote", "base"} {
		intent, err := parseCommand(":resolve-draft " + choice)
		if err != nil {
			t.Fatal(err)
		}
		if intent.kind != commandResolveDraft || intent.text != choice {
			t.Fatalf("choice %q parsed as %+v", choice, intent)
		}
	}
	for _, command := range []string{
		":resolve-draft",
		":resolve-draft ours",
		":resolve-draft local extra",
	} {
		if _, err := parseCommand(command); err == nil {
			t.Fatalf("command %q unexpectedly parsed", command)
		}
	}
}
