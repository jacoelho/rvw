package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

type jsonFrameKind uint8

const (
	jsonObjectStart jsonFrameKind = iota + 1
	jsonObjectEnd
	jsonArrayStart
	jsonArrayEnd
	jsonObjectKey
	jsonString
	jsonNumber
	jsonTrue
	jsonFalse
	jsonNull
)

// jsonFrame is borrowed for the duration of one visitor call. Raw is present
// only for keys and scalar values. Count is the preflight-proven cardinality
// for an array start.
type jsonFrame struct {
	Raw          []byte
	DecodedBytes int64
	Kind         jsonFrameKind
	Count        int
}

type jsonFrameVisitor func(jsonFrame) error

type jsonExpectation uint8

const (
	jsonObjectKeyOrEnd jsonExpectation = iota + 1
	jsonObjectKeyRequired
	jsonObjectColon
	jsonValueRequired
	jsonArrayValueOrEnd
	jsonArrayValueRequired
	jsonCommaOrEnd
)

type jsonContainer struct {
	cardinalitySlot int
	count           int
	expect          jsonExpectation
	kind            byte
}

const jsonFramerMetadataBytes int64 = 64 << 10

type jsonValuePosition uint8

const (
	jsonNestedValue jsonValuePosition = iota + 1
	jsonRootArrayItem
)

type jsonTokenKind uint8

const (
	jsonNoToken jsonTokenKind = iota
	jsonKeyToken
	jsonStringToken
	jsonNumberToken
	jsonTrueToken
	jsonFalseToken
	jsonNullToken
)

type jsonNumberState uint8

const (
	jsonNumberSign jsonNumberState = iota + 1
	jsonNumberZero
	jsonNumberInteger
	jsonNumberDot
	jsonNumberFraction
	jsonNumberExponent
	jsonNumberExponentSign
	jsonNumberExponentDigits
)

type jsonCardinalityBuilder struct {
	ledger *sourceLedger
	values []uint32
}

func (builder *jsonCardinalityBuilder) add() (int, error) {
	if len(builder.values) == cap(builder.values) {
		capacity := max(16, cap(builder.values)*2)
		bytes := int64(capacity) * 4
		if err := builder.ledger.reserve(bytes); err != nil {
			return 0, err
		}
		grown := make([]uint32, len(builder.values), capacity)
		copy(grown, builder.values)
		builder.ledger.release(int64(cap(builder.values)) * 4)
		builder.values = grown
	}
	builder.values = append(builder.values, 0)
	return len(builder.values) - 1, nil
}

func (builder *jsonCardinalityBuilder) close() {
	if builder == nil || builder.ledger == nil {
		return
	}
	builder.ledger.release(int64(cap(builder.values)) * 4)
	builder.values = nil
	builder.ledger = nil
}

type jsonFramer struct {
	ctx             context.Context
	visitor         jsonFrameVisitor
	ledger          *sourceLedger
	cardinalities   *jsonCardinalityBuilder
	literal         string
	knownCounts     []uint32
	token           []byte
	offset          int64
	stringBytes     int64
	decodedBytes    int64
	maximumToken    int64
	tokenBytes      int64
	tokenLimit      int64
	depth           int
	knownCountIndex int
	literalIndex    int
	unicodeDigits   int
	unicodeValue    uint16
	pendingHigh     uint16
	number          jsonNumberState
	tokenKind       jsonTokenKind
	utf8            jsonUTF8State
	rootStarted     bool
	rootComplete    bool
	escaped         bool
	tokenPosition   jsonValuePosition
	stack           [maxGitHubJSONDepth]jsonContainer
	limits          jsonFramingLimits
}

