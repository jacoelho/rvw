package source

import (
	"context"
	"errors"
	"io"
	"math"
)

const (
	maxGitHubJSONDepth              = 256
	maxGitHubJSONTokenBytes         = 6*64<<10 + 2
	maxGitHubPageCollectionElements = 100
)

type githubErrors []struct{}

type jsonCommandMetrics struct {
	cardinalities     []uint32
	cardinalityBytes  int64
	maximumTokenBytes int64
	limits            jsonFramingLimits
}

// jsonFramingLimits keeps pagination breadth independent from nested page
// cardinality without teaching the generic framer endpoint schemas.
type jsonFramingLimits struct {
	rootArrayElements   int
	nestedArrayElements int
}

func newJSONFramingLimits(rootArrayElements, nestedArrayElements int) (jsonFramingLimits, error) {
	if rootArrayElements <= 0 || nestedArrayElements <= 0 ||
		uint64(rootArrayElements) > math.MaxUint32 || uint64(nestedArrayElements) > math.MaxUint32 {
		return jsonFramingLimits{}, errors.New("JSON framing limits are invalid")
	}
	return jsonFramingLimits{
		rootArrayElements: rootArrayElements, nestedArrayElements: nestedArrayElements,
	}, nil
}

func defaultJSONFramingLimits() jsonFramingLimits {
	return jsonFramingLimits{
		rootArrayElements:   maxGitHubPageCollectionElements,
		nestedArrayElements: maxGitHubPageCollectionElements,
	}
}

func (limits jsonFramingLimits) valid() bool {
	return limits.rootArrayElements > 0 && limits.nestedArrayElements > 0 &&
		uint64(limits.rootArrayElements) <= math.MaxUint32 &&
		uint64(limits.nestedArrayElements) <= math.MaxUint32
}

func (limits jsonFramingLimits) arrayElements(depth int) int {
	if depth == 1 {
		return limits.rootArrayElements
	}
	return limits.nestedArrayElements
}

func (metrics *jsonCommandMetrics) release(ledger *sourceLedger) {
	if metrics == nil || metrics.cardinalityBytes == 0 {
		return
	}
	ledger.release(metrics.cardinalityBytes)
	metrics.cardinalityBytes = 0
	metrics.cardinalities = nil
}

func validateCommandJSONWithLimits(
	ctx context.Context,
	spool *githubSpool,
	runCPU CPUAdmission,
	ledger *sourceLedger,
	limits jsonFramingLimits,
) (jsonCommandMetrics, error) {
	if err := spool.Rewind(); err != nil {
		return jsonCommandMetrics{}, err
	}
	metrics, err := inspectJSONWithLimits(
		ctx,
		spool.Reader(),
		maxGitHubJSONTokenBytes,
		runCPU,
		ledger,
		limits,
	)
	if err != nil {
		return jsonCommandMetrics{}, err
	}
	if err := spool.Rewind(); err != nil {
		metrics.release(ledger)
		return jsonCommandMetrics{}, err
	}
	return metrics, nil
}

func validateJSONLexical(input io.Reader, maxStringBytes int64) error {
	return validateJSONLexicalContext(context.Background(), input, maxStringBytes)
}

func validateJSONLexicalContext(
	ctx context.Context,
	input io.Reader,
	maxStringBytes int64,
) error {
	_, err := inspectJSONLexical(ctx, input, maxStringBytes)
	return err
}

func inspectJSONLexical(
	ctx context.Context,
	input io.Reader,
	maxStringBytes int64,
) (jsonCommandMetrics, error) {
	ledger, err := newSourceLedger(0)
	if err != nil {
		return jsonCommandMetrics{}, err
	}
	return inspectJSONWithCPU(
		ctx,
		input,
		maxStringBytes,
		directCPUAdmission,
		ledger,
	)
}

func inspectJSONWithCPU(
	ctx context.Context,
	input io.Reader,
	maximumTokenBytes int64,
	runCPU CPUAdmission,
	ledger *sourceLedger,
) (jsonCommandMetrics, error) {
	return inspectJSONWithLimits(
		ctx, input, maximumTokenBytes, runCPU, ledger, defaultJSONFramingLimits(),
	)
}

func inspectJSONWithLimits(
	ctx context.Context,
	input io.Reader,
	maximumTokenBytes int64,
	runCPU CPUAdmission,
	ledger *sourceLedger,
	limits jsonFramingLimits,
) (jsonCommandMetrics, error) {
	counter, err := newJSONCounter(ctx, ledger, maximumTokenBytes, limits)
	if err != nil {
		return jsonCommandMetrics{}, err
	}
	defer ledger.release(jsonFramerMetadataBytes)
	err = consumeSourceStream(
		ctx,
		input,
		runCPU,
		ledger,
		counter.consume,
		counter.finish,
	)
	if err != nil {
		counter.cardinalities.close()
		return jsonCommandMetrics{}, err
	}
	metrics, err := counter.metrics()
	if err != nil {
		counter.cardinalities.close()
		return jsonCommandMetrics{}, err
	}
	counter.cardinalities.ledger = nil
	return metrics, nil
}

type jsonUTF8State struct {
	remaining uint8
	minimum   byte
	maximum   byte
}

func (state *jsonUTF8State) consume(value byte) bool {
	if state.remaining != 0 {
		if value < state.minimum || value > state.maximum {
			return false
		}
		state.remaining--
		state.minimum, state.maximum = 0x80, 0xbf
		return true
	}
	switch {
	case value < 0x80:
		return true
	case value >= 0xc2 && value <= 0xdf:
		state.remaining, state.minimum, state.maximum = 1, 0x80, 0xbf
	case value == 0xe0:
		state.remaining, state.minimum, state.maximum = 2, 0xa0, 0xbf
	case value >= 0xe1 && value <= 0xec || value >= 0xee && value <= 0xef:
		state.remaining, state.minimum, state.maximum = 2, 0x80, 0xbf
	case value == 0xed:
		state.remaining, state.minimum, state.maximum = 2, 0x80, 0x9f
	case value == 0xf0:
		state.remaining, state.minimum, state.maximum = 3, 0x90, 0xbf
	case value >= 0xf1 && value <= 0xf3:
		state.remaining, state.minimum, state.maximum = 3, 0x80, 0xbf
	case value == 0xf4:
		state.remaining, state.minimum, state.maximum = 3, 0x80, 0x8f
	default:
		return false
	}
	return true
}

func (state *jsonUTF8State) complete() bool { return state.remaining == 0 }

func isHexadecimal(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' ||
		value >= 'A' && value <= 'F'
}
