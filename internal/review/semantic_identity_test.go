package review

import (
	"strings"
	"testing"
)

func TestUnmergedSemanticTupleParticipatesInFileIdentity(t *testing.T) {
	mode, err := NewFileMode("100644")
	if err != nil {
		t.Fatal(err)
	}
	contentOID, err := NewObjectID(strings.Repeat("1", 40))
	if err != nil {
		t.Fatal(err)
	}
	stageOID, err := NewObjectID(strings.Repeat("2", 40))
	if err != nil {
		t.Fatal(err)
	}
	changedStageOID, err := NewObjectID(strings.Repeat("3", 40))
	if err != nil {
		t.Fatal(err)
	}
	identity := semanticIdentityForTest(t, mode, stageOID)
	changedIdentity := semanticIdentityForTest(t, mode, changedStageOID)
	contents, err := NewFileContentsWithSemanticIdentity(
		identity,
		OldFileContent(contentOID),
		NewFileContent(contentOID),
	)
	if err != nil {
		t.Fatal(err)
	}
	changedContents, err := NewFileContentsWithSemanticIdentity(
		changedIdentity,
		OldFileContent(contentOID),
		NewFileContent(contentOID),
	)
	if err != nil {
		t.Fatal(err)
	}
	if sameFileContentIdentity(
		File{contents: contents},
		File{contents: changedContents},
	) {
		t.Fatal("different unmerged stage tuples had the same file identity")
	}
}

func semanticIdentityForTest(
	t *testing.T,
	mode FileMode,
	oid ObjectID,
) FileSemanticIdentity {
	t.Helper()
	object, err := NewGitObject(ObjectBlob, oid, mode)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := NewFileSemanticStage(2, object)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := NewFileSemanticIdentity(stage)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}
