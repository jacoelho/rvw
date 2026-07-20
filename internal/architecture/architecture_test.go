package architecture_test

import (
	"bufio"
	"fmt"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/jacoelho/rvw"

var allowedInternalImports = map[string][]string{
	"cmd/rvw": {
		"internal/app",
		"internal/config",
		"internal/delivery",
		"internal/output",
		"internal/review",
		"internal/source",
	},
	"internal/app": {
		"internal/delivery",
		"internal/review",
		"internal/source",
	},
	"internal/config":   {},
	"internal/delivery": {"internal/subprocess"},
	"internal/output": {
		"internal/review",
		"internal/source",
	},
	"internal/review":     {},
	"internal/source":     {"internal/review", "internal/subprocess"},
	"internal/subprocess": {},
}

var pureReviewImports = []string{
	"bytes",
	"cmp",
	"container/heap",
	"container/list",
	"crypto/sha256",
	"encoding/base64",
	"encoding/binary",
	"errors",
	"fmt",
	"hash",
	"hash/fnv",
	"iter",
	"maps",
	"math",
	"math/bits",
	"regexp",
	"slices",
	"sort",
	"strconv",
	"strings",
	"unicode",
	"unicode/utf8",
}

func TestDependencyPins(t *testing.T) {
	root := repositoryRoot(t)
	contents, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}

	required := map[string]string{
		"charm.land/bubbles/v2":   "v2.1.1",
		"charm.land/bubbletea/v2": "v2.0.8",
		"charm.land/lipgloss/v2":  "v2.0.5",
	}
	found := parseDirectRequirements(string(contents))
	for module, version := range required {
		if found[module] != version {
			t.Errorf("go.mod must pin %s at %s; got %q", module, version, found[module])
		}
	}
	for line := range strings.Lines(string(contents)) {
		line = strings.TrimSpace(strings.SplitN(line, "//", 2)[0])
		if line == "replace (" || strings.HasPrefix(line, "replace ") ||
			line == "exclude (" || strings.HasPrefix(line, "exclude ") {
			t.Errorf("go.mod replacements and exclusions require an accepted dependency-baseline amendment: %s", line)
		}
	}
}

func TestResolvedDependencyBaseline(t *testing.T) {
	root := repositoryRoot(t)
	command := exec.Command(
		"go",
		"list",
		"-m",
		"-mod=readonly",
		"-f",
		"{{if .Main}}{{.Path}}{{else}}{{.Path}} {{.Version}}{{end}}",
		"all",
	)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("resolve module baseline: %v", err)
	}

	baseline, err := os.ReadFile(filepath.Join(root, "internal", "architecture", "dependencies.txt"))
	if err != nil {
		t.Fatal(err)
	}

	got := nonemptySortedLines(string(output))
	want := nonemptySortedLines(string(baseline))
	if !slices.Equal(got, want) {
		t.Errorf("resolved dependency graph changed\nwant:\n%s\n\ngot:\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
}

func parseDirectRequirements(contents string) map[string]string {
	requirements := make(map[string]string)
	inBlock := false
	for line := range strings.Lines(contents) {
		line = strings.TrimSpace(strings.SplitN(line, "//", 2)[0])
		switch {
		case line == "require (":
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		}

		fields := strings.Fields(line)
		if inBlock && len(fields) == 2 {
			requirements[fields[0]] = fields[1]
		}
		if !inBlock && len(fields) == 3 && fields[0] == "require" {
			requirements[fields[1]] = fields[2]
		}
	}
	return requirements
}

func TestProductionDependencyDirections(t *testing.T) {
	root := repositoryRoot(t)
	files := productionGoFiles(t, root)
	seenPackages := make(map[string]bool)

	for _, filename := range files {
		relative := relativeSlash(t, root, filename)
		owner := pathOwner(relative)
		if owner == "" {
			t.Errorf("%s: production Go code must belong to cmd/rvw or an accepted internal owner", relative)
			continue
		}
		seenPackages[owner] = true

		parsed, err := parser.ParseFile(token.NewFileSet(), filename, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", relative, err)
			continue
		}

		for _, spec := range parsed.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("%s: invalid import %s: %v", relative, spec.Path.Value, err)
				continue
			}
			checkInternalImport(t, relative, owner, importPath)
			checkEffectImport(t, relative, owner, importPath)
			if owner == "internal/review" {
				checkReviewImport(t, relative, importPath)
			}
			if importPath == "C" {
				t.Errorf("%s: CGO is forbidden", relative)
			}
		}
	}

	for owner := range seenPackages {
		if _, ok := allowedInternalImports[owner]; !ok {
			t.Errorf("production package %q has no accepted architecture owner", owner)
		}
	}
}