func newJSONCounter(
	ctx context.Context,
	ledger *sourceLedger,
	maximumToken int64,
	limits jsonFramingLimits,
) (*jsonFramer, error) {
	if ctx == nil || ledger == nil || maximumToken <= 0 || maximumToken > maxGitHubJSONTokenBytes ||
		!limits.valid() {
		return nil, errors.New("JSON counter limits are invalid")
	}
	if err := ledger.reserve(jsonFramerMetadataBytes); err != nil {
		return nil, err
	}
	cardinalities := &jsonCardinalityBuilder{ledger: ledger}
	return &jsonFramer{
		ctx: ctx, ledger: ledger, cardinalities: cardinalities, tokenLimit: maximumToken,
		limits: limits,
	}, nil
}

func newJSONFramer(
	ctx context.Context,
	ledger *sourceLedger,
	metrics jsonCommandMetrics,
	visitor jsonFrameVisitor,
) (*jsonFramer, int64, error) {
	if ctx == nil || ledger == nil || visitor == nil || metrics.maximumTokenBytes <= 0 ||
		metrics.maximumTokenBytes > maxGitHubJSONTokenBytes || !metrics.limits.valid() {
		return nil, 0, errors.New("JSON framing inputs are invalid")
	}
	// The raw token and the standard unquoter's conservative temporary scratch
	// coexist. A receiving decode scope separately owns a completed string.
	tokenBytes := jsonFramerMetadataBytes + metrics.maximumTokenBytes + metrics.maximumTokenBytes + 8
	if err := ledger.reserve(tokenBytes); err != nil {
		return nil, 0, err
	}
	framer := &jsonFramer{
		ctx:         ctx,
		visitor:     visitor,
		ledger:      ledger,
		knownCounts: metrics.cardinalities,
		token:       make([]byte, 0, metrics.maximumTokenBytes),
		tokenLimit:  metrics.maximumTokenBytes,
		limits:      metrics.limits,
	}
	return framer, tokenBytes, nil
}

func (framer *jsonFramer) consume(value []byte) error {
	for index, current := range value {
		if index%sourceCPUPollBytes == 0 {
			if err := context.Cause(framer.ctx); err != nil {
				return err
			}
		}
		framer.offset++
		if !framer.utf8.consume(current) {
			return errors.New("JSON is not valid UTF-8")
		}
		if err := framer.consumeByte(current); err != nil {
			return err
		}
	}
	return nil
}

func (framer *jsonFramer) consumeByte(value byte) error {
	if framer.tokenKind != jsonNoToken {
		consumed, err := framer.consumeToken(value)
		if err != nil || consumed {
			return err
		}
	}
	if isJSONWhitespace(value) {
		return nil
	}
	if framer.rootComplete {
		return errors.New("JSON has trailing data")
	}
	if framer.depth == 0 {
		if framer.rootStarted {
			return errors.New("JSON has multiple root values")
		}
		return framer.startValue(value)
	}
	container := &framer.stack[framer.depth-1]
	switch container.expect {
	case jsonObjectKeyOrEnd:
		if value == '}' {
			return framer.closeContainer(value)
		}
		if value != '"' {
			return errors.New("JSON object key is not a string")
		}
		return framer.startString(jsonKeyToken)
	case jsonObjectKeyRequired:
		if value != '"' {
			return errors.New("JSON object is missing a key after comma")
		}
		return framer.startString(jsonKeyToken)
	case jsonObjectColon:
		if value != ':' {
			return errors.New("JSON object key is missing a colon")
		}
		container.expect = jsonValueRequired
		return nil
	case jsonValueRequired:
		return framer.startValue(value)
	case jsonArrayValueOrEnd:
		if value == ']' {
			return framer.closeContainer(value)
		}
		return framer.startValue(value)
	case jsonArrayValueRequired:
		if value == ']' {
			return errors.New("JSON array has a trailing comma")
		}
		return framer.startValue(value)
	case jsonCommaOrEnd:
		switch {
		case value == ',' && container.kind == '{':
			container.expect = jsonObjectKeyRequired
			return nil
		case value == ',' && container.kind == '[':
			container.expect = jsonArrayValueRequired
			return nil
		case value == '}' && container.kind == '{', value == ']' && container.kind == '[':
			return framer.closeContainer(value)
		default:
			return errors.New("JSON value is missing a comma or closing delimiter")
		}
	default:
		return errors.New("JSON parser state is invalid")
	}
}

