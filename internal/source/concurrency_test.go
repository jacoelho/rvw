package source_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/source"
)

// numberedFile returns n lines "<prefix> <i>\n".
func numberedFile(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s %d\n", prefix, i)
	}
	return b.String()
}

// TestLinesRecoversAfterCancellation checks that a Lines call whose context ends, before
// or during the read, fails with the context's error or completes correctly, and the next call
// with a live context returns the right lines.
func TestLinesRecoversAfterCancellation(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	const big = 300_000
	r.write("big.txt", numberedFile("big", big))
	r.write("small.txt", numberedFile("small", 10))
	r.commit("files")
	snap := resolve(t, r.dir, source.Last())
	bigIdx, smallIdx := fileIndex(t, snap, "big.txt"), fileIndex(t, snap, "small.txt")

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := linesOf(cancelled, snap, bigIdx, true, 1, 10); !errors.Is(err, context.Canceled) {
		t.Errorf("Lines with a cancelled context: err = %v, want context.Canceled", err)
	}

	for round := range 5 {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		var lines []string
		var err error
		go func() {
			defer close(done)
			lines, err = linesOf(ctx, snap, bigIdx, true, 1, big)
		}()
		cancel()
		<-done
		switch {
		case err != nil:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("round %d: cancelled Lines err = %v, want context.Canceled", round, err)
			}
		case len(lines) != big || lines[big-1] != "big "+strconv.Itoa(big):
			t.Errorf("round %d: completed Lines returned %d lines", round, len(lines))
		}

		got, err := linesOf(t.Context(), snap, smallIdx, true, 4, 6)
		if err != nil {
			t.Fatalf("round %d: Lines after cancellation: %v", round, err)
		}
		if want := []string{"small 4", "small 5", "small 6"}; !reflect.DeepEqual(got, want) {
			t.Errorf("round %d: Lines after cancellation = %q, want %q", round, got, want)
		}
	}
}

// TestCloseStopsGitProcesses checks that after Close every git process the repository started
// has ended and been reaped. Not parallel: the PID log changes PATH.
func TestCloseStopsGitProcesses(t *testing.T) {
	r := newRepo(t)
	r.write("f.txt", numberedFile("f", 5))
	r.commit("f")
	pidLog := logGitPIDs(t, "*")
	repo, err := source.Open(t.Context(), r.dir)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Resolve(t.Context(), source.Last(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := linesOf(t.Context(), snap, 0, true, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	processesGone(t, readPIDs(t, pidLog))
}

// TestSnapshotConcurrentReads checks that concurrent Lines, Content, Diff, NumStat and Search on
// one snapshot each return what a lone call returns.
func TestSnapshotConcurrentReads(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	const files, lines = 6, 200
	for f := range files {
		r.write(fmt.Sprintf("f%d.txt", f), numberedFile(fmt.Sprintf("old %d", f), lines))
	}
	r.commit("base")
	for f := range files {
		r.write(fmt.Sprintf("f%d.txt", f), strings.Replace(numberedFile(fmt.Sprintf("old %d", f), lines), "old", "new", 3))
	}
	r.commit("edit")
	snap := resolve(t, r.dir, source.Last())

	wantDiff := make([]source.FileDiff, files)
	blobs := make([][2][]byte, files) // old, new
	for i := range files {
		f := snap.File(i)
		blobs[i] = [2][]byte{r.blob("HEAD^", f.OldPath), r.blob("HEAD", f.NewPath)}
		d, err := snap.Diff(t.Context(), i)
		if err != nil {
			t.Fatal(err)
		}
		wantDiff[i] = d
	}
	wantCounts := map[int]source.LineCount{}
	if err := snap.NumStat(t.Context(), func(i int, c source.LineCount) { wantCounts[i] = c }); err != nil {
		t.Fatal(err)
	}
	wantSearch := search(t, snap, "new 1")
	needle := newSearchText(t, "new 1")

	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			for k := range 20 {
				i := (g + k) % files
				newSide := k%2 == 0
				blob := blobs[i][0]
				if newSide {
					blob = blobs[i][1]
				}
				all := blobLines(blob)
				from := 1 + (g*7+k*13)%(lines-10)
				got, err := linesOf(t.Context(), snap, i, newSide, from, from+9)
				if err != nil {
					t.Errorf("Lines: %v", err)
					return
				}
				if want := all[from-1 : from+9]; !reflect.DeepEqual(got, want) {
					t.Errorf("Lines(%d, new=%t, %d) = %q, want %q", i, newSide, from, got, want)
				}
				switch k % 4 {
				case 0:
					d, err := snap.Diff(t.Context(), i)
					if err != nil || !reflect.DeepEqual(d, wantDiff[i]) {
						t.Errorf("concurrent Diff(%d) differs (err %v)", i, err)
					}
				case 1:
					f, err := snap.Facts(t.Context(), i)
					if err != nil || !reflect.DeepEqual(f, wantDiff[i].Facts) {
						t.Errorf("concurrent Facts(%d) differs (err %v)", i, err)
					}
				case 2:
					counts := map[int]source.LineCount{}
					err := snap.NumStat(t.Context(), func(i int, c source.LineCount) { counts[i] = c })
					if err != nil || !reflect.DeepEqual(counts, wantCounts) {
						t.Errorf("concurrent NumStat differs (err %v)", err)
					}
				case 3:
					res, err := snap.Search(t.Context(), needle)
					if err != nil || !reflect.DeepEqual(res, wantSearch) {
						t.Errorf("concurrent Search differs (err %v)", err)
					}
				}
			}
		})
	}
	wg.Wait()
}