func TestNoUnsupportedPlatformCode(t *testing.T) {
	root := repositoryRoot(t)
	for _, filename := range goFiles(t, root, true) {
		relative := relativeSlash(t, root, filename)
		platform, err := unsupportedPlatform(filename)
		if err != nil {
			t.Errorf("%s: parse build constraint: %v", relative, err)
			continue
		}
		if platform != "" {
			t.Errorf("%s: Windows and WebAssembly code/tests are forbidden (matched %q)", relative, platform)
		}
	}
}

func TestUnsupportedPlatformDetection(t *testing.T) {
	tests := []struct {
		name       string
		filename   string
		constraint string
		want       string
	}{
		{name: "Windows filename", filename: "process_windows_amd64.go", want: "windows"},
		{name: "Windows test filename", filename: "process_windows_amd64_test.go", want: "windows"},
		{name: "WebAssembly filename", filename: "process_js_wasm.go", want: "js"},
		{name: "Windows constraint", filename: "process.go", constraint: "//go:build windows && amd64\n", want: "windows"},
		{name: "WebAssembly constraint", filename: "process.go", constraint: "//go:build wasip1 && wasm\n", want: "wasip1"},
		{name: "Supported constraint", filename: "process.go", constraint: "//go:build linux || darwin\n"},
		{name: "Explicit Windows exclusion", filename: "process.go", constraint: "//go:build !windows && (linux || darwin)\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), test.filename)
			contents := test.constraint + "package fixture\n"
			if err := os.WriteFile(filename, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := unsupportedPlatform(filename)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("unsupportedPlatform() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRepositoryHasNoRustRuntimeIntegration(t *testing.T) {
	root := repositoryRoot(t)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		name := entry.Name()
		rustArtifact := strings.HasSuffix(name, ".rs") || name == "Cargo.toml" || name == "Cargo.lock"
		if rustArtifact && !isTestFixture(root, path) {
			relative := relativeSlash(t, root, path)
			t.Errorf("Rust runtime artifact is forbidden: %s", relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGoSourceFilesAreCohesive(t *testing.T) {
	root := repositoryRoot(t)
	for _, filename := range goFiles(t, root, true) {
		file, err := os.Open(filename)
		if err != nil {
			t.Error(err)
			continue
		}

		lines := 0
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			lines++
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			t.Errorf("%s: %v", relativeSlash(t, root, filename), scanErr)
			continue
		}
		if closeErr != nil {
			t.Errorf("%s: %v", relativeSlash(t, root, filename), closeErr)
			continue
		}
		if lines > 1_000 {
			relative := relativeSlash(t, root, filename)
			t.Errorf("%s has %d lines; maximum is 1000", relative, lines)
		}
	}
}

func checkInternalImport(t *testing.T, filename, owner, importPath string) {
	t.Helper()

	internalPrefix := modulePath + "/internal/"
	if !strings.HasPrefix(importPath, internalPrefix) {
		return
	}
	target := strings.TrimPrefix(importPath, modulePath+"/")
	allowed, knownOwner := allowedInternalImports[owner]
	if !knownOwner || !slices.Contains(allowed, target) {
		t.Errorf("%s: %s may not import %s", filename, owner, target)
	}
}

func checkReviewImport(t *testing.T, filename, importPath string) {
	t.Helper()

	if !slices.Contains(pureReviewImports, importPath) {
		t.Errorf("%s: review domain import %s is not in the accepted pure-import set", filename, importPath)
	}
}

func checkEffectImport(t *testing.T, filename, owner, importPath string) {
	t.Helper()

	allowed := func(owners ...string) bool {
		return slices.Contains(owners, owner)
	}
	fail := func(effect string) {
		t.Errorf("%s: %s owns %s, but imports %s", filename, owner, effect, importPath)
	}

	switch {
	case importPath == "github.com/charmbracelet/x/windows":
		fail("a forbidden direct Windows dependency")
	case strings.HasPrefix(importPath, "charm.land/bubbletea/v2"):
		if !allowed("cmd/rvw", "internal/app") {
			fail("Bubble Tea")
		}
	case strings.HasPrefix(importPath, "charm.land/bubbles/v2"),
		strings.HasPrefix(importPath, "charm.land/lipgloss/v2"):
		if !allowed("internal/app") {
			fail("interactive component/presentation effects")
		}
	case importPath == "os/exec":
		if !allowed("internal/subprocess") {
			fail("subprocess execution")
		}
	case importPath == "os/signal":
		if !allowed("cmd/rvw") {
			fail("termination-signal registration")
		}
	case importPath == "net" || strings.HasPrefix(importPath, "net/"):
		if !allowed("internal/source") {
			fail("network effects")
		}
	case importPath == "os", importPath == "io/fs", importPath == "path/filepath":
		if !allowed("cmd/rvw", "internal/config", "internal/source", "internal/subprocess") {
			fail("filesystem/process-boundary effects")
		}
	case importPath == "syscall":
		if !allowed("cmd/rvw", "internal/subprocess") {
			fail("operating-system process control")
		}
	case importPath == "context":
		if !allowed("cmd/rvw", "internal/app", "internal/delivery", "internal/source", "internal/subprocess") {
			fail("cancellation/lifecycle effects")
		}
	case importPath == "sync" || strings.HasPrefix(importPath, "sync/"):
		if !allowed("internal/app", "internal/delivery", "internal/subprocess") {
			fail("concurrent ownership")
		}
	case importPath == "time":
		if !allowed("cmd/rvw", "internal/app", "internal/delivery", "internal/source", "internal/subprocess") {
			fail("clock/deadline effects")
		}
	case importPath == "runtime":
		if !allowed("cmd/rvw", "internal/app", "internal/subprocess") {
			fail("runtime/platform behavior")
		}
	case importPath == "io":
		if !allowed("cmd/rvw", "internal/config", "internal/delivery", "internal/output", "internal/source", "internal/subprocess") {
			fail("stream ownership")
		}
	}
}

func productionGoFiles(t *testing.T, root string) []string {
	t.Helper()
	return goFiles(t, root, false)
}

func goFiles(t *testing.T, root string, includeTests bool) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" ||
				entry.Name() == "testdata" || entry.Name() == "fixtures" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".go") &&
			(includeTests || !strings.HasSuffix(entry.Name(), "_test.go")) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func unsupportedPlatform(filename string) (string, error) {
	name := strings.TrimSuffix(filepath.Base(filename), ".go")
	name = strings.TrimSuffix(name, "_test")
	parts := strings.Split(name, "_")
	for _, part := range parts[max(1, len(parts)-2):] {
		if isUnsupportedPlatformTag(part) {
			return part, nil
		}
	}

	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "package ") {
			break
		}
		if !strings.HasPrefix(line, "//go:build ") && !strings.HasPrefix(line, "// +build ") {
			continue
		}
		expression, err := constraint.Parse(line)
		if err != nil {
			return "", err
		}
		if tag := unsupportedConstraintTag(expression, false); tag != "" {
			return tag, nil
		}
	}
	return "", scanner.Err()
}

func unsupportedConstraintTag(expression constraint.Expr, negated bool) string {
	switch expression := expression.(type) {
	case *constraint.TagExpr:
		if !negated && isUnsupportedPlatformTag(expression.Tag) {
			return expression.Tag
		}
	case *constraint.NotExpr:
		return unsupportedConstraintTag(expression.X, !negated)
	case *constraint.AndExpr:
		if tag := unsupportedConstraintTag(expression.X, negated); tag != "" {
			return tag
		}
		return unsupportedConstraintTag(expression.Y, negated)
	case *constraint.OrExpr:
		if tag := unsupportedConstraintTag(expression.X, negated); tag != "" {
			return tag
		}
		return unsupportedConstraintTag(expression.Y, negated)
	}
	return ""
}

func isUnsupportedPlatformTag(tag string) bool {
	return tag == "windows" || tag == "js" || tag == "wasip1" || tag == "wasm"
}

func pathOwner(relative string) string {
	directory := filepath.ToSlash(filepath.Dir(relative))
	switch {
	case directory == "cmd/rvw":
		return directory
	case strings.HasPrefix(directory, "internal/"):
		parts := strings.Split(directory, "/")
		if len(parts) >= 2 {
			return strings.Join(parts[:2], "/")
		}
	}
	return ""
}

func isTestFixture(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		if part == "testdata" || part == "fixtures" {
			return true
		}
	}
	return false
}

func nonemptySortedLines(contents string) []string {
	var lines []string
	for line := range strings.Lines(contents) {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	slices.Sort(lines)
	return lines
}

func repositoryRoot(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate architecture test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func relativeSlash(t *testing.T, root, path string) string {
	t.Helper()

	relative, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatal(fmt.Errorf("relative path for %s: %w", path, err))
	}
	return filepath.ToSlash(relative)
}
