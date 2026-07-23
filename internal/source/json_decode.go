package source

import (
	"errors"
	"fmt"
	"math"
)

const (
	jsonDecodeScopeMetadataBytes int64 = 64
	jsonDecodedPointerBytes      int64 = 256
	jsonDecodedElementBytes      int64 = 256
	jsonDecodedCollectorBytes    int64 = 32 << 10
)

// jsonDecodeScope owns only the allocations explicitly reserved through it.
// Domain reservations and enclosing checkpoint ownership are never inferred
// from the ledger's aggregate usage.
type jsonDecodeScope struct {
	ledger *sourceLedger
	owned  int64
	closed bool
}

// jsonDecodeLease is a move-only-by-convention ownership token. Endpoint DTOs
// never store leases; they transfer retained bytes to their enclosing ledger
// checkpoint before the decode scope is closed.
type jsonDecodeLease struct {
	scope *jsonDecodeScope
	bytes int64
}

func newJSONDecodeScope(ledger *sourceLedger) (*jsonDecodeScope, error) {
	if ledger == nil {
		return nil, errors.New("JSON decode scope requires a source ledger")
	}
	if err := ledger.reserve(jsonDecodeScopeMetadataBytes); err != nil {
		return nil, err
	}
	return &jsonDecodeScope{ledger: ledger, owned: jsonDecodeScopeMetadataBytes}, nil
}

func (scope *jsonDecodeScope) reserve(bytes int64) error {
	if scope == nil || scope.closed || bytes < 0 {
		return errors.New("JSON decode scope reservation is invalid")
	}
	if err := scope.ledger.reserve(bytes); err != nil {
		return err
	}
	scope.owned += bytes
	return nil
}

func (scope *jsonDecodeScope) acquire(bytes int64) (jsonDecodeLease, error) {
	if err := scope.reserve(bytes); err != nil {
		return jsonDecodeLease{}, err
	}
	return jsonDecodeLease{scope: scope, bytes: bytes}, nil
}

func (scope *jsonDecodeScope) release(bytes int64) {
	if scope == nil || scope.closed || bytes < 0 || bytes > scope.owned-jsonDecodeScopeMetadataBytes {
		panic("invalid JSON decode scope release")
	}
	scope.ledger.release(bytes)
	scope.owned -= bytes
}

// transfer moves fungible ledger ownership out of the decode scope without
// releasing it. The caller's enclosing checkpoint becomes responsible for the
// returned reservation.
func (scope *jsonDecodeScope) transfer(bytes int64) error {
	if scope == nil || scope.closed || bytes < 0 || bytes > scope.owned-jsonDecodeScopeMetadataBytes {
		return errors.New("JSON decode scope transfer is invalid")
	}
	scope.owned -= bytes
	return nil
}

func (scope *jsonDecodeScope) mark() int64 {
	if scope == nil || scope.closed {
		return -1
	}
	return scope.owned
}

func (scope *jsonDecodeScope) releaseTo(mark int64) error {
	if scope == nil || scope.closed || mark < jsonDecodeScopeMetadataBytes || mark > scope.owned {
		return errors.New("JSON decode scope mark is invalid")
	}
	scope.release(scope.owned - mark)
	return nil
}

func (scope *jsonDecodeScope) Close() {
	if scope == nil || scope.closed {
		return
	}
	scope.closed = true
	scope.ledger.release(scope.owned)
	scope.owned = 0
	scope.ledger = nil
}

func (lease *jsonDecodeLease) release() {
	if lease == nil || lease.scope == nil {
		return
	}
	lease.scope.release(lease.bytes)
	lease.scope = nil
	lease.bytes = 0
}

func (scope *jsonDecodeScope) decodedString(frame jsonFrame) (string, error) {
	if (frame.Kind != jsonString && frame.Kind != jsonObjectKey) || frame.DecodedBytes < 0 {
		return "", errors.New("JSON value is not a string")
	}
	if err := scope.reserve(frame.DecodedBytes); err != nil {
		return "", err
	}
	value, err := decodeJSONString(frame.Raw)
	if err != nil {
		scope.release(frame.DecodedBytes)
		return "", err
	}
	if int64(len(value)) != frame.DecodedBytes {
		scope.release(frame.DecodedBytes)
		return "", errors.New("JSON decoded string size differs from preflight")
	}
	return value, nil
}

func (scope *jsonDecodeScope) string(frame jsonFrame) (string, error) {
	if frame.Kind != jsonString {
		return "", errors.New("JSON field is not a string")
	}
	return scope.decodedString(frame)
}

func (scope *jsonDecodeScope) nullableString(frame jsonFrame) (*string, error) {
	if frame.Kind == jsonNull {
		//nolint:nilnil // A nil pointer is the valid representation of JSON null.
		return nil, nil
	}
	value, err := scope.string(frame)
	if err != nil {
		return nil, err
	}
	if err := scope.reserve(jsonDecodedPointerBytes); err != nil {
		return nil, err
	}
	return new(value), nil
}

func (scope *jsonDecodeScope) nullableBoolean(frame jsonFrame) (*bool, error) {
	if frame.Kind == jsonNull {
		//nolint:nilnil // A nil pointer is the valid representation of JSON null.
		return nil, nil
	}
	value, err := jsonBooleanValue(frame)
	if err != nil {
		return nil, err
	}
	if err := scope.reserve(jsonDecodedPointerBytes); err != nil {
		return nil, err
	}
	return new(value), nil
}

func (scope *jsonDecodeScope) nullableUint32(frame jsonFrame) (*uint32, error) {
	if frame.Kind == jsonNull {
		//nolint:nilnil // A nil pointer is the valid representation of JSON null.
		return nil, nil
	}
	value, err := jsonUint64Value(frame)
	if err != nil {
		return nil, err
	}
	if value > math.MaxUint32 {
		return nil, errors.New("JSON field is not a uint32")
	}
	if err := scope.reserve(jsonDecodedPointerBytes); err != nil {
		return nil, err
	}
	narrowed := uint32(value)
	return new(narrowed), nil
}

func (scope *jsonDecodeScope) nullableGitHubUint64(frame jsonFrame) (*githubUint64, error) {
	if frame.Kind == jsonNull {
		//nolint:nilnil // A nil pointer is the valid representation of JSON null.
		return nil, nil
	}
	value, err := jsonGitHubUint64(frame)
	if err != nil {
		return nil, err
	}
	if err := scope.reserve(jsonDecodedPointerBytes); err != nil {
		return nil, err
	}
	return new(value), nil
}

func jsonDecodedSliceBytes(count int) (int64, error) {
	if count < 0 || int64(count) > math.MaxInt64/jsonDecodedElementBytes {
		return 0, errors.New("JSON decoded slice size overflows")
	}
	return int64(count) * jsonDecodedElementBytes, nil
}

func reserveJSONDecodedSlice(scope *jsonDecodeScope, count int) error {
	bytes, err := jsonDecodedSliceBytes(count)
	if err != nil {
		return err
	}
	if err := scope.reserve(bytes); err != nil {
		return fmt.Errorf("reserve JSON decoded slice: %w", err)
	}
	return nil
}
