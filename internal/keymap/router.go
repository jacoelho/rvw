package keymap

// MaxNumericRepeat bounds an explicit numeric action prefix.
const MaxNumericRepeat = 9_999

// Route describes how one normalized key stroke was handled.
type Route struct {
	Action            Action
	TimeoutGeneration uint64
	Consumed          bool
}

// Router owns chord, scope, timeout, numeric-repeat, and automatic-repeat
// policy for one input stream.
type Router struct {
	bindings   *Map
	scope      Scope
	strokes    []string
	generation uint64
	repeat     uint16
}

// NewRouter constructs a router over an immutable effective map.
func NewRouter(bindings *Map) Router {
	return Router{bindings: bindings, strokes: make([]string, 0, MaxChordStrokes)}
}

// SetMap replaces the immutable map and clears all in-flight input state.
func (r *Router) SetMap(bindings *Map) {
	r.bindings = bindings
	r.Abandon()
}

// RouteStroke resolves one normalized stroke, retrying a failed continuation
// as a new chord exactly once.
func (r *Router) RouteStroke(scope Scope, stroke string, automatic bool) Route {
	if stroke == "" {
		return Route{}
	}
	r.ResetScope(scope)
	match := r.resolveWith(stroke)
	if match.Exact {
		r.complete()
		return Route{Action: repeatedAction(match.Action, automatic), Consumed: true}
	}
	if match.Prefix {
		return r.beginPrefix(scope, stroke, false)
	}
	r.Abandon()
	match = r.bindings.Resolve(scope, []string{stroke})
	if match.Exact {
		return Route{Action: repeatedAction(match.Action, automatic), Consumed: true}
	}
	if match.Prefix {
		return r.beginPrefix(scope, stroke, true)
	}
	return Route{}
}

func (r *Router) resolveWith(stroke string) Match {
	if len(r.strokes) >= MaxChordStrokes {
		return Match{}
	}
	var candidate [MaxChordStrokes]string
	copy(candidate[:], r.strokes)
	candidate[len(r.strokes)] = stroke
	return r.bindings.Resolve(r.scope, candidate[:len(r.strokes)+1])
}

func (r *Router) beginPrefix(scope Scope, stroke string, reset bool) Route {
	if reset {
		r.strokes = r.strokes[:0]
	}
	r.strokes = append(r.strokes, stroke)
	r.scope = scope
	r.generation++
	return Route{Consumed: true, TimeoutGeneration: r.generation}
}

// ResetScope clears chord and repeat state when the concrete owner changes.
func (r *Router) ResetScope(scope Scope) {
	if len(r.strokes) == 0 {
		r.scope = scope
		return
	}
	if r.scope == scope {
		return
	}
	r.Abandon()
}

// AcceptNumeric consumes a numeric prefix when the active surface permits it.
// The second result reports overflow so the caller can present a notice.
func (r *Router) AcceptNumeric(stroke string, allowed bool) (bool, bool) {
	if !allowed || len(r.strokes) != 0 || len(stroke) != 1 ||
		stroke[0] < '0' || stroke[0] > '9' {
		return false, false
	}
	digit := uint16(stroke[0] - '0')
	if r.repeat == 0 && digit == 0 {
		return false, false
	}
	next := uint32(r.repeat)*10 + uint32(digit)
	if next > MaxNumericRepeat {
		r.repeat = 0
		return true, true
	}
	r.repeat = uint16(next)
	return true, false
}

// TakeRepeat returns and clears the numeric repeat for an action.
func (r *Router) TakeRepeat(action Action) int {
	repeat := r.repeat
	r.repeat = 0
	if repeat == 0 || !repeatable(action) {
		return 1
	}
	return int(repeat)
}

// Timeout abandons the matching pending chord and fences stale timeouts.
func (r *Router) Timeout(generation uint64) bool {
	if generation != r.generation || len(r.strokes) == 0 {
		return false
	}
	r.Abandon()
	return true
}

// Abandon clears pending chord and repeat state.
func (r *Router) Abandon() {
	r.complete()
	r.repeat = 0
}

func (r *Router) complete() {
	if len(r.strokes) != 0 {
		r.generation++
	}
	clear(r.strokes)
	r.strokes = r.strokes[:0]
	r.scope = Scope{}
}

// PendingStrokes returns an owned snapshot for diagnostics and tests.
func (r *Router) PendingStrokes() []string { return append([]string(nil), r.strokes...) }

// Generation returns the current timeout generation.
func (r *Router) Generation() uint64 { return r.generation }

// Repeat returns the pending numeric repeat.
func (r *Router) Repeat() uint16 { return r.repeat }

func repeatedAction(action Action, automatic bool) Action {
	if automatic && !acceptsAutomaticRepeat(action) {
		return ActionNone
	}
	return action
}

func repeatable(action Action) bool {
	switch action {
	case ActionFocusNextPane, ActionFocusPreviousPane, ActionMoveUp, ActionMoveDown,
		ActionPageUp, ActionPageDown, ActionHalfPageUp, ActionHalfPageDown,
		ActionNextFile, ActionPreviousFile, ActionNextHunk, ActionPreviousHunk,
		ActionNextComment, ActionPreviousComment, ActionExtendUp, ActionExtendDown:
		return true
	default:
		return false
	}
}

func acceptsAutomaticRepeat(action Action) bool {
	switch action {
	case ActionFocusNextPane, ActionFocusPreviousPane, ActionMoveUp, ActionMoveDown,
		ActionScrollLeft, ActionScrollRight, ActionPageUp, ActionPageDown,
		ActionHalfPageUp, ActionHalfPageDown, ActionNextFile, ActionPreviousFile,
		ActionNextHunk, ActionPreviousHunk, ActionNextComment, ActionPreviousComment,
		ActionNextSearchHit, ActionPreviousSearchHit, ActionExtendUp, ActionExtendDown,
		ActionDeleteBackward, ActionInsertNewline, ActionNextHelpPage,
		ActionPreviousHelpPage, ActionHelpLineUp, ActionHelpLineDown,
		ActionHelpPageUp, ActionHelpPageDown:
		return true
	default:
		return false
	}
}
