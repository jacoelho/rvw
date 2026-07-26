package change

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestPathPreservesRawBytesAndEscapesDisplay(t *testing.T) {
	raw := []byte{'a', '\\', '\n', 0xff, 0xe2, 0x80, 0xae}
	path, err := NewPath(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := path.UTF8(); ok {
		t.Fatal("path containing invalid UTF-8 reported as valid")
	}
	if got, want := path.Display(), `a\\\x0A\xFF\u{202E}`; got != want {
		t.Fatalf("Display() = %q, want %q", got, want)
	}
	if got, want := path.Base64(), base64.StdEncoding.EncodeToString(raw); got != want {
		t.Fatalf("Base64() = %q, want %q", got, want)
	}
	returned := path.Raw()
	returned[0] = 'z'
	if path.Raw()[0] != 'a' {
		t.Fatal("Raw exposed mutable path storage")
	}
}

func TestPathLimits(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "empty"},
		{name: "NUL", raw: []byte("a\x00b")},
		{name: "too long", raw: []byte(strings.Repeat("a", int(MaxPathBytes)+1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewPath(test.raw); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("NewPath() error = %v, want ErrInvalidPath", err)
			}
		})
	}
}

func TestSnapshotRequiresSequentialCrossConsistentIdentities(t *testing.T) {
	sourceID, _ := NewSourceID(7)
	path, _ := NewPath([]byte("file.go"))
	change, _ := NewUnclassifiedChange(ChangeAdded, 1, 0)
	flags, _ := NewFlags(FileTraits{}, HiddenReasons{})
	fileID, _ := NewFileID(0)
	patchID, _ := NewPatchID(1)
	handle, _ := NewPatchHandle(patchID, sourceID, fileID)
	file, err := NewFile(fileID, nil, &path, change, flags, handle)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewSnapshot(sourceID, []File{file})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := snapshot.File(0)
	if snapshot.FileCount() != 1 || first.DisplayPath() != "file.go" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}

	otherSource, _ := NewSourceID(8)
	if _, err := NewSnapshot(otherSource, []File{file}); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("NewSnapshot() error = %v, want ErrInvalidSnapshot", err)
	}
}

func TestFilePathInvariants(t *testing.T) {
	sourceID, _ := NewSourceID(1)
	fileID, _ := NewFileID(0)
	patchID, _ := NewPatchID(1)
	handle, _ := NewPatchHandle(patchID, sourceID, fileID)
	path, _ := NewPath([]byte("file.go"))
	flags, _ := NewFlags(FileTraits{}, HiddenReasons{})

	tests := []struct {
		name    string
		kind    ChangeKind
		oldPath *Path
		newPath *Path
	}{
		{name: "added with old path", kind: ChangeAdded, oldPath: &path, newPath: &path},
		{name: "deleted with new path", kind: ChangeDeleted, oldPath: &path, newPath: &path},
		{name: "modified without old", kind: ChangeModified, newPath: &path},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			change, _ := NewUnclassifiedChange(test.kind, 0, 0)
			if _, err := NewFile(fileID, test.oldPath, test.newPath, change, flags, handle); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("NewFile() error = %v, want ErrInvalidSnapshot", err)
			}
		})
	}
}

func TestFileContentAndSubmoduleSidesMatchPaths(t *testing.T) {
	sourceID, _ := NewSourceID(1)
	fileID, _ := NewFileID(0)
	patchID, _ := NewPatchID(1)
	handle, _ := NewPatchHandle(patchID, sourceID, fileID)
	path, _ := NewPath([]byte("module"))
	flags, _ := NewFlags(FileTraits{}, HiddenReasons{})
	oldObject, _ := NewObjectID(strings.Repeat("1", 40))
	newObject, _ := NewObjectID(strings.Repeat("2", 40))

	added, _ := NewSubmoduleChange(nil, &newObject)
	if _, err := NewFile(fileID, &path, &path, added, flags, handle); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("cross-wired added submodule error = %v", err)
	}
	if _, err := NewFile(fileID, nil, &path, added, flags, handle); err != nil {
		t.Fatalf("valid added submodule: %v", err)
	}
	deleted, _ := NewSubmoduleChange(&oldObject, nil)
	if _, err := NewFile(fileID, &path, nil, deleted, flags, handle); err != nil {
		t.Fatalf("valid deleted submodule: %v", err)
	}

	change, _ := NewUnclassifiedChange(ChangeAdded, 1, 0)
	contents, _ := NewFileContents(OldFileContent(oldObject), NewFileContent(newObject))
	if _, err := newFileWithContents(
		fileID, nil, &path, change, flags, handle, contents,
	); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("added file with old content error = %v", err)
	}
}

