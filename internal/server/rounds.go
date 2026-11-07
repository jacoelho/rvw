package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/round"
)

// The wire side of review rounds (contract revision 3): each file's round state, the earlier
// comments and their placements, the interdiff and the whitespace-only marks. round owns
// every decision; this file translates.

// roundJSON is a reviewed file's round state; the zero value, omitted, is an unreviewed file's.
type roundJSON struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	State      string    `json:"state"`
	ReviewedWs bool      `json:"reviewed_ws"`
	ReviewedAt time.Time `json:"reviewed_at"`
}

func stateName(st round.State) (string, error) {
	switch st {
	case round.StateSame:
		return "same", nil
	case round.StateChanged:
		return "changed", nil
	case round.StateBaseChanged:
		return "base", nil
	case round.StateChangedAndBase:
		return "changed_base", nil
	case round.StateUnreviewed:
	}
	return "", fmt.Errorf("unknown round state %d", st)
}

// toRoundJSON returns file i's round state, the zero value when it is unreviewed.
func toRoundJSON(p *round.Prior, i int) (roundJSON, error) {
	st, r := p.State(i)
	if st == round.StateUnreviewed {
		return roundJSON{}, nil
	}
	name, err := stateName(st)
	if err != nil {
		return roundJSON{}, err
	}
	return roundJSON{State: name, ReviewedWs: r.Whitespace, ReviewedAt: r.At}, nil
}

// earlierStatusName returns an earlier comment's status on the wire; "" while it is pending, until
// its file is requested.
func earlierStatusName(st round.Status) (string, error) {
	switch st {
	case round.StatusPending:
		return "", nil
	case round.StatusCurrent:
		return "current", nil
	case round.StatusOutdated:
		return "outdated", nil
	case round.StatusUnplaced:
		return "unplaced", nil
	default:
		return "", fmt.Errorf("unknown earlier comment status %d", st)
	}
}

// earlierJSON is an earlier comment as the session document lists it. File is the changed file
// with the comment's file key, null for a review comment or a key no file has. Status is absent
// while pending.
type earlierJSON struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	ID     string        `json:"id"`
	Author review.Author `json:"author"`
	Body   string        `json:"body"`
	At     time.Time     `json:"at"`
	Target targetJSON    `json:"target"`
	File   *int          `json:"file"`
	Status string        `json:"status,omitzero"`
}

func toEarlierJSON(at time.Time, c round.PriorComment) (earlierJSON, error) {
	status, err := earlierStatusName(c.Status)
	if err != nil {
		return earlierJSON{}, err
	}
	j := earlierJSON{
		ID: c.ID, Author: c.Author, Body: c.Body, At: at, Target: toTargetJSON(c.Target), Status: status,
	}
	if c.File >= 0 {
		j.File = &c.File
	}
	return j, nil
}

// placementOutJSON is an earlier line or range comment as file i's request places it: its placed
// lines while current, its original lines and quote while outdated.
type placementOutJSON struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	ID        string      `json:"id"`
	Status    string      `json:"status"`
	Side      review.Side `json:"side"`
	Start     int         `json:"start"`
	End       int         `json:"end"`
	Quote     []string    `json:"quote,omitzero"`
	QuoteNote string      `json:"quote_note,omitzero"`
}

// placementsJSON returns the earlier line and range comments of one file as placed in the snapshot.
func placementsJSON(prior *round.Prior, placed []round.Placed) ([]placementOutJSON, error) {
	out := make([]placementOutJSON, 0, len(placed))
	for _, pl := range placed {
		c, ok := prior.Find(pl.ID)
		if !ok {
			return nil, fmt.Errorf("placed earlier comment %s is not listed", pl.ID)
		}
		status, err := earlierStatusName(pl.Status)
		if err != nil || status == "" {
			return nil, errors.Join(fmt.Errorf("earlier comment %s has no final status", pl.ID), err)
		}
		start, end := pl.Start, pl.End
		if pl.Status == round.StatusOutdated {
			start, end = c.Target.Lines()
		}
		j := placementOutJSON{ID: pl.ID, Status: status, Side: c.Target.Side(), Start: start, End: end}
		if pl.Quote != nil {
			j.Quote = make([]string, len(pl.Quote.Code))
			for k, l := range pl.Quote.Code {
				j.Quote[k] = strings.ToValidUTF8(l, "�")
			}
			j.QuoteNote = pl.Quote.Note()
		}
		out = append(out, j)
	}
	return out, nil
}

// fallbackName names why a file shows all its changes instead of its interdiff, "" when
// it is shown or the reason is not one the UI shows.
func fallbackName(f round.Fallback) (string, error) {
	switch f {
	case round.NoFallback, round.FallbackNotChanged:
		return "", nil
	case round.FallbackMissing:
		return "missing", nil
	case round.FallbackNotText:
		return "not_text", nil
	default:
		return "", fmt.Errorf("unknown since-review fallback %d", f)
	}
}

// putViewed sets or clears the viewed marks of several files at once (REQ-NAV-010), all or none.
func (s *Server) putViewed(w http.ResponseWriter, r *http.Request, g *generation) {
	var req struct {
		Files  []int `json:"files"`
		Viewed bool  `json:"viewed"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Files) == 0 || len(req.Files) > g.snap.FileCount() {
		http.Error(w, fmt.Sprintf("give 1 to %d file indexes", g.snap.FileCount()), http.StatusBadRequest)
		return
	}
	if err := s.cfg.Session.SetViewed(req.Files, req.Viewed); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putHideWhitespace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hide bool `json:"hide"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.cfg.Session.SetHideWhitespace(req.Hide); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// postEarlierCopy copies earlier comment eid into the draft as comment id (REQ-CMT-011), at the
// most specific target that still applies. Repeating it returns the comment already copied.
func (s *Server) postEarlierCopy(w http.ResponseWriter, r *http.Request, g *generation) {
	var req struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	c, ok := g.prior.Find(r.PathValue("eid"))
	if !ok {
		http.Error(w, "unknown earlier comment", http.StatusNotFound)
		return
	}
	// Placing and validating lines read the file, so the copy shares the file-view bound.
	if err := s.acquire(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	defer s.release()
	places, err := g.prior.StillApplies(r.Context(), c)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	got, err := s.cfg.Session.CopyComment(r.Context(), req.ID, c.ID, c.Author, c.Body, places...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, toCommentJSON(got))
}
