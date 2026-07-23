package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
)

// walkNULTokensWithCPU reads only outside admission and performs hashing,
// framing, and token handling in bounded admitted quanta. Tokens are borrowed
// for the duration of visit and must not be retained.
func walkNULTokensWithCPU(
	ctx context.Context,
	input io.Reader,
	runCPU CPUAdmission,
	ledger *sourceLedger,
	digest hash.Hash,
	visit func([]byte) error,
) ([sha256.Size]byte, error) {
	if visit == nil {
		return [sha256.Size]byte{}, errors.New("NUL token visitor is nil")
	}
	carryBytes := int64(maxSourceProtocolToken + 1)
	if err := ledger.reserve(carryBytes); err != nil {
		return [sha256.Size]byte{}, err
	}
	defer ledger.release(carryBytes)
	walker := nulTokenWalker{
		ctx: ctx, carry: make([]byte, 0, maxSourceProtocolToken+1),
		digest: digest, visit: visit,
	}
	err := consumeSourceStream(
		ctx, input, runCPU, ledger, walker.consume, walker.finish,
	)
	return walker.sum, err
}

type nulTokenWalker struct {
	ctx    context.Context
	digest hash.Hash
	visit  func([]byte) error
	carry  []byte
	sum    [sha256.Size]byte
}

func (walker *nulTokenWalker) consume(chunk []byte) error {
	if walker.digest != nil {
		if _, err := walker.digest.Write(chunk); err != nil {
			return fmt.Errorf("hash NUL stream: %w", err)
		}
	}
	for len(chunk) != 0 {
		separator := bytes.IndexByte(chunk, 0)
		if separator < 0 {
			return walker.appendCarry(chunk)
		}
		part := chunk[:separator]
		if len(walker.carry) == 0 {
			if err := walker.visit(part); err != nil {
				return err
			}
		} else {
			if err := walker.appendCarry(part); err != nil {
				return err
			}
			if err := walker.visit(walker.carry); err != nil {
				return err
			}
			walker.carry = walker.carry[:0]
		}
		chunk = chunk[separator+1:]
	}
	return nil
}

func (walker *nulTokenWalker) appendCarry(value []byte) error {
	if len(value) > maxSourceProtocolToken-len(walker.carry) {
		return fmt.Errorf("NUL-delimited token exceeds %d bytes", maxSourceProtocolToken)
	}
	walker.carry = append(walker.carry, value...)
	return nil
}

func (walker *nulTokenWalker) finish() error {
	if len(walker.carry) != 0 {
		return io.ErrUnexpectedEOF
	}
	if walker.digest != nil {
		copy(walker.sum[:], walker.digest.Sum(nil))
	}
	return context.Cause(walker.ctx)
}