func (framer *jsonFramer) startValue(value byte) error {
	position, err := framer.markValueStart()
	if err != nil {
		return err
	}
	switch value {
	case '{':
		return framer.openContainer(value, jsonObjectKeyOrEnd)
	case '[':
		return framer.openContainer(value, jsonArrayValueOrEnd)
	case '"':
		framer.tokenPosition = position
		return framer.startString(jsonStringToken)
	case 't':
		return framer.startLiteral(jsonTrueToken, "true", position)
	case 'f':
		return framer.startLiteral(jsonFalseToken, "false", position)
	case 'n':
		return framer.startLiteral(jsonNullToken, "null", position)
	case '-':
		return framer.startNumber(value, jsonNumberSign, position)
	case '0':
		return framer.startNumber(value, jsonNumberZero, position)
	default:
		if value >= '1' && value <= '9' {
			return framer.startNumber(value, jsonNumberInteger, position)
		}
		return fmt.Errorf("JSON value starts with invalid byte %q", value)
	}
}

func (framer *jsonFramer) markValueStart() (jsonValuePosition, error) {
	if framer.depth == 0 {
		framer.rootStarted = true
		return jsonNestedValue, nil
	}
	parent := &framer.stack[framer.depth-1]
	position := jsonNestedValue
	if framer.depth == 1 && parent.kind == '[' {
		position = jsonRootArrayItem
	}
	if parent.kind == '[' {
		parent.count++
		maximum := framer.limits.arrayElements(framer.depth)
		if parent.count > maximum {
			return 0, fmt.Errorf(
				"JSON collection exceeds %d elements",
				maximum,
			)
		}
	}
	parent.expect = jsonCommaOrEnd
	return position, nil
}

func (framer *jsonFramer) openContainer(
	kind byte,
	expect jsonExpectation,
) error {
	if framer.depth == len(framer.stack) {
		return fmt.Errorf("JSON nesting exceeds %d", len(framer.stack))
	}
	container := jsonContainer{
		kind: kind, expect: expect, cardinalitySlot: -1,
	}
	if kind == '[' {
		if framer.cardinalities != nil {
			slot, err := framer.cardinalities.add()
			if err != nil {
				return err
			}
			container.cardinalitySlot = slot
		} else {
			if framer.knownCountIndex >= len(framer.knownCounts) {
				return errors.New("JSON cardinality preflight is incomplete")
			}
			container.cardinalitySlot = framer.knownCountIndex
			framer.knownCountIndex++
		}
	}
	framer.stack[framer.depth] = container
	framer.depth++
	frame := jsonFrame{Kind: jsonObjectStart}
	if kind == '[' {
		frame.Kind = jsonArrayStart
		if framer.cardinalities == nil {
			frame.Count = int(framer.knownCounts[container.cardinalitySlot])
		}
	}
	return framer.emit(frame)
}

func (framer *jsonFramer) closeContainer(value byte) error {
	if framer.depth == 0 {
		return errors.New("JSON delimiters are unbalanced")
	}
	container := framer.stack[framer.depth-1]
	if value == '}' && container.kind != '{' || value == ']' && container.kind != '[' {
		return errors.New("JSON delimiters are unbalanced")
	}
	if container.kind == '[' && framer.cardinalities != nil {
		if container.count < 0 || container.count > framer.limits.arrayElements(framer.depth) {
			return errors.New("JSON array cardinality is outside its validated bound")
		}
		//nolint:gosec // limits.valid proves every accepted count fits in uint32.
		framer.cardinalities.values[container.cardinalitySlot] = uint32(container.count)
	}
	framer.depth--
	frame := jsonFrame{Kind: jsonObjectEnd}
	if container.kind == '[' {
		frame.Kind = jsonArrayEnd
	}
	if err := framer.emit(frame); err != nil {
		return err
	}
	if framer.depth == 0 {
		framer.rootComplete = true
	}
	return nil
}

