package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jacoelho/rvw/internal/review"
)

func enforceRenameCandidateLimitStreamContextCPU(
	ctx context.Context,
	input io.Reader,
	runCPU CPUAdmission,
) error {
	ledger, err := newSourceLedger(0)
	if err != nil {
		return err
	}
	parser := renameCandidateStreamParser{}
	_, err = walkNULTokensWithCPU(ctx, input, runCPU, ledger, nil, parser.consume)
	if err != nil {
		return err
	}
	if parser.pathsRemaining != 0 {
		return io.ErrUnexpectedEOF
	}
	return nil
}

type renameCandidateStreamParser struct {
	candidates     uint32
	files          int
	pathsRemaining int
}

func (parser *renameCandidateStreamParser) consume(token []byte) error {
	if parser.pathsRemaining != 0 {
		if err := validateRawPath(token); err != nil {
			return err
		}
		parser.pathsRemaining--
		return nil
	}
	if parser.files == review.MaxFiles {
		return fmt.Errorf("changed files exceed %d", review.MaxFiles)
	}
	parsed, err := parseRawDiffHeader(token)
	if err != nil {
		return err
	}
	parser.files++
	parser.pathsRemaining = 1
	if parsed.status == 'R' || parsed.status == 'C' {
		parser.pathsRemaining = 2
	}
	if parsed.status == 'A' || parsed.status == 'D' {
		parser.candidates++
		if parser.candidates > 1_000 {
			return &LimitError{Resource: LimitRenameCandidates, Limit: 1_000}
		}
	}
	return nil
}

func parseRawDiffStreamWithLedgerCPU(
	ctx context.Context,
	input io.Reader,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) ([]diffRecord, error) {
	return parseRawDiffStreamWithLedgerLimitsCPU(
		ctx, input, ledger, review.MaxFiles, review.MaxSourcePaths, runCPU,
	)
}

func parseRawDiffStreamWithLedgerLimitsCPU(
	ctx context.Context,
	input io.Reader,
	ledger *sourceLedger,
	recordLimit int,
	pathLimit int64,
	runCPU CPUAdmission,
) ([]diffRecord, error) {
	if recordLimit < 0 || recordLimit > review.MaxFiles ||
		pathLimit < 0 || pathLimit > review.MaxSourcePaths {
		return nil, errors.New("source diff limits are invalid")
	}
	parser := rawDiffCPUParser{builder: diffRecordBuilder{
		ledger: ledger, recordLimit: recordLimit, pathLimit: pathLimit,
	}}
	_, err := walkNULTokensWithCPU(ctx, input, runCPU, ledger, nil, parser.consume)
	if err != nil {
		return nil, err
	}
	if parser.pathsRemaining != 0 {
		return nil, io.ErrUnexpectedEOF
	}
	return parser.builder.records, nil
}

type rawDiffCPUParser struct {
	record         diffRecord
	builder        diffRecordBuilder
	pathsRemaining int
}

func (parser *rawDiffCPUParser) consume(token []byte) error {
	if parser.pathsRemaining == 0 {
		return parser.header(token)
	}
	if err := validateRawPath(token); err != nil {
		return err
	}
	if err := parser.builder.reservePath(len(token)); err != nil {
		return err
	}
	path := string(token)
	switch parser.record.status {
	case 'A':
		parser.record.newPath = path
	case 'D':
		parser.record.oldPath = path
	case 'R', 'C':
		if parser.pathsRemaining == 2 {
			parser.record.oldPath = path
		} else {
			parser.record.newPath = path
		}
	default:
		parser.record.oldPath = path
		parser.record.newPath = path
	}
	parser.pathsRemaining--
	if parser.pathsRemaining == 0 {
		parser.builder.add(parser.record)
	}
	return nil
}

