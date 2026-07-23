package source

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp/syntax"
	"unicode/utf8"
)

const (
	maxFilterCompileWork       uint64 = 32 << 20
	filterNFAStateCharge       int64  = 96
	filterNFATransitionCharge  int64  = 32
	filterNFAEpsilonCharge     int64  = 8
	filterAcceptTagCharge      int64  = 24
	filterDFAStateCharge       int64  = 128
	filterDFATransitionCharge  int64  = 48
	filterMatcherScratchCharge int64  = 20
	filterDFAKeyScratchCharge  int64  = 4
	filterDFASetElementCharge  int64  = 8
)

type filterTagKind uint8

const (
	filterTagInclude filterTagKind = iota + 1
	filterTagExclude
	filterTagIgnore
	filterTagGitignore
)

type filterTag struct {
	ordinal uint32
	kind    filterTagKind
	ignored bool
}

type filterRuneTransition struct {
	low  rune
	high rune
	to   int
}

type filterNFAState struct {
	transitions []filterRuneTransition
	epsilon     []int
	accepts     []filterTag
}

type filterDFAState struct {
	transitions map[rune]int
	nfa         []int
	ignoreOrder uint32
	gitOrder    uint32
	flags       filterMatchFlags
}

type filterMatchFlag uint8

const (
	filterMatchInclude filterMatchFlag = iota + 1
	filterMatchExclude
	filterMatchIgnore
	filterMatchGitignore
)

const filterMatchKnownBits = uint8(
	1<<(filterMatchInclude-1) |
		1<<(filterMatchExclude-1) |
		1<<(filterMatchIgnore-1) |
		1<<(filterMatchGitignore-1),
)

// filterMatchFlags is a closed set of independent matcher outcomes. Its raw
// representation never crosses this type's checked operations.
type filterMatchFlags struct {
	bits uint8
}

func (flags *filterMatchFlags) Add(flag filterMatchFlag) error {
	if flags == nil || !validFilterMatchFlag(flag) {
		return errors.New("filter match flag is invalid")
	}
	flags.bits |= 1 << (flag - 1)
	return nil
}

func (flags *filterMatchFlags) Remove(flag filterMatchFlag) error {
	if flags == nil || !validFilterMatchFlag(flag) {
		return errors.New("filter match flag is invalid")
	}
	flags.bits &^= 1 << (flag - 1)
	return nil
}

func (flags *filterMatchFlags) Has(flag filterMatchFlag) bool {
	return flags != nil && validFilterMatchFlag(flag) && flags.valid() &&
		flags.bits&(1<<(flag-1)) != 0
}

func (flags *filterMatchFlags) valid() bool {
	return flags != nil && flags.bits&^filterMatchKnownBits == 0
}

func validFilterMatchFlag(flag filterMatchFlag) bool {
	return flag >= filterMatchInclude && flag <= filterMatchGitignore
}

type compiledFilterSet struct {
	ctx         context.Context
	ledger      *sourceLedger
	stateByKey  map[string]int
	nfa         []filterNFAState
	dfa         []*filterDFAState
	marks       []uint32
	scratch     []int
	sortScratch []int
	keyScratch  []byte
	retained    int64
	compileWork uint64
	inputBytes  uint64
	mark        uint32
	closed      bool
}