func TestChangeVariantsValidateRanges(t *testing.T) {
	if _, err := NewUnclassifiedChange(ChangeBinary, 0, 0); !errors.Is(err, ErrInvalidChange) {
		t.Fatalf("path change error = %v, want ErrInvalidChange", err)
	}
	if _, err := NewUnclassifiedChange(ChangeModified, MaxAdditions+1, 0); !errors.Is(err, ErrInvalidChange) {
		t.Fatalf("count error = %v, want ErrInvalidChange", err)
	}
	mode, err := NewMode("100644", "100755")
	if err != nil {
		t.Fatal(err)
	}
	change, err := NewSimpleChange(ChangeModeOnly, &mode)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := change.Mode(); !ok || got.Old().String() != "100644" || got.New().String() != "100755" {
		t.Fatalf("unexpected mode: %+v, %v", got, ok)
	}
	if _, err := NewFileMode("777777"); !errors.Is(err, ErrInvalidChange) {
		t.Fatalf("unsupported mode error = %v", err)
	}
	sameMode, _ := NewMode("100644", "100644")
	if _, err := NewSimpleChange(ChangeModeOnly, &sameMode); !errors.Is(err, ErrInvalidChange) {
		t.Fatalf("unchanged mode error = %v", err)
	}
	if _, err := NewSimpleChange(ChangeModeOnly, nil); !errors.Is(err, ErrInvalidChange) {
		t.Fatalf("missing mode error = %v", err)
	}
	if _, err := NewSubmoduleChange(nil, nil); !errors.Is(err, ErrInvalidChange) {
		t.Fatalf("missing submodule commits error = %v", err)
	}
	objectID, _ := NewObjectID(strings.Repeat("1", 40))
	gitlinkMode, _ := NewFileMode("160000")
	if _, err := NewGitObject(ObjectBlob, objectID, gitlinkMode); !errors.Is(err, ErrInvalidChange) {
		t.Fatalf("blob/gitlink mode error = %v", err)
	}
}

func TestHiddenReasonsAreDistinct(t *testing.T) {
	traits, err := NewFileTraits(TraitGenerated)
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := NewHiddenReasons(HiddenExclude, HiddenInclude, HiddenExclude)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := NewFlags(traits, hidden)
	if err != nil {
		t.Fatal(err)
	}
	reasons := flags.HiddenReasons()
	if len(reasons) != 2 || !flags.Hidden() {
		t.Fatalf("unexpected hidden reasons: %v", reasons)
	}
	if !flags.Traits().Has(TraitGenerated) || flags.Traits().Has(TraitVendored) {
		t.Fatalf("unexpected file traits: %+v", flags.Traits())
	}
	returned := flags.HiddenReasons()
	returned[0] = HiddenGitignore
	if flags.HiddenReasons()[0] == HiddenGitignore {
		t.Fatal("Clone exposed hidden-reason storage")
	}
}

func TestFileTraitsRejectUnknownBits(t *testing.T) {
	if _, err := NewFileTraits(FileTrait(99)); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("unknown trait error = %v", err)
	}
}

func TestObjectIDRejectsZeroAndSnapshotBuilderRejectsMixedFormats(t *testing.T) {
	if _, err := NewObjectID(strings.Repeat("0", 40)); !errors.Is(err, ErrInvalidChange) {
		t.Fatalf("all-zero object ID error = %v, want ErrInvalidChange", err)
	}

	sourceID, _ := NewSourceID(1)
	builder, err := NewSnapshotBuilder(sourceID, 2)
	if err != nil {
		t.Fatal(err)
	}
	flags, _ := NewFlags(FileTraits{}, HiddenReasons{})
	change, _ := NewUnclassifiedChange(ChangeAdded, 1, 0)
	for index, width := range []int{40, 64} {
		path, _ := NewPath(fmt.Appendf(nil, "file-%d.go", index))
		objectID, _ := NewObjectID(strings.Repeat(strconv.Itoa(index+1), width))
		contents, err := NewFileContents(NewFileContent(objectID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := builder.AddFileWithContents(
			nil,
			&path,
			change,
			flags,
			contents,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := builder.Finish(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("mixed object ID formats error = %v, want ErrInvalidSnapshot", err)
	}
	if builder.files != nil {
		t.Fatal("failed Finish retained consumed file backing")
	}
	if _, err := builder.Finish(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("repeated Finish error = %v, want ErrInvalidSnapshot", err)
	}
}

func TestSnapshotBuilderFinishConsumesTransferredBacking(t *testing.T) {
	sourceID, _ := NewSourceID(1)
	builder, err := NewSnapshotBuilder(sourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := NewPath([]byte("file.go"))
	change, _ := NewUnclassifiedChange(ChangeAdded, 1, 0)
	flags, _ := NewFlags(FileTraits{}, HiddenReasons{})
	if _, err := builder.AddFile(nil, &path, change, flags); err != nil {
		t.Fatal(err)
	}
	backing := &builder.files[0]
	snapshot, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if builder.files != nil || &snapshot.files[0] != backing {
		t.Fatal("Finish did not consume and transfer file backing")
	}
	if _, err := builder.AddFile(nil, &path, change, flags); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("post-Finish mutation error = %v, want ErrInvalidSnapshot", err)
	}
}
