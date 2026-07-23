package source

import (
	"context"
	"encoding/binary"
	"errors"
	"regexp"
	"testing"
)

func TestCompiledFilterSetPreservesGlobSemantics(t *testing.T) {
	patterns := []string{
		"*.go",
		"/root?.txt",
		"src/**/[a-z].go",
		"docs/",
		"hé?.[α-ω]",
		"line[!0-9].txt",
	}
	paths := []string{
		"main.go",
		"nested/main.go",
		"root1.txt",
		"nested/root1.txt",
		"src/a/b/c.go",
		"src/a/λ.go",
		"docs",
		"docs/guide/readme.md",
		"héλ.β",
		"linez.txt",
		"line5.txt",
		"dir\nsegment/main.go",
	}
	include := make([]FilterPattern, 0, len(patterns))
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, value := range patterns {
		pattern, err := NewFilterPattern(value)
		if err != nil {
			t.Fatal(err)
		}
		include = append(include, pattern)
		reference, err := compileGlob(value)
		if err != nil {
			t.Fatal(err)
		}
		compiled = append(compiled, reference)
	}
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := newCompiledFilterSet(t.Context(), ledger, include, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer matcher.Close()
	for _, path := range paths {
		want := false
		for _, reference := range compiled {
			want = want || reference.MatchString(path)
		}
		got, err := matcher.match(path)
		if err != nil {
			t.Fatal(err)
		}
		if got.Has(filterMatchInclude) != want {
			t.Errorf("path %q include=%v, want %v", path, got.Has(filterMatchInclude), want)
		}
	}
}

func TestFilterMatchFlagsRejectUnknownMembersAndBits(t *testing.T) {
	var flags filterMatchFlags
	if err := flags.Add(filterMatchFlag(0)); err == nil {
		t.Fatal("zero filter match flag was accepted")
	}
	if err := flags.Add(filterMatchFlag(5)); err == nil {
		t.Fatal("unknown filter match flag was accepted")
	}
	if err := flags.Remove(filterMatchFlag(5)); err == nil {
		t.Fatal("unknown filter match flag removal was accepted")
	}
	flags.bits = 1 << 7
	if flags.valid() {
		t.Fatal("unknown filter match bits were accepted")
	}
	if flags.Has(filterMatchInclude) {
		t.Fatal("invalid filter match set reported membership")
	}
	if _, err := filterFlags("main.go", filterPathFacts{}, OpenOptions{}, flags); err == nil {
		t.Fatal("invalid filter match set was accepted by projection")
	}
}

func TestCompiledFilterSetRequiresPathComponentBoundary(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{name: "literal path", pattern: "main.go", path: "main.go", want: true},
		{name: "literal nested path", pattern: "main.go", path: "nested/main.go", want: true},
		{name: "literal mid-component", pattern: "main.go", path: "notmain.go"},
		{name: "slash path", pattern: "src/x", path: "src/x", want: true},
		{name: "slash nested path", pattern: "src/x", path: "nested/src/x", want: true},
		{name: "slash mid-component", pattern: "src/x", path: "foosrc/x"},
	}
	tags := []struct {
		name  string
		kind  filterTagKind
		match func(filterMatchFlags) bool
	}{
		{name: "include", kind: filterTagInclude, match: func(match filterMatchFlags) bool {
			return match.Has(filterMatchInclude)
		}},
		{name: "exclude", kind: filterTagExclude, match: func(match filterMatchFlags) bool {
			return match.Has(filterMatchExclude)
		}},
		{name: "ignore", kind: filterTagIgnore, match: func(match filterMatchFlags) bool {
			return match.Has(filterMatchIgnore)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reference, err := compileGlob(test.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if got := reference.MatchString(test.path); got != test.want {
				t.Fatalf("reference match = %v, want %v", got, test.want)
			}
			for _, tag := range tags {
				t.Run(tag.name, func(t *testing.T) {
					ledger, err := newSourceLedger(0)
					if err != nil {
						t.Fatal(err)
					}
					matcher := newFilterMatcherWithTag(t, ledger, test.pattern, tag.kind)
					defer matcher.Close()
					matched, err := matcher.match(test.path)
					if err != nil {
						t.Fatal(err)
					}
					if got := tag.match(matched); got != test.want {
						t.Errorf("match = %v, want %v", got, test.want)
					}
				})
			}
		})
	}
}

func newFilterMatcherWithTag(
	t *testing.T,
	ledger *sourceLedger,
	pattern string,
	kind filterTagKind,
) *compiledFilterSet {
	t.Helper()
	value, err := NewFilterPattern(pattern)
	if err != nil {
		t.Fatal(err)
	}
	var include, exclude []FilterPattern
	var ignore []ignoreRule
	switch kind {
	case filterTagInclude:
		include = []FilterPattern{value}
	case filterTagExclude:
		exclude = []FilterPattern{value}
	case filterTagIgnore:
		ignore = []ignoreRule{{pattern: pattern}}
	default:
		t.Fatalf("unknown filter tag kind %d", kind)
	}
	matcher, err := newCompiledFilterSet(t.Context(), ledger, include, exclude, ignore)
	if err != nil {
		t.Fatal(err)
	}
	return matcher
}

func TestCompiledFilterSetKeepsIndependentAndOrderedResults(t *testing.T) {
	include, _ := NewFilterPattern("*.go")
	exclude, _ := NewFilterPattern("main.*")
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := newCompiledFilterSet(
		t.Context(),
		ledger,
		[]FilterPattern{include},
		[]FilterPattern{exclude},
		[]ignoreRule{
			{pattern: "*.go"},
			{pattern: "main.go", negated: true},
			{pattern: "/nested/main.go"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer matcher.Close()
	for _, test := range []struct {
		path                     string
		include, exclude, ignore bool
	}{
		{path: "main.go", include: true, exclude: true},
		{path: "other.go", include: true, ignore: true},
		{path: "nested/main.go", include: true, exclude: true, ignore: true},
		{path: string([]byte{'b', 0xff, '.', 'g', 'o'})},
	} {
		match, err := matcher.match(test.path)
		if err != nil {
			t.Fatal(err)
		}
		if match.Has(filterMatchInclude) != test.include ||
			match.Has(filterMatchExclude) != test.exclude ||
			match.Has(filterMatchIgnore) != test.ignore {
			t.Errorf("path %q match=%+v", test.path, match)
		}
	}
}

func TestCompiledFilterSetReleasesLedger(t *testing.T) {
	pattern, _ := NewFilterPattern("*.go")
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := newCompiledFilterSet(
		t.Context(), ledger, []FilterPattern{pattern}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if ledger.used == 0 {
		t.Fatal("matcher retained no charged state")
	}
	matcher.Close()
	if ledger.used != 0 {
		t.Fatalf("matcher ledger after close = %d", ledger.used)
	}
}

func TestCompiledFilterSetAdmissionIncludesCanonicalKeyScratch(t *testing.T) {
	baseline, baselineLedger := newFilterMatcherForTest(t, t.Context())
	exact := baselineLedger.used
	baseline.Close()

	pattern, err := NewFilterPattern("*.go")
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := newSourceLedgerWithLimit(exact, 0)
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := newCompiledFilterSet(
		t.Context(), ledger, []FilterPattern{pattern}, nil, nil,
	)
	if err != nil {
		t.Fatalf("exact admission rejected: %v", err)
	}
	if ledger.used != exact {
		t.Fatalf("exact admission used = %d, want %d", ledger.used, exact)
	}
	matcher.Close()

	ledger, err = newSourceLedgerWithLimit(exact-1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newCompiledFilterSet(
		t.Context(), ledger, []FilterPattern{pattern}, nil, nil,
	); !errors.Is(err, ErrSourceLimited) {
		t.Fatalf("one-byte-over error = %v", err)
	}
	if ledger.used != 0 {
		t.Fatalf("failed admission retained %d bytes", ledger.used)
	}
}

func TestCompiledFilterSetNovelDFAStateAdmissionIsExact(t *testing.T) {
	matcher, ledger := newFilterMatcherForTest(t, t.Context())
	defer matcher.Close()
	set := novelAcceptingDFASet(t, matcher)
	start := ledger.used
	charge := filterDFAStateRetainedBytes(len(set))
	ledger.limit = start + charge

	if _, err := matcher.internDFAState(set); err != nil {
		t.Fatalf("exact state admission rejected: %v", err)
	}
	if ledger.used != start+charge {
		t.Fatalf("state admission used = %d, want %d", ledger.used, start+charge)
	}

	limited, limitedLedger := newFilterMatcherForTest(t, t.Context())
	defer limited.Close()
	set = novelAcceptingDFASet(t, limited)
	start = limitedLedger.used
	limitedLedger.limit = start + filterDFAStateRetainedBytes(len(set)) - 1
	if _, err := limited.internDFAState(set); !errors.Is(err, ErrSourceLimited) {
		t.Fatalf("one-byte-over state error = %v", err)
	}
	if limitedLedger.used != start {
		t.Fatalf("failed state admission retained %d bytes", limitedLedger.used-start)
	}
}

func TestCompiledFilterSetExistingDFAKeyLookupDoesNotAllocate(t *testing.T) {
	matcher, _ := newFilterMatcherForTest(t, t.Context())
	defer matcher.Close()
	set := matcher.dfa[0].nfa
	var (
		state     int
		lookupErr error
	)
	allocations := testing.AllocsPerRun(1_000, func() {
		state, lookupErr = matcher.internDFAState(set)
	})
	if lookupErr != nil {
		t.Fatal(lookupErr)
	}
	if state != 0 {
		t.Fatalf("existing state = %d, want 0", state)
	}
	if allocations != 0 {
		t.Fatalf("existing state lookup allocations = %v, want 0", allocations)
	}
}

func TestCompiledFilterSetStoredDFAKeyDoesNotAliasScratch(t *testing.T) {
	matcher, _ := newFilterMatcherForTest(t, t.Context())
	defer matcher.Close()
	set := matcher.dfa[0].nfa
	key := encodedDFAKey(set)
	clear(matcher.keyScratch)

	state, ok := matcher.stateByKey[key]
	if !ok || state != 0 {
		t.Fatalf("stored key after scratch overwrite = (%d, %v), want (0, true)", state, ok)
	}
	state, err := matcher.internDFAState(set)
	if err != nil {
		t.Fatal(err)
	}
	if state != 0 {
		t.Fatalf("lookup after scratch reuse = %d, want 0", state)
	}
}

func TestCompiledFilterSetDFAStateConstructionReleasesAdmissionOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	matcher, ledger := newFilterMatcherForTest(t, ctx)
	defer matcher.Close()
	set := novelAcceptingDFASet(t, matcher)
	start := ledger.used
	cancel()
	// One key-encoding unit, one exact-copy unit, then the accepting-tag scan
	// reaches the cancellation checkpoint.
	matcher.compileWork = 2_045

	if _, err := matcher.internDFAState(set); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled state construction error = %v", err)
	}
	if ledger.used != start {
		t.Fatalf("cancelled state construction retained %d bytes", ledger.used-start)
	}
	if _, ok := matcher.stateByKey[encodedDFAKey(set)]; ok {
		t.Fatal("cancelled state construction published its key")
	}
}

func TestCompiledFilterSetDFAKeyEncodingConsumesCompileWork(t *testing.T) {
	matcher, ledger := newFilterMatcherForTest(t, t.Context())
	defer matcher.Close()
	start := ledger.used
	matcher.compileWork = maxFilterCompileWork

	_, err := matcher.internDFAState(matcher.dfa[0].nfa)
	limitErr, ok := errors.AsType[*LimitError](err)
	if !ok || limitErr.Resource != LimitFilterAutomaton {
		t.Fatalf("compile-work error = %v", err)
	}
	if ledger.used != start {
		t.Fatalf("exhausted key encoding retained %d bytes", ledger.used-start)
	}
}

func TestCompiledFilterSetDFATransitionAdmissionIsIndependent(t *testing.T) {
	matcher, ledger := newFilterMatcherForTest(t, t.Context())
	defer matcher.Close()
	prepareExistingTransitionTarget(t, matcher, 'm')
	start := ledger.used
	ledger.limit = start + filterDFATransitionCharge

	if _, err := matcher.transition(0, 'm'); err != nil {
		t.Fatalf("exact transition admission rejected: %v", err)
	}
	if ledger.used != start+filterDFATransitionCharge {
		t.Fatalf(
			"transition admission used = %d, want %d",
			ledger.used,
			start+filterDFATransitionCharge,
		)
	}

	limited, limitedLedger := newFilterMatcherForTest(t, t.Context())
	defer limited.Close()
	prepareExistingTransitionTarget(t, limited, 'm')
	start = limitedLedger.used
	limitedLedger.limit = start + filterDFATransitionCharge - 1
	if _, err := limited.transition(0, 'm'); !errors.Is(err, ErrSourceLimited) {
		t.Fatalf("one-byte-over transition error = %v", err)
	}
	if limitedLedger.used != start {
		t.Fatalf("failed transition admission retained %d bytes", limitedLedger.used-start)
	}
	if limited.dfa[0].transitions != nil {
		t.Fatal("failed transition admission allocated its transition map")
	}
}

func newFilterMatcherForTest(
	t *testing.T,
	ctx context.Context,
) (*compiledFilterSet, *sourceLedger) {
	t.Helper()
	pattern, err := NewFilterPattern("*.go")
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := newCompiledFilterSet(
		ctx, ledger, []FilterPattern{pattern}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return matcher, ledger
}

func novelAcceptingDFASet(t *testing.T, matcher *compiledFilterSet) []int {
	t.Helper()
	for index := range matcher.nfa {
		if len(matcher.nfa[index].accepts) == 0 {
			continue
		}
		set := []int{index}
		if _, ok := matcher.stateByKey[encodedDFAKey(set)]; !ok {
			return set
		}
	}
	t.Fatal("matcher has no novel accepting DFA set")
	return nil
}

func encodedDFAKey(set []int) string {
	key := make([]byte, len(set)*int(filterDFAKeyScratchCharge))
	for index, value := range set {
		binary.LittleEndian.PutUint32(
			key[index*int(filterDFAKeyScratchCharge):],
			uint32(value),
		)
	}
	return string(key)
}

func prepareExistingTransitionTarget(
	t *testing.T,
	matcher *compiledFilterSet,
	value rune,
) {
	t.Helper()
	if _, err := matcher.transition(0, value); err != nil {
		t.Fatal(err)
	}
	matcher.dfa[0].transitions = nil
	matcher.release(filterDFATransitionCharge)
}