func newCompiledFilterSet(
	ctx context.Context,
	ledger *sourceLedger,
	include []FilterPattern,
	exclude []FilterPattern,
	ignore []ignoreRule,
) (_ *compiledFilterSet, returnErr error) {
	if ctx == nil || ledger == nil {
		return nil, errors.New("filter matcher requires context and source ledger")
	}
	matcher := &compiledFilterSet{
		ctx:    ctx,
		ledger: ledger,
	}
	defer func() {
		if returnErr != nil {
			matcher.Close()
		}
	}()
	start, err := matcher.appendNFAState()
	if err != nil {
		return nil, err
	}
	if start != 0 {
		return nil, errors.New("filter matcher start state is invalid")
	}
	for _, pattern := range include {
		if compileErr := matcher.addGlob(
			pattern.String(),
			filterTag{kind: filterTagInclude},
		); compileErr != nil {
			return nil, fmt.Errorf("compile include pattern %q: %w", pattern, compileErr)
		}
	}
	for _, pattern := range exclude {
		if compileErr := matcher.addGlob(
			pattern.String(),
			filterTag{kind: filterTagExclude},
		); compileErr != nil {
			return nil, fmt.Errorf("compile exclude pattern %q: %w", pattern, compileErr)
		}
	}
	for index, rule := range ignore {
		kind := filterTagIgnore
		if rule.gitignore {
			kind = filterTagGitignore
		}
		if compileErr := matcher.addGlob(rule.pattern, filterTag{
			kind:    kind,
			ordinal: uint32(index + 1),
			ignored: !rule.negated,
		}); compileErr != nil {
			return nil, fmt.Errorf("compile ignore pattern %q: %w", rule.pattern, compileErr)
		}
	}
	if reserveErr := matcher.reserve(
		int64(len(matcher.nfa)) *
			(filterMatcherScratchCharge + filterDFAKeyScratchCharge),
	); reserveErr != nil {
		return nil, reserveErr
	}
	matcher.marks = make([]uint32, len(matcher.nfa))
	matcher.scratch = make([]int, 0, len(matcher.nfa))
	matcher.sortScratch = make([]int, len(matcher.nfa))
	matcher.keyScratch = make([]byte, len(matcher.nfa)*int(filterDFAKeyScratchCharge))
	closure, err := matcher.epsilonClosure([]int{0})
	if err != nil {
		return nil, err
	}
	if _, err := matcher.internDFAState(closure); err != nil {
		return nil, err
	}
	return matcher, nil
}

func (matcher *compiledFilterSet) Close() {
	if matcher == nil || matcher.closed {
		return
	}
	matcher.closed = true
	matcher.nfa = nil
	matcher.dfa = nil
	matcher.marks = nil
	matcher.scratch = nil
	matcher.sortScratch = nil
	matcher.keyScratch = nil
	matcher.stateByKey = nil
	matcher.ledger.release(matcher.retained)
	matcher.retained = 0
}

func (matcher *compiledFilterSet) reserve(bytes int64) error {
	if err := matcher.ledger.reserve(bytes); err != nil {
		return err
	}
	matcher.retained += bytes
	return nil
}

func (matcher *compiledFilterSet) release(bytes int64) {
	matcher.ledger.release(bytes)
	matcher.retained -= bytes
}

func (matcher *compiledFilterSet) appendNFAState() (int, error) {
	if err := matcher.reserve(filterNFAStateCharge); err != nil {
		return 0, err
	}
	matcher.nfa = append(matcher.nfa, filterNFAState{})
	return len(matcher.nfa) - 1, nil
}

func (matcher *compiledFilterSet) addEpsilon(from, to int) error {
	if err := matcher.reserve(filterNFAEpsilonCharge); err != nil {
		return err
	}
	matcher.nfa[from].epsilon = append(matcher.nfa[from].epsilon, to)
	return nil
}

func (matcher *compiledFilterSet) addTransition(from, to int, low, high rune) error {
	if err := matcher.reserve(filterNFATransitionCharge); err != nil {
		return err
	}
	matcher.nfa[from].transitions = append(
		matcher.nfa[from].transitions,
		filterRuneTransition{low: low, high: high, to: to},
	)
	return nil
}

func (matcher *compiledFilterSet) addRanges(from, to int, ranges []rune) error {
	for len(ranges) != 0 {
		if len(ranges) < 2 {
			return errors.New("filter rune range is incomplete")
		}
		low := ranges[0]
		ranges = ranges[1:]
		high := ranges[0]
		ranges = ranges[1:]
		if err := matcher.addTransition(from, to, low, high); err != nil {
			return err
		}
	}
	return nil
}

