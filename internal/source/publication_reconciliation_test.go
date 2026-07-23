package source

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

func TestPublicationReconciliationFinishRejectsInvalidUTF8AndClosesSpool(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "reconciliation-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(
		[]byte("[[{\"id\":1,\"body\":\""),
		append([]byte{0xff}, []byte("\"}]]")...)...,
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	marker, _ := review.NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	acquisition := &PublicationReconciliationAcquisition{
		spool: &commandSpool{file: file},
		request: review.ReconciliationRequest{
			Operation: 1,
			Workspace: 1,
			Source:    1,
			Marker:    marker,
		},
	}
	result := acquisition.Finish(t.Context(), directCPUAdmission)
	if result.Disposition != review.ReconciliationStillUnknown || result.Detail.IsEmpty() {
		t.Fatalf("invalid UTF-8 reconciliation result = %+v", result)
	}
	if acquisition.spool != nil {
		t.Fatal("finished reconciliation retained its spool")
	}
	if _, err := file.Stat(); err == nil {
		t.Fatal("finished reconciliation left its spool open")
	}
}

func TestPublicationReconciliationFinishKeepsSpoolIOOutsideAdmission(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "reconciliation-*.json")
	if err != nil {
		t.Fatal(err)
	}
	encoded := []byte(`[[{"id":1,"body":"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->"}]]`)
	if _, err := file.Write(encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	marker, _ := review.NewPublicationMarker(
		"<!-- rvw-publication:v1:0123456789abcdef0123456789abcdef -->",
	)
	acquisition := &PublicationReconciliationAcquisition{
		spool: &commandSpool{file: file},
		request: review.ReconciliationRequest{
			Operation: 1,
			Workspace: 1,
			Source:    1,
			Marker:    marker,
		},
	}

	active := false
	runCPU := func(_ context.Context, work func() error) error {
		if active {
			return errors.New("nested reconciliation CPU admission")
		}
		before, err := file.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		active = true
		workErr := work()
		active = false
		after, seekErr := file.Seek(0, io.SeekCurrent)
		if seekErr != nil {
			return errors.Join(workErr, seekErr)
		}
		if after != before {
			return errors.Join(workErr, errors.New("reconciliation spool I/O ran under CPU admission"))
		}
		return workErr
	}
	result := acquisition.Finish(t.Context(), runCPU)
	if result.Disposition != review.ReconciliationCommitted {
		t.Fatalf("reconciliation result = %+v", result)
	}
	if acquisition.spool != nil {
		t.Fatal("finished reconciliation retained its spool")
	}
	if _, err := file.Stat(); err == nil {
		t.Fatal("finished reconciliation left its spool open")
	}
}