func (parser *rawDiffCPUParser) header(token []byte) error {
	if len(parser.builder.records) == parser.builder.recordLimit {
		return fmt.Errorf("changed files exceed %d", parser.builder.recordLimit)
	}
	if err := parser.builder.ensureCapacity(); err != nil {
		return err
	}
	parsed, err := parseRawDiffHeader(token)
	if err != nil {
		return err
	}
	var oldOID, newOID *review.ObjectID
	if parsed.hasOldOID {
		oldOID = new(parsed.oldOID)
	}
	if parsed.hasNewOID {
		newOID = new(parsed.newOID)
	}
	parser.record = diffRecord{
		oldMode: parsed.oldMode, newMode: parsed.newMode,
		oldOID: oldOID, newOID: newOID, status: parsed.status,
	}
	parser.pathsRemaining = 1
	if parsed.status == 'R' || parsed.status == 'C' {
		parser.pathsRemaining = 2
	}
	return nil
}

func mergeNumstatStreamContextCPU(
	ctx context.Context,
	input io.Reader,
	records []diffRecord,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) error {
	parser := numstatCPUParser{records: records}
	_, err := walkNULTokensWithCPU(ctx, input, runCPU, ledger, nil, parser.consume)
	if err != nil {
		return err
	}
	if parser.pathPhase != 0 {
		return io.ErrUnexpectedEOF
	}
	if parser.index != len(records) {
		return fmt.Errorf(
			"git metadata streams disagree: raw=%d numstat=%d",
			len(records), parser.index,
		)
	}
	return nil
}

type numstatCPUParser struct {
	records   []diffRecord
	index     int
	pathPhase int
	additions uint32
	deletions uint32
	binary    bool
}

func (parser *numstatCPUParser) consume(token []byte) error {
	if parser.pathPhase != 0 {
		return parser.path(token)
	}
	if parser.index >= len(parser.records) {
		return fmt.Errorf(
			"git metadata streams disagree: raw=%d numstat>%d",
			len(parser.records), parser.index,
		)
	}
	firstTab := bytes.IndexByte(token, '\t')
	secondTab := -1
	if firstTab >= 0 {
		if offset := bytes.IndexByte(token[firstTab+1:], '\t'); offset >= 0 {
			secondTab = firstTab + 1 + offset
		}
	}
	if firstTab <= 0 || secondTab <= firstTab+1 {
		return fmt.Errorf("invalid Git numstat record %q", boundedText(string(token), 256))
	}
	additions, binaryA, err := parseCount(token[:firstTab])
	if err != nil {
		return err
	}
	deletions, binaryD, err := parseCount(token[firstTab+1 : secondTab])
	if err != nil {
		return err
	}
	if binaryA != binaryD {
		return errors.New("git numstat has inconsistent binary counts")
	}
	parser.additions, parser.deletions, parser.binary = additions, deletions, binaryA
	path := token[secondTab+1:]
	if len(path) == 0 {
		parser.pathPhase = 1
		return nil
	}
	if err := validateRawPath(path); err != nil {
		return err
	}
	return parser.apply(path, path)
}

func (parser *numstatCPUParser) path(path []byte) error {
	if err := validateRawPath(path); err != nil {
		return err
	}
	record := &parser.records[parser.index]
	if parser.pathPhase == 1 {
		matches := record.status == 'A' || record.oldPath == string(path)
		if !matches {
			return fmt.Errorf("git metadata streams disagree at file %d", parser.index)
		}
		parser.pathPhase = 2
		return nil
	}
	oldPath := []byte(record.oldPath)
	if record.status == 'A' {
		oldPath = nil
	}
	return parser.apply(oldPath, path)
}

func (parser *numstatCPUParser) apply(oldPath, newPath []byte) error {
	record := &parser.records[parser.index]
	var matches bool
	switch record.status {
	case 'A':
		matches = record.newPath == string(newPath)
	case 'D':
		matches = record.oldPath == string(oldPath)
	default:
		matches = record.oldPath == string(oldPath) && record.newPath == string(newPath)
	}
	if !matches {
		return fmt.Errorf("git metadata streams disagree at file %d", parser.index)
	}
	record.additions = parser.additions
	record.deletions = parser.deletions
	record.binary = parser.binary
	parser.pathPhase = 0
	parser.index++
	return nil
}