func (matcher *compiledFilterSet) addGlob(pattern string, tag filterTag) error {
	glob, err := parseFilterGlob(pattern)
	if err != nil {
		return err
	}
	current, err := matcher.appendNFAState()
	if err != nil {
		return err
	}
	if epsilonErr := matcher.addEpsilon(0, current); epsilonErr != nil {
		return epsilonErr
	}
	current, err = matcher.addGlobPrefix(current, glob)
	if err != nil {
		return err
	}
	current, err = matcher.addGlobTokens(current, glob.body)
	if err != nil {
		return err
	}
	current, err = matcher.addGlobDirectorySuffix(current, glob)
	if err != nil {
		return err
	}
	if err := matcher.reserve(filterAcceptTagCharge); err != nil {
		return err
	}
	matcher.nfa[current].accepts = append(matcher.nfa[current].accepts, tag)
	return nil
}

type filterGlobAnchoring uint8

const (
	filterGlobUnanchored filterGlobAnchoring = iota
	filterGlobAnchored
)

type filterGlobTarget uint8

const (
	filterGlobPath filterGlobTarget = iota
	filterGlobDirectory
)

type parsedFilterGlob struct {
	body      string
	anchoring filterGlobAnchoring
	target    filterGlobTarget
}

func parseFilterGlob(pattern string) (parsedFilterGlob, error) {
	if pattern == "" || int64(len(pattern)) > 4_096 || !utf8.ValidString(pattern) {
		return parsedFilterGlob{}, errors.New("invalid glob pattern")
	}
	anchoring := filterGlobUnanchored
	if pattern[0] == '/' {
		anchoring = filterGlobAnchored
		pattern = pattern[1:]
	}
	target := filterGlobPath
	if pattern != "" && pattern[len(pattern)-1] == '/' {
		target = filterGlobDirectory
		pattern = pattern[:len(pattern)-1]
	}
	if pattern == "" {
		return parsedFilterGlob{}, errors.New("empty glob pattern")
	}
	return parsedFilterGlob{body: pattern, anchoring: anchoring, target: target}, nil
}

func (matcher *compiledFilterSet) addGlobPrefix(
	current int,
	glob parsedFilterGlob,
) (int, error) {
	if glob.anchoring == filterGlobAnchored {
		return current, nil
	}
	prefixEnd, err := matcher.appendNFAState()
	if err != nil {
		return 0, err
	}
	if epsilonErr := matcher.addEpsilon(current, prefixEnd); epsilonErr != nil {
		return 0, epsilonErr
	}
	prefix, err := matcher.appendNFAState()
	if err != nil {
		return 0, err
	}
	if err := matcher.addEpsilon(current, prefix); err != nil {
		return 0, err
	}
	prefixRanges := []rune{0, utf8.MaxRune}
	if containsSlash(glob.body) {
		prefixRanges = []rune{0, '\n' - 1, '\n' + 1, utf8.MaxRune}
	}
	if err := matcher.addRanges(prefix, prefix, prefixRanges); err != nil {
		return 0, err
	}
	if err := matcher.addTransition(prefix, prefixEnd, '/', '/'); err != nil {
		return 0, err
	}
	return prefixEnd, nil
}

