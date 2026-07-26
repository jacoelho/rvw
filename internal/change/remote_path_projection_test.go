package change

import (
	"testing"
	"unsafe"
)

func TestRemotePathProjectionSortsCollapsesAndMarksAmbiguousAliases(t *testing.T) {
	projection := newRemotePathProjection([]remotePathAlias{
		{path: "same.go", file: 2, status: RemoteMappingMapped},
		{path: "only.go", file: 1, status: RemoteMappingMapped},
		{path: "same.go", file: 1, status: RemoteMappingMapped},
		{path: "only.go", file: 1, status: RemoteMappingMapped},
	})
	if len(projection.aliases) != 2 || projection.aliases[0].path != "only.go" ||
		projection.aliases[1].path != "same.go" {
		t.Fatalf("projection aliases = %#v", projection.aliases)
	}
	if mapping, ok := projection.lookup("only.go"); !ok || mapping.status != RemoteMappingMapped || mapping.file != 1 {
		t.Fatalf("unique mapping = %#v, exists %t", mapping, ok)
	}
	if mapping, ok := projection.lookup("same.go"); !ok || mapping.status != RemoteMappingAmbiguous || mapping.file != 0 {
		t.Fatalf("ambiguous mapping = %#v, exists %t", mapping, ok)
	}
	if _, ok := projection.lookup("missing.go"); ok {
		t.Fatal("missing alias unexpectedly mapped")
	}
	wantRetained := int64(cap(projection.aliases))*remotePathAliasStorageBytes +
		int64(len("only.go")+len("same.go"))
	if projection.retainedBytes() != wantRetained {
		t.Fatalf("projection retained = %d, want %d", projection.retainedBytes(), wantRetained)
	}
	if remotePathAliasStorageBytes < int64(unsafe.Sizeof(remotePathAlias{})) {
		t.Fatalf(
			"alias storage charge = %d, layout = %d",
			remotePathAliasStorageBytes,
			unsafe.Sizeof(remotePathAlias{}),
		)
	}
}

func TestSnapshotRemotePathProjectionIsCachedAndCapacityBounded(t *testing.T) {
	snapshot := testSnapshot(t, 1, "one.go")
	if len(snapshot.pathProjection.aliases) != 1 ||
		cap(snapshot.pathProjection.aliases) > 2*snapshot.FileCount() {
		t.Fatalf(
			"projection = len %d cap %d files %d",
			len(snapshot.pathProjection.aliases),
			cap(snapshot.pathProjection.aliases),
			snapshot.FileCount(),
		)
	}
	var projection remotePathProjection
	if allocations := testing.AllocsPerRun(100, func() {
		projection = snapshot.remotePathMappings()
	}); allocations != 0 {
		t.Fatalf("remotePathMappings allocations = %f, want 0", allocations)
	}
	if unsafe.SliceData(projection.aliases) != unsafe.SliceData(snapshot.pathProjection.aliases) {
		t.Fatal("remote path mapping did not borrow the cached projection")
	}
}
