package commentseed

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/change"
)

func TestReadAcceptsEveryActiveExportTargetInArbitraryFieldOrder(t *testing.T) {
	document, err := Read(strings.NewReader(`{
		"comments":[
			{"comment":"review","binding":"active","target":{"type":"review"}},
			{"binding":"active","target":{"path":"a.go","type":"file"},"comment":"file"},
			{"target":{"line":7,"side":"old","path":"a.go","type":"line"},"comment":"line","binding":"active"},
			{"comment":"range","target":{"end":12,"type":"range","start":9,"path":"a.go","side":"new"},"binding":"active"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !document.Present() || document.CommentCount() != 4 {
		t.Fatalf("document = present %t, comments %d", document.Present(), document.CommentCount())
	}
	if document.comments[0].target.kind != change.TargetReview ||
		document.comments[1].target.kind != change.TargetFile ||
		document.comments[2].target.start.Uint32() != 7 ||
		document.comments[2].target.side != change.LineOld ||
		document.comments[3].target.start.Uint32() != 9 ||
		document.comments[3].target.end.Uint32() != 12 {
		t.Fatalf("decoded targets = %+v", document.comments)
	}
}

func TestReadDistinguishesZeroBytesFromEmptyExport(t *testing.T) {
	empty, err := Read(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if empty.Present() {
		t.Fatal("zero-byte stdin was treated as a document")
	}
	export, err := Read(strings.NewReader(`{"comments":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !export.Present() || export.CommentCount() != 0 {
		t.Fatalf("empty export = present %t, comments %d", export.Present(), export.CommentCount())
	}
}

func TestReadDecodesBytePreservingExportPath(t *testing.T) {
	document, err := Read(strings.NewReader(
		`{"comments":[{"target":{"type":"file","path":"bad\u00ff\u003c.txt"},"binding":"active","comment":"body"}]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := document.comments[0].target.path.Raw(), []byte{'b', 'a', 'd', 0xff, '<', '.', 't', 'x', 't'}; !bytes.Equal(got, want) {
		t.Fatalf("decoded path = %x, want %x", got, want)
	}
}

func TestReadRejectsMalformedOrInapplicableExportShapes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "whitespace", input: " \n", want: "expected '{'"},
		{name: "unknown root", input: `{"comments":[],"version":1}`, want: "unknown document field"},
		{name: "duplicate root", input: `{"comments":[],"comments":[]}`, want: "duplicate document field"},
		{name: "unknown comment", input: `{"comments":[{"target":{"type":"review"},"binding":"active","comment":"x","extra":0}]}`, want: "unknown field"},
		{name: "duplicate comment", input: `{"comments":[{"target":{"type":"review"},"binding":"active","comment":"x","comment":"y"}]}`, want: "duplicate field"},
		{name: "unknown target", input: `{"comments":[{"target":{"type":"review","extra":"x"},"binding":"active","comment":"x"}]}`, want: "unknown target field"},
		{name: "duplicate target", input: `{"comments":[{"target":{"type":"file","path":"a","path":"b"},"binding":"active","comment":"x"}]}`, want: "duplicate target field"},
		{name: "stale", input: `{"comments":[{"target":{"type":"review"},"binding":"stale","comment":"x"}]}`, want: "is not active"},
		{name: "orphaned", input: `{"comments":[{"target":{"type":"review"},"binding":"orphaned","comment":"x"}]}`, want: "is not active"},
		{name: "missing path", input: `{"comments":[{"target":{"type":"file"},"binding":"active","comment":"x"}]}`, want: "file target requires"},
		{name: "line extras", input: `{"comments":[{"target":{"type":"line","path":"a","side":"new","line":1,"start":1},"binding":"active","comment":"x"}]}`, want: "line target requires"},
		{name: "range order", input: `{"comments":[{"target":{"type":"range","path":"a","side":"new","start":2,"end":2},"binding":"active","comment":"x"}]}`, want: "start less than end"},
		{name: "zero line", input: `{"comments":[{"target":{"type":"line","path":"a","side":"new","line":0},"binding":"active","comment":"x"}]}`, want: "line number must be positive"},
		{name: "bad side", input: `{"comments":[{"target":{"type":"line","path":"a","side":"right","line":1},"binding":"active","comment":"x"}]}`, want: "unknown target side"},
		{name: "empty body", input: `{"comments":[{"target":{"type":"review"},"binding":"active","comment":" \n"}]}`, want: "body must be non-whitespace"},
		{name: "trailing", input: `{"comments":[]} {"comments":[]}`, want: "trailing data"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Read(strings.NewReader(test.input))
			if err == nil || !strings.Contains(err.Error(), test.want) || !errors.Is(err, ErrInvalidDocument) {
				t.Fatalf("Read() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestReadRejectsInvalidUTF8AndDocumentLimit(t *testing.T) {
	if _, err := Read(bytes.NewReader([]byte{'{', 0xff, '}'})); err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	oversized := bytes.NewReader(bytes.Repeat([]byte{' '}, int(change.MaxPreparedExportBytes)+1))
	if _, err := Read(oversized); err == nil || !strings.Contains(err.Error(), "input exceeds") {
		t.Fatalf("oversized error = %v", err)
	}
}