func (matcher *compiledFilterSet) addGlobTokens(current int, pattern string) (int, error) {
	for offset := 0; offset < len(pattern); {
		switch pattern[offset] {
		case '*':
			var err error
			current, offset, err = matcher.addGlobStar(current, pattern, offset)
			if err != nil {
				return 0, err
			}
		case '?':
			next, stateErr := matcher.appendNFAState()
			if stateErr != nil {
				return 0, stateErr
			}
			if err := matcher.addRanges(
				current,
				next,
				[]rune{0, '/' - 1, '/' + 1, utf8.MaxRune},
			); err != nil {
				return 0, err
			}
			current = next
			offset++
		case '[':
			closeOffset := indexByte(pattern[offset+1:], ']')
			if closeOffset < 0 {
				return 0, errors.New("unterminated character class")
			}
			closeOffset += offset + 1
			class := pattern[offset+1 : closeOffset]
			if class == "" || containsSlash(class) {
				return 0, errors.New("invalid character class")
			}
			ranges, rangeErr := parseFilterClass(class)
			if rangeErr != nil {
				return 0, rangeErr
			}
			next, stateErr := matcher.appendNFAState()
			if stateErr != nil {
				return 0, stateErr
			}
			if err := matcher.addRanges(current, next, ranges); err != nil {
				return 0, err
			}
			current = next
			offset = closeOffset + 1
		case '\\':
			if offset+1 == len(pattern) {
				return 0, errors.New("glob pattern has a trailing escape")
			}
			value, size := utf8.DecodeRuneInString(pattern[offset+1:])
			next, stateErr := matcher.appendNFAState()
			if stateErr != nil {
				return 0, stateErr
			}
			if err := matcher.addTransition(current, next, value, value); err != nil {
				return 0, err
			}
			current = next
			offset += size + 1
		default:
			value, size := utf8.DecodeRuneInString(pattern[offset:])
			next, stateErr := matcher.appendNFAState()
			if stateErr != nil {
				return 0, stateErr
			}
			if err := matcher.addTransition(current, next, value, value); err != nil {
				return 0, err
			}
			current = next
			offset += size
		}
	}
	return current, nil
}

func (matcher *compiledFilterSet) addGlobStar(
	current int,
	pattern string,
	offset int,
) (int, int, error) {
	ranges := []rune{0, '/' - 1, '/' + 1, utf8.MaxRune}
	width := 1
	if offset+1 < len(pattern) && pattern[offset+1] == '*' {
		ranges = []rune{0, '\n' - 1, '\n' + 1, utf8.MaxRune}
		width = 2
	}
	next, err := matcher.appendNFAState()
	if err != nil {
		return 0, 0, err
	}
	if err := matcher.addEpsilon(current, next); err != nil {
		return 0, 0, err
	}
	if err := matcher.addRanges(current, current, ranges); err != nil {
		return 0, 0, err
	}
	return next, offset + width, nil
}

func (matcher *compiledFilterSet) addGlobDirectorySuffix(
	current int,
	glob parsedFilterGlob,
) (int, error) {
	if glob.target != filterGlobDirectory {
		return current, nil
	}
	final, err := matcher.appendNFAState()
	if err != nil {
		return 0, err
	}
	if epsilonErr := matcher.addEpsilon(current, final); epsilonErr != nil {
		return 0, epsilonErr
	}
	suffix, err := matcher.appendNFAState()
	if err != nil {
		return 0, err
	}
	if err := matcher.addTransition(current, suffix, '/', '/'); err != nil {
		return 0, err
	}
	if err := matcher.addRanges(
		suffix,
		suffix,
		[]rune{0, '\n' - 1, '\n' + 1, utf8.MaxRune},
	); err != nil {
		return 0, err
	}
	if err := matcher.addEpsilon(suffix, final); err != nil {
		return 0, err
	}
	return final, nil
}

func containsSlash(value string) bool {
	return indexByte(value, '/') >= 0
}

func indexByte(value string, target byte) int {
	for index := range len(value) {
		if value[index] == target {
			return index
		}
	}
	return -1
}

func parseFilterClass(class string) ([]rune, error) {
	if class[0] == '!' {
		class = "^" + class[1:]
	}
	parsed, err := syntax.Parse("["+class+"]", syntax.Perl)
	if err != nil {
		return nil, fmt.Errorf("invalid character class: %w", err)
	}
	switch parsed.Op {
	case syntax.OpCharClass:
		return parsed.Rune, nil
	case syntax.OpLiteral:
		if len(parsed.Rune) != 1 {
			return nil, errors.New("invalid character class")
		}
		return []rune{parsed.Rune[0], parsed.Rune[0]}, nil
	default:
		return nil, errors.New("invalid character class")
	}
}