func (framer *jsonFramer) startString(kind jsonTokenKind) error {
	framer.tokenKind = kind
	framer.tokenBytes = 0
	framer.stringBytes = 0
	framer.decodedBytes = 0
	framer.unicodeDigits = 0
	framer.unicodeValue = 0
	framer.pendingHigh = 0
	framer.escaped = false
	framer.token = framer.token[:0]
	return framer.appendToken('"')
}

func (framer *jsonFramer) startLiteral(
	kind jsonTokenKind,
	literal string,
	position jsonValuePosition,
) error {
	framer.tokenKind = kind
	framer.tokenBytes = 0
	framer.literal = literal
	framer.literalIndex = 1
	framer.tokenPosition = position
	framer.token = framer.token[:0]
	return framer.appendToken(literal[0])
}

func (framer *jsonFramer) startNumber(
	value byte,
	state jsonNumberState,
	position jsonValuePosition,
) error {
	framer.tokenKind = jsonNumberToken
	framer.tokenBytes = 0
	framer.number = state
	framer.tokenPosition = position
	framer.token = framer.token[:0]
	return framer.appendToken(value)
}

func (framer *jsonFramer) consumeToken(value byte) (bool, error) {
	if framer.tokenKind == jsonKeyToken || framer.tokenKind == jsonStringToken {
		return true, framer.consumeString(value)
	}
	if framer.tokenKind == jsonTrueToken || framer.tokenKind == jsonFalseToken ||
		framer.tokenKind == jsonNullToken {
		if framer.literalIndex < len(framer.literal) {
			if value != framer.literal[framer.literalIndex] {
				return true, errors.New("JSON literal is invalid")
			}
			framer.literalIndex++
			return true, framer.appendToken(value)
		}
		if !isJSONValueDelimiter(value) {
			return true, errors.New("JSON literal has an invalid suffix")
		}
		return false, framer.finishScalar()
	}
	if framer.tokenKind != jsonNumberToken {
		return true, errors.New("JSON token state is invalid")
	}
	if isJSONValueDelimiter(value) {
		if !framer.validNumberEnd() {
			return true, errors.New("JSON number is incomplete")
		}
		return false, framer.finishScalar()
	}
	if err := framer.consumeNumber(value); err != nil {
		return true, err
	}
	return true, framer.appendToken(value)
}

func (framer *jsonFramer) consumeString(value byte) error {
	framer.stringBytes++
	if framer.stringBytes > framer.tokenLimit {
		return fmt.Errorf("JSON string token exceeds %d bytes", framer.tokenLimit)
	}
	if err := framer.appendToken(value); err != nil {
		return err
	}
	if framer.unicodeDigits > 0 {
		if !isHexadecimal(value) {
			return errors.New("JSON string contains an invalid Unicode escape")
		}
		framer.unicodeValue = framer.unicodeValue<<4 | uint16(hexadecimalValue(value))
		framer.unicodeDigits--
		if framer.unicodeDigits == 0 {
			framer.finishUnicodeEscape()
		}
		return nil
	}
	if framer.escaped {
		framer.escaped = false
		if value == 'u' {
			framer.unicodeDigits = 4
			framer.unicodeValue = 0
			return nil
		}
		framer.flushPendingHigh()
		if value != '"' && value != '\\' && value != '/' &&
			value != 'b' && value != 'f' && value != 'n' && value != 'r' && value != 't' {
			return errors.New("JSON string contains an invalid escape")
		}
		framer.decodedBytes++
		return nil
	}
	if value == '\\' {
		framer.escaped = true
		return nil
	}
	framer.flushPendingHigh()
	if value == '"' {
		framer.maximumToken = max(framer.maximumToken, int64(len(framer.token)))
		kind := framer.tokenKind
		framer.tokenKind = jsonNoToken
		if kind == jsonKeyToken {
			if framer.depth == 0 || framer.stack[framer.depth-1].kind != '{' {
				return errors.New("JSON object key is outside an object")
			}
			framer.stack[framer.depth-1].expect = jsonObjectColon
			return framer.emit(jsonFrame{
				Kind: jsonObjectKey, Raw: framer.token, DecodedBytes: framer.decodedBytes,
			})
		}
		if err := framer.emit(jsonFrame{
			Kind: jsonString, Raw: framer.token, DecodedBytes: framer.decodedBytes,
		}); err != nil {
			return err
		}
		framer.completeScalarValue()
		return nil
	}
	if value < 0x20 {
		return errors.New("JSON string contains an unescaped control byte")
	}
	framer.decodedBytes++
	return nil
}

