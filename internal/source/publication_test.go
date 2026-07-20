package source

import (
	"slices"
	"testing"
)

func TestDecodeReviewBodiesRequiresSlurpedValidatedPages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{
			name:  "valid pages",
			value: `[[{"id":1,"body":"first"}],[],[{"id":2,"body":""}]]`,
			want:  []string{"first", ""},
		},
		{name: "null", value: `null`},
		{name: "no pages", value: `[]`},
		{name: "null page", value: `[null]`},
		{name: "flat response", value: `[{"id":1,"body":"first"}]`},
		{name: "missing identity", value: `[[{"body":"first"}]]`},
		{name: "zero identity", value: `[[{"id":0,"body":"first"}]]`},
		{name: "repeated identity", value: `[[{"id":1,"body":"first"}],[{"id":1,"body":"second"}]]`},
		{name: "missing body", value: `[[{"id":1}]]`},
		{name: "null body", value: `[[{"id":1,"body":null}]]`},
		{name: "duplicate identity", value: `[[{"id":1,"id":2,"body":"first"}]]`},
		{name: "duplicate body", value: `[[{"id":1,"body":"first","body":"second"}]]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeReviewBodies([]byte(test.value))
			if test.want == nil {
				if err == nil {
					t.Fatalf("decodeReviewBodies(%s) unexpectedly succeeded with %q", test.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeReviewBodies(%s): %v", test.value, err)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("decodeReviewBodies(%s) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}
