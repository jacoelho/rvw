package output

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

func TestHumanCountsHiddenFilesButListsOnlyVisible(t *testing.T) {
	document := localDocument(t, map[string]string{
		"a.go":   "package a\n",
		"go.sum": "hidden\n",
	})
	defer document.Close()
	var output bytes.Buffer
	if err := WriteHuman(&output, document, Options{}); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "2 changed files\na.go\n"; got != want {
		t.Fatalf("human output:\n%s\nwant:\n%s", got, want)
	}
}

func TestJSONIncludesHiddenFilesAndCrossConsistentHandles(t *testing.T) {
	document := localDocument(t, map[string]string{
		"a.go":   "package a\n",
		"go.sum": "hidden\n",
	})
	defer document.Close()
	var output bytes.Buffer
	if err := WriteJSON(&output, document, Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(output.String(), "}\n") {
		t.Fatalf("JSON lacks one trailing newline: %q", output.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.UseNumber()
	var value struct {
		Source    string      `json:"source"`
		SourceID  json.Number `json:"source_id"`
		FileCount json.Number `json:"file_count"`
		Files     []struct {
			ID      json.Number `json:"id"`
			NewPath *string     `json:"new_path"`
			Flags   struct {
				HiddenReasons []string `json:"hidden_reasons"`
			} `json:"flags"`
			PatchHandle struct {
				ID       json.Number `json:"id"`
				SourceID json.Number `json:"source_id"`
				FileID   json.Number `json:"file_id"`
			} `json:"patch_handle"`
		} `json:"files"`
	}
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	if value.Source != "diff" || value.SourceID.String() != "1" ||
		value.FileCount.String() != "2" || len(value.Files) != 2 {
		t.Fatalf("unexpected document: %+v", value)
	}
	for _, file := range value.Files {
		if file.ID.String() != file.PatchHandle.FileID.String() ||
			file.PatchHandle.SourceID.String() != value.SourceID.String() {
			t.Fatalf("cross-inconsistent handle: %+v", file)
		}
	}
}

func TestTerminalJSONEscapesBidirectionalControls(t *testing.T) {
	document := localDocument(t, map[string]string{"safe\u202e\u007f\u009b.go": "unsafe name\n"})
	defer document.Close()
	var output bytes.Buffer
	if err := WriteJSON(&output, document, Options{Terminal: true}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte("\u202e")) {
		t.Fatalf("terminal JSON contains raw bidi control: %q", output.String())
	}
	if !bytes.Contains(output.Bytes(), []byte(`\u202e`)) {
		t.Fatalf("terminal JSON lacks escaped bidi control: %q", output.String())
	}
	for _, escaped := range []string{`\u007f`, `\u009b`} {
		if !bytes.Contains(output.Bytes(), []byte(escaped)) {
			t.Fatalf("terminal JSON lacks escaped control %s: %q", escaped, output.String())
		}
	}
}

func TestOutputsPreserveNonUTF8PathIdentityWithoutRawTerminalBytes(t *testing.T) {
	document := nonUTF8StagedDocument(
		t,
		[]byte{'o', 'd', 'd', '-', 0xff, '.', 'g', 'o'},
	)
	defer document.Close()

	var human bytes.Buffer
	if err := WriteHuman(&human, document, Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), `odd-\xFF.go`) ||
		bytes.Contains(human.Bytes(), []byte{0xff}) {
		t.Fatalf("human output did not safely escape path: %q", human.String())
	}

	var encoded bytes.Buffer
	if err := WriteJSON(&encoded, document, Options{}); err != nil {
		t.Fatal(err)
	}
	var value struct {
		Files []struct {
			NewPath            *string `json:"new_path"`
			NewPathDisplay     *string `json:"new_path_display"`
			NewPathBytesBase64 *string `json:"new_path_bytes_base64"`
		} `json:"files"`
	}
	if err := json.Unmarshal(encoded.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Files) != 1 || value.Files[0].NewPath != nil ||
		value.Files[0].NewPathDisplay == nil ||
		*value.Files[0].NewPathDisplay != `odd-\xFF.go` ||
		value.Files[0].NewPathBytesBase64 == nil ||
		*value.Files[0].NewPathBytesBase64 != "b2RkLf8uZ28=" {
		t.Fatalf("non-UTF-8 JSON path = %+v", value.Files)
	}
}

func nonUTF8StagedDocument(t *testing.T, path []byte) *source.Document {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init")
	hash := exec.CommandContext(t.Context(), "git", "hash-object", "-w", "--stdin")
	hash.Dir = root
	hash.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	hash.Stdin = strings.NewReader("unsafe name\n")
	output, err := hash.Output()
	if err != nil {
		t.Fatal(err)
	}
	objectID := strings.TrimSpace(string(output))
	index := exec.CommandContext(t.Context(), "git", "update-index", "-z", "--index-info")
	index.Dir = root
	index.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	index.Stdin = bytes.NewReader(append(
		append([]byte("100644 "+objectID+"\t"), path...),
		0,
	))
	if output, err := index.CombinedOutput(); err != nil {
		t.Fatalf("write raw index path: %v\n%s", err, output)
	}
	workingDirectory, err := source.NewWorkingDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := source.NewService(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	document, err := service.Open(
		t.Context(),
		source.StagedTarget(),
		source.OpenOptions{BuiltinDefaults: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestWriterFailurePropagates(t *testing.T) {
	document := localDocument(t, map[string]string{"a.go": "package a\n"})
	defer document.Close()
	writer := failingWriter{}
	if err := WriteHuman(writer, document, Options{}); !errors.Is(err, errWriterFailed) {
		t.Fatalf("WriteHuman() error = %v", err)
	}
	if err := WriteJSON(writer, document, Options{}); !errors.Is(err, errWriterFailed) {
		t.Fatalf("WriteJSON() error = %v", err)
	}
}

func localDocument(t *testing.T, files map[string]string) *source.Document {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init")
	for name, contents := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workingDirectory, err := source.NewWorkingDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := source.NewService(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	document, err := service.Open(t.Context(), source.WorktreeTarget(), source.OpenOptions{
		IncludeUntracked: true,
		BuiltinDefaults:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

var errWriterFailed = errors.New("writer failed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errWriterFailed
}