func (framer *jsonFramer) finishUnicodeEscape() {
	value := framer.unicodeValue
	switch {
	case value >= 0xd800 && value <= 0xdbff:
		framer.flushPendingHigh()
		framer.pendingHigh = value
	case value >= 0xdc00 && value <= 0xdfff && framer.pendingHigh != 0:
		framer.decodedBytes += 4
		framer.pendingHigh = 0
	case value >= 0xdc00 && value <= 0xdfff:
		framer.decodedBytes += 3
	default:
		framer.flushPendingHigh()
		switch {
		case value <= 0x7f:
			framer.decodedBytes++
		case value <= 0x7ff:
			framer.decodedBytes += 2
		default:
			framer.decodedBytes += 3
		}
	}
}

func (framer *jsonFramer) flushPendingHigh() {
	if framer.pendingHigh != 0 {
		framer.decodedBytes += 3
		framer.pendingHigh = 0
	}
}

func (framer *jsonFramer) consumeNumber(value byte) error {
	digit := value >= '0' && value <= '9'
	switch framer.number {
	case jsonNumberSign:
		switch {
		case value == '0':
			framer.number = jsonNumberZero
		case value >= '1' && value <= '9':
			framer.number = jsonNumberInteger
		default:
			return errors.New("JSON number sign is not followed by a digit")
		}
	case jsonNumberZero:
		switch value {
		case '.':
			framer.number = jsonNumberDot
		case 'e', 'E':
			framer.number = jsonNumberExponent
		default:
			return errors.New("JSON number has a leading zero or invalid suffix")
		}
	case jsonNumberInteger:
		switch {
		case digit:
		case value == '.':
			framer.number = jsonNumberDot
		case value == 'e' || value == 'E':
			framer.number = jsonNumberExponent
		default:
			return errors.New("JSON number has an invalid suffix")
		}
	case jsonNumberDot:
		if !digit {
			return errors.New("JSON number fraction has no digit")
		}
		framer.number = jsonNumberFraction
	case jsonNumberFraction:
		switch {
		case digit:
		case value == 'e' || value == 'E':
			framer.number = jsonNumberExponent
		default:
			return errors.New("JSON number fraction has an invalid suffix")
		}
	case jsonNumberExponent:
		switch {
		case value == '+' || value == '-':
			framer.number = jsonNumberExponentSign
		case digit:
			framer.number = jsonNumberExponentDigits
		default:
			return errors.New("JSON number exponent has no digit")
		}
	case jsonNumberExponentSign:
		if !digit {
			return errors.New("JSON number exponent sign has no digit")
		}
		framer.number = jsonNumberExponentDigits
	case jsonNumberExponentDigits:
		if !digit {
			return errors.New("JSON number exponent has an invalid suffix")
		}
	default:
		return errors.New("JSON number state is invalid")
	}
	return nil
}

func (framer *jsonFramer) validNumberEnd() bool {
	return framer.number == jsonNumberZero || framer.number == jsonNumberInteger ||
		framer.number == jsonNumberFraction || framer.number == jsonNumberExponentDigits
}