func (matcher *compiledFilterSet) match(path string) (filterMatchFlags, error) {
	encoding := filterPathInvalidUTF8
	if utf8.ValidString(path) {
		encoding = filterPathValidUTF8
	}
	return matcher.matchPrepared(path, encoding)
}

func (matcher *compiledFilterSet) matchPrepared(
	path string,
	encoding filterPathEncoding,
) (filterMatchFlags, error) {
	if matcher == nil || matcher.closed || encoding != filterPathValidUTF8 {
		return filterMatchFlags{}, nil
	}
	state := 0
	processed := 0
	for _, value := range path {
		next, err := matcher.transition(state, value)
		if err != nil {
			return filterMatchFlags{}, err
		}
		state = next
		size := utf8.RuneLen(value)
		processed += size
		// RuneLen returns one through four for values produced by ranging a string.
		//nolint:gosec // The conversion is lossless for the documented RuneLen range.
		matcher.inputBytes += uint64(size)
		if processed >= sourceCPUPollBytes {
			if err := context.Cause(matcher.ctx); err != nil {
				return filterMatchFlags{}, err
			}
			processed = 0
		}
	}
	return matcher.dfa[state].flags, nil
}

func (matcher *compiledFilterSet) transition(state int, value rune) (int, error) {
	current := matcher.dfa[state]
	if next, ok := current.transitions[value]; ok {
		return next, nil
	}
	matcher.nextMark()
	matcher.scratch = matcher.scratch[:0]
	for _, nfaIndex := range current.nfa {
		if err := matcher.consumeCompileWork(); err != nil {
			return 0, err
		}
		for _, transition := range matcher.nfa[nfaIndex].transitions {
			if err := matcher.consumeCompileWork(); err != nil {
				return 0, err
			}
			if value < transition.low || value > transition.high {
				continue
			}
			if matcher.marks[transition.to] != matcher.mark {
				matcher.marks[transition.to] = matcher.mark
				matcher.scratch = append(matcher.scratch, transition.to)
			}
		}
	}
	closure, err := matcher.expandMarkedClosure()
	if err != nil {
		return 0, err
	}
	next, err := matcher.internDFAState(closure)
	if err != nil {
		return 0, err
	}
	if err := matcher.reserve(filterDFATransitionCharge); err != nil {
		return 0, err
	}
	if current.transitions == nil {
		current.transitions = make(map[rune]int)
	}
	current.transitions[value] = next
	return next, nil
}

func (matcher *compiledFilterSet) epsilonClosure(seeds []int) ([]int, error) {
	matcher.nextMark()
	matcher.scratch = matcher.scratch[:0]
	for _, seed := range seeds {
		if matcher.marks[seed] == matcher.mark {
			continue
		}
		matcher.marks[seed] = matcher.mark
		matcher.scratch = append(matcher.scratch, seed)
	}
	return matcher.expandMarkedClosure()
}

func (matcher *compiledFilterSet) expandMarkedClosure() ([]int, error) {
	for cursor := 0; cursor < len(matcher.scratch); cursor++ {
		state := matcher.scratch[cursor]
		if err := matcher.consumeCompileWork(); err != nil {
			return nil, err
		}
		for _, next := range matcher.nfa[state].epsilon {
			if err := matcher.consumeCompileWork(); err != nil {
				return nil, err
			}
			if matcher.marks[next] == matcher.mark {
				continue
			}
			matcher.marks[next] = matcher.mark
			matcher.scratch = append(matcher.scratch, next)
		}
	}
	if err := matcher.radixStateSet(matcher.scratch); err != nil {
		return nil, err
	}
	return matcher.scratch, nil
}

