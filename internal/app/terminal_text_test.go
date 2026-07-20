package app

import "testing"

func TestTerminalTextRendering(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "printable Unicode and graphemes",
			input: "café 界 e\u0301 👩‍💻",
			want:  "café 界 e\u0301 👩‍💻",
		},
		{
			name:  "tab after narrow characters",
			input: "12\tvalue",
			want:  "12  value",
		},
		{
			name:  "tab after wide character",
			input: "界\tvalue",
			want:  "界  value",
		},
		{
			name:  "tab after combining sequence",
			input: "e\u0301\tvalue",
			want:  "e\u0301   value",
		},
		{
			name:  "ASCII controls",
			input: "\x00\n\r\x1b\x7f",
			want:  `\x00\x0A\x0D\x1B\x7F`,
		},
		{
			name:  "C1 and bidirectional controls",
			input: "\u0085\u202e",
			want:  `\u{0085}\u{202E}`,
		},
		{
			name:  "invalid UTF-8",
			input: string([]byte{'a', 0xff, 'b'}),
			want:  `a\xFFb`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := safeText(test.input); got != test.want {
				t.Fatalf("safeText(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestTerminalTextRendererCarriesTabColumnAcrossSegments(t *testing.T) {
	renderer := terminalTextRenderer{}
	got := renderer.render("界") + renderer.render("\tvalue")
	if got != "界  value" {
		t.Fatalf("segmented render = %q, want %q", got, "界  value")
	}
}

func TestTerminalTextRendererMeasuresWholeGraphemes(t *testing.T) {
	if got := safeText("1️⃣\tvalue"); got != "1️⃣  value" {
		t.Fatalf("keycap render = %q, want %q", got, "1️⃣  value")
	}
}
