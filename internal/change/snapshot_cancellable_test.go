package change

import (
	"errors"
	"fmt"
	"testing"
)

func TestRemotePathProjectionSortStopsOnCancellation(t *testing.T) {
	aliases := make([]remotePathAlias, 10_000)
	for index := range aliases {
		aliases[index] = remotePathAlias{
			path: fmt.Sprintf("%08d", len(aliases)-index),
			file: FileID(index),
		}
	}
	cause := errors.New("stop path projection sort")
	checks := 0
	_, err := newRemotePathProjectionCancellable(func() error {
		checks++
		if checks == 4 {
			return cause
		}
		return nil
	}, aliases)
	if !errors.Is(err, cause) {
		t.Fatalf("cancellation error = %v", err)
	}
	if checks != 4 {
		t.Fatalf("cancellation checks = %d, want 4", checks)
	}
}
