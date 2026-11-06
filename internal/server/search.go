package server

import (
	"net/http"
	"strings"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// searchJSON is a content search's reply. Truncated reports that the search stopped at one of its
// bounds, so files may hold matches that are not listed.
type searchJSON struct {
	Matches   []matchJSON `json:"matches"`
	Truncated bool        `json:"truncated"`
}

// matchJSON is one matched line. File is the changed-file index, so no raw path crosses the wire.
// Before, Match and After are the window of the line around the match, valid UTF-8; Match is never
// empty. CutStart and CutEnd report that the line goes on before or after the window.
type matchJSON struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	File     int         `json:"file"`
	Side     review.Side `json:"side"`
	Line     int         `json:"line"`
	Before   string      `json:"before"`
	Match    string      `json:"match"`
	After    string      `json:"after"`
	CutStart bool        `json:"cut_start"`
	CutEnd   bool        `json:"cut_end"`
}

// getSearch searches the changed files' shown sides for the text q (REQ-NAV-007). q must be given
// once and be valid search text; it is checked before a slot is taken.
func (s *Server) getSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()["q"]
	if len(q) != 1 {
		http.Error(w, "q must be given once", http.StatusBadRequest)
		return
	}
	text, err := source.NewSearchText(q[0])
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err = s.acquire(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	defer s.release()
	res, err := s.snap.Search(r.Context(), text)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := searchJSON{Matches: make([]matchJSON, 0, len(res.Matches)), Truncated: res.Truncated}
	for _, m := range res.Matches {
		side := review.Old
		if m.NewSide {
			side = review.New
		}
		out.Matches = append(out.Matches, matchJSON{
			File:     m.File,
			Side:     side,
			Line:     m.Line,
			Before:   strings.ToValidUTF8(m.Before, "�"),
			Match:    strings.ToValidUTF8(m.Matched, "�"),
			After:    strings.ToValidUTF8(m.After, "�"),
			CutStart: m.CutStart,
			CutEnd:   m.CutEnd,
		})
	}
	s.writeJSON(w, out)
}