func (framer *jsonFramer) finishScalar() error {
	kind := framer.tokenKind
	framer.tokenKind = jsonNoToken
	frame := jsonFrame{Raw: framer.token}
	switch kind {
	case jsonNumberToken:
		frame.Kind = jsonNumber
	case jsonTrueToken:
		frame.Kind = jsonTrue
	case jsonFalseToken:
		frame.Kind = jsonFalse
	case jsonNullToken:
		frame.Kind = jsonNull
	default:
		return errors.New("JSON scalar state is invalid")
	}
	framer.maximumToken = max(framer.maximumToken, int64(len(framer.token)))
	if err := framer.emit(frame); err != nil {
		return err
	}
	framer.completeScalarValue()
	return nil
}

func (framer *jsonFramer) completeScalarValue() {
	framer.tokenPosition = jsonNestedValue
	if framer.depth == 0 {
		framer.rootComplete = true
	}
}

func (framer *jsonFramer) appendToken(value byte) error {
	framer.tokenBytes++
	length := framer.tokenBytes
	if length > framer.tokenLimit+2 {
		return fmt.Errorf("JSON scalar token exceeds %d bytes", framer.tokenLimit)
	}
	if framer.visitor == nil {
		framer.maximumToken = max(framer.maximumToken, length)
		return nil
	}
	if len(framer.token) == cap(framer.token) {
		return errors.New("JSON scalar exceeded its preflight capacity")
	}
	framer.token = append(framer.token, value)
	return nil
}

func (framer *jsonFramer) emit(frame jsonFrame) error {
	if framer.visitor == nil {
		return nil
	}
	return framer.visitor(frame)
}

func (framer *jsonFramer) finish() error {
	if err := context.Cause(framer.ctx); err != nil {
		return err
	}
	if !framer.utf8.complete() {
		return errors.New("JSON is not valid UTF-8")
	}
	if framer.tokenKind != jsonNoToken {
		if framer.tokenKind == jsonNumberToken && framer.validNumberEnd() ||
			(framer.tokenKind == jsonTrueToken || framer.tokenKind == jsonFalseToken ||
				framer.tokenKind == jsonNullToken) && framer.literalIndex == len(framer.literal) {
			if err := framer.finishScalar(); err != nil {
				return err
			}
		} else {
			return errors.New("JSON scalar token is incomplete")
		}
	}
	if !framer.rootStarted || !framer.rootComplete || framer.depth != 0 {
		return errors.New("JSON structure is incomplete")
	}
	if framer.cardinalities == nil && framer.knownCountIndex != len(framer.knownCounts) {
		return errors.New("JSON cardinality preflight has trailing entries")
	}
	return nil
}

func (framer *jsonFramer) metrics() (jsonCommandMetrics, error) {
	if framer.cardinalities == nil {
		return jsonCommandMetrics{}, errors.New("JSON framer is not a counter")
	}
	if framer.maximumToken < 0 || framer.maximumToken > math.MaxInt {
		return jsonCommandMetrics{}, errors.New("JSON scalar token metrics are invalid")
	}
	return jsonCommandMetrics{
		maximumTokenBytes: max(int64(1), framer.maximumToken),
		cardinalities:     framer.cardinalities.values,
		cardinalityBytes:  int64(cap(framer.cardinalities.values)) * 4,
		limits:            framer.limits,
	}, nil
}

func walkJSONFrames(
	ctx context.Context,
	input io.Reader,
	runCPU CPUAdmission,
	ledger *sourceLedger,
	metrics jsonCommandMetrics,
	visitor jsonFrameVisitor,
) error {
	framer, scratchBytes, err := newJSONFramer(ctx, ledger, metrics, visitor)
	if err != nil {
		return err
	}
	defer ledger.release(scratchBytes)
	return consumeSourceStream(ctx, input, runCPU, ledger, framer.consume, framer.finish)
}

func decodeJSONString(raw []byte) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func isJSONWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func isJSONValueDelimiter(value byte) bool {
	return isJSONWhitespace(value) || value == ',' || value == ']' || value == '}'
}

func hexadecimalValue(value byte) byte {
	switch {
	case value >= '0' && value <= '9':
		return value - '0'
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10
	default:
		return value - 'A' + 10
	}
}