func (matcher *compiledFilterSet) radixStateSet(values []int) error {
	source := values
	destination := matcher.sortScratch[:len(values)]
	for shift := range 4 {
		var counts [256]int
		bits := uint(shift * 8)
		for _, value := range source {
			if err := matcher.consumeCompileWork(); err != nil {
				return err
			}
			counts[uint(value)>>bits&0xff]++
		}
		var positions [256]int
		for index := 1; index < len(positions); index++ {
			positions[index] = positions[index-1] + counts[index-1]
		}
		for _, value := range source {
			bucket := uint(value) >> bits & 0xff
			destination[positions[bucket]] = value
			positions[bucket]++
		}
		source, destination = destination, source
	}
	if len(source) > 0 && &source[0] != &values[0] {
		copy(values, source)
	}
	return nil
}

func (matcher *compiledFilterSet) internDFAState(set []int) (int, error) {
	keyBytes := matcher.keyScratch[:len(set)*int(filterDFAKeyScratchCharge)]
	for index, value := range set {
		if err := matcher.consumeCompileWork(); err != nil {
			return 0, err
		}
		// NFA state indices are bounded well below uint32 by the source ledger.
		//nolint:gosec // The ledger-enforced bound makes this conversion lossless.
		binary.LittleEndian.PutUint32(
			keyBytes[index*int(filterDFAKeyScratchCharge):],
			uint32(value),
		)
	}
	if state, ok := matcher.stateByKey[string(keyBytes)]; ok {
		return state, nil
	}
	charge := filterDFAStateRetainedBytes(len(set))
	if err := matcher.reserve(charge); err != nil {
		return 0, err
	}
	state := &filterDFAState{nfa: make([]int, len(set))}
	for index, nfaIndex := range set {
		if err := matcher.consumeCompileWork(); err != nil {
			matcher.release(charge)
			return 0, err
		}
		state.nfa[index] = nfaIndex
		for _, tag := range matcher.nfa[nfaIndex].accepts {
			if err := matcher.consumeCompileWork(); err != nil {
				matcher.release(charge)
				return 0, err
			}
			if err := state.applyTag(tag); err != nil {
				matcher.release(charge)
				return 0, err
			}
		}
	}
	key := string(keyBytes)
	index := len(matcher.dfa)
	matcher.dfa = append(matcher.dfa, state)
	if matcher.stateByKey == nil {
		matcher.stateByKey = make(map[string]int)
	}
	matcher.stateByKey[key] = index
	return index, nil
}

func (state *filterDFAState) applyTag(tag filterTag) error {
	switch tag.kind {
	case filterTagInclude:
		return state.flags.Add(filterMatchInclude)
	case filterTagExclude:
		return state.flags.Add(filterMatchExclude)
	case filterTagIgnore:
		return state.applyOrderedIgnore(tag, filterMatchIgnore, &state.ignoreOrder)
	case filterTagGitignore:
		return state.applyOrderedIgnore(tag, filterMatchGitignore, &state.gitOrder)
	default:
		return errors.New("filter accept tag kind is invalid")
	}
}

func (state *filterDFAState) applyOrderedIgnore(
	tag filterTag,
	flag filterMatchFlag,
	order *uint32,
) error {
	if tag.ordinal < *order {
		return nil
	}
	*order = tag.ordinal
	if tag.ignored {
		return state.flags.Add(flag)
	}
	return state.flags.Remove(flag)
}

func filterDFAStateRetainedBytes(stateCount int) int64 {
	return filterDFAStateCharge + int64(stateCount)*
		(filterDFASetElementCharge+filterDFAKeyScratchCharge)
}

func (matcher *compiledFilterSet) nextMark() {
	matcher.mark++
	if matcher.mark != 0 {
		return
	}
	clear(matcher.marks)
	matcher.mark = 1
}

func (matcher *compiledFilterSet) consumeCompileWork() error {
	if matcher.compileWork == maxFilterCompileWork {
		return &LimitError{Resource: LimitFilterAutomaton, Limit: maxFilterCompileWork}
	}
	matcher.compileWork++
	if matcher.compileWork%2_048 == 0 {
		return context.Cause(matcher.ctx)
	}
	return nil
}
