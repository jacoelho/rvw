package change

import (
	"fmt"
	"unicode/utf8"
)

// OwnedDraftBody is an opaque, exact-sized immutable draft-body allocation.
type OwnedDraftBody struct {
	value string
	valid bool
}

// MaterializeDraftBody validates runes and copies them once into exact string ownership.
func MaterializeDraftBody(value []rune) (OwnedDraftBody, error) {
	bytes := int64(0)
	for _, character := range value {
		size := utf8.RuneLen(character)
		if size < 0 {
			return OwnedDraftBody{}, ErrInvalidReviewTransition
		}
		bytes += int64(size)
		if bytes > MaxCommentBodyBytes {
			return OwnedDraftBody{}, fmt.Errorf(
				"%w: comment body exceeds %d bytes",
				ErrInvalidReviewTransition,
				MaxCommentBodyBytes,
			)
		}
	}
	body := string(value)
	if _, err := scanCommentText(nil, body); err != nil {
		return OwnedDraftBody{}, ErrInvalidReviewTransition
	}
	return OwnedDraftBody{value: body, valid: true}, nil
}

// String returns the transferred immutable text.
func (body OwnedDraftBody) String() string {
	if !body.valid {
		return ""
	}
	return body.value
}
