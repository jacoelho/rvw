package source

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
)

// maxLsTree bounds one ls-tree run's output: one record per go.sum pathspec.
const maxLsTree = 16 << 20

// VendoredDir returns changed file i's vendored directory without a trailing slash, or "" when
// the file is not vendored. It panics unless 0 <= i < FileCount().
//
// A vendored directory is a directory of the file's displayed path, not its own name, that is
// named exactly node_modules, or named exactly vendor with a regular file go.sum beside it (a Go
// module's vendor directory). go.sum is looked up in the displayed side's tree: the head tree, or
// the base tree for a deleted file. The outermost such directory wins, so each Go module's
// vendor directory is its own vendored directory.
func (s *Snapshot) VendoredDir(i int) string { return s.vendored[i] }

// vendoredDirs computes VendoredDir for every file: one ls-tree per tree, batched by pathspec
// size, over the go.sum candidates beside every vendor directory.
func (s *Snapshot) vendoredDirs(ctx context.Context) ([]string, error) {
	// goSum[tree][p] reports whether p is a regular file in tree; keys are the candidates.
	goSum := map[string]map[string]bool{s.headTree: {}, s.baseTree: {}}
	for _, f := range s.files {
		tree := s.displayedTree(f)
		for _, dir := range vendorDirs(f.Path()) {
			goSum[tree][goSumBeside(dir)] = false
		}
	}
	for _, tree := range []string{s.headTree, s.baseTree} {
		if err := s.markRegularFiles(ctx, tree, goSum[tree]); err != nil {
			return nil, fmt.Errorf("find go.sum files: %w", err)
		}
	}
	out := make([]string, len(s.files))
	for i, f := range s.files {
		out[i] = vendoredDir(f.Path(), goSum[s.displayedTree(f)])
	}
	return out, nil
}

func (s *Snapshot) displayedTree(f ChangedFile) string {
	if f.NewPath == "" {
		return s.baseTree
	}
	return s.headTree
}

// vendorDirs returns the directories of p named exactly vendor, outermost first.
func vendorDirs(p string) []string {
	var out []string
	for end := 0; ; {
		i := strings.IndexByte(p[end:], '/')
		if i < 0 {
			return out
		}
		if p[end:end+i] == "vendor" {
			out = append(out, p[:end+i])
		}
		end += i + 1
	}
}

// goSumBeside returns the path of the go.sum file in vendor directory dir's parent.
func goSumBeside(dir string) string {
	if parent := path.Dir(dir); parent != "." {
		return parent + "/go.sum"
	}
	return "go.sum"
}

// vendoredDir is VendoredDir's rule for one displayed path, given which go.sum paths are
// regular files in the displayed side's tree.
func vendoredDir(p string, goSum map[string]bool) string {
	for end := 0; ; {
		i := strings.IndexByte(p[end:], '/')
		if i < 0 {
			return ""
		}
		dir := p[:end+i]
		switch p[end : end+i] {
		case "node_modules":
			return dir
		case "vendor":
			if goSum[goSumBeside(dir)] {
				return dir
			}
		}
		end += i + 1
	}
}

// markRegularFiles sets paths[p] for every key p that is a regular file in tree.
func (s *Snapshot) markRegularFiles(ctx context.Context, tree string, paths map[string]bool) error {
	return pathspecBatches(slices.Sorted(maps.Keys(paths)), func(batch []string) error {
		return s.lsTree(ctx, tree, batch, paths)
	})
}

// lsTree lists pathspecs in tree; the runner's --literal-pathspecs makes each name one path, and
// --full-tree makes it relative to the top level.
func (s *Snapshot) lsTree(ctx context.Context, tree string, pathspecs []string, paths map[string]bool) error {
	args := append([]string{"ls-tree", "-z", "--full-tree", tree, "--"}, pathspecs...)
	out, err := s.repo.run.output(ctx, maxLsTree, nil, args...)
	if err != nil {
		return err
	}
	// -z records: "<mode> SP <type> SP <object> TAB <path> NUL".
	for rest := out; len(rest) > 0; {
		rec, after, ok := bytes.Cut(rest, []byte{0})
		if !ok {
			return fmt.Errorf("ls-tree: unterminated record %q", truncate(rest, 40))
		}
		rest = after
		meta, p, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			return fmt.Errorf("ls-tree: malformed record %q", truncate(rec, 40))
		}
		mode, _, _ := bytes.Cut(meta, []byte{' '})
		if _, want := paths[string(p)]; want && (string(mode) == "100644" || string(mode) == "100755") {
			paths[string(p)] = true
		}
	}
	return nil
}