// TestPatchAndCountCancellation checks that cancelling Diff while its diff-pairs runs, NumStat
// while its input feed is blocked, and Search while its git grep runs, returns the context's error
// promptly, leaves no such process, running or unreaped, and that the next call succeeds. The
// shim holds diff-pairs and git grep without reading input until the gate goes; NumStat's input is larger than a pipe
// holds, so its feed blocks. Not parallel: the shim changes PATH.
func TestPatchAndCountCancellation(t *testing.T) {
	r := newRepo(t)
	long := strings.Repeat("p", 200)
	const n = 1000
	for i := range n {
		r.write(fmt.Sprintf("%04d-%s.txt", i, long), "old\n")
	}
	r.commit("base")
	for i := range n {
		r.write(fmt.Sprintf("%04d-%s.txt", i, long), "new\n")
	}
	r.commit("head")
	gate := filepath.Join(t.TempDir(), "gate")
	// Every diff-pairs logs its PID, gated or not; the gated one is the shim's shell, which the
	// runner started and must stop.
	logPath := shimGit(t, `*" diff-pairs "*|*" --column "*) echo $$ >> "$LOG"; if [ -e '`+gate+`' ]; then while [ -e '`+gate+`' ]; do sleep 0.05; done; exit 1; fi;;`)
	snap := resolve(t, r.dir, source.Last())
	newNeed := newSearchText(t, "new")

	for _, tc := range []struct {
		name string
		call func(ctx context.Context) error
	}{
		{"Diff", func(ctx context.Context) error {
			_, err := snap.Diff(ctx, 0)
			return err
		}},
		{"NumStat", func(ctx context.Context) error {
			return snap.NumStat(ctx, func(int, source.LineCount) {})
		}},
		{"Search", func(ctx context.Context) error {
			_, err := snap.Search(ctx, newNeed)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(gate, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(gate) })
			started := logLines(t, logPath)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- tc.call(ctx) }()
			deadline := time.Now().Add(10 * time.Second)
			for logLines(t, logPath) == started {
				if time.Now().After(deadline) {
					t.Fatalf("%s: the gated git command never started", tc.name)
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("cancelled %s: err = %v, want context.Canceled", tc.name, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("cancelled %s did not return", tc.name)
			}
			processesGone(t, readPIDs(t, logPath))
			if err := os.Remove(gate); err != nil {
				t.Fatal(err)
			}
			if err := tc.call(t.Context()); err != nil {
				t.Errorf("%s after the cancelled one: %v", tc.name, err)
			}
		})
	}
}
