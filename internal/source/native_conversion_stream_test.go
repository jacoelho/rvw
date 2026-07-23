package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamNativeConversionPreservesStateAcrossReadBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		conversion nativeConversion
		want       string
	}{
		{
			name:       "crlf split at 64 KiB",
			input:      strings.Repeat("x", 65_535) + "\r\nlast\r\n",
			conversion: nativeConversion{normalizeEOL: true},
			want:       strings.Repeat("x", 65_535) + "\nlast\n",
		},
		{
			name:       "ident prefix split at 64 KiB",
			input:      strings.Repeat("x", 65_534) + "$Id: expanded$\n",
			conversion: nativeConversion{ident: true},
			want:       strings.Repeat("x", 65_534) + "$Id$\n",
		},
		{
			name:       "unterminated long ident candidate",
			input:      "$Id:" + strings.Repeat("content", 20_000),
			conversion: nativeConversion{ident: true},
			want:       "$Id:" + strings.Repeat("content", 20_000),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			inputPath := filepath.Join(directory, "input")
			if err := os.WriteFile(inputPath, []byte(test.input), 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := os.Create(filepath.Join(directory, "output"))
			if err != nil {
				t.Fatal(err)
			}
			written, conversionErr := streamNativeConversion(
				t.Context(), inputPath, output, test.conversion,
			)
			closeErr := output.Close()
			if conversionErr != nil {
				t.Fatal(conversionErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			got, err := os.ReadFile(filepath.Join(directory, "output"))
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(got)) != written || string(got) != test.want {
				t.Fatalf("conversion wrote %d bytes (%q), want %d bytes (%q)",
					written, got, len(test.want), test.want)
			}
		})
	}
}
