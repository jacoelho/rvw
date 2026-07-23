package review

import (
	"errors"
	"unicode"
	"unicode/utf8"
)

const commentTextCancellationBytes = 2_048

var errInvalidCommentText = errors.New("invalid comment text")

// scanCommentText validates one bounded comment-text fragment without retaining
// it and reports whether it contains a non-whitespace rune.
func scanCommentText(cancelled func() error, value string) (bool, error) {
	if int64(len(value)) > MaxCommentBodyBytes {
		return false, errInvalidCommentText
	}
	if err := cancellationError(cancelled); err != nil {
		return false, err
	}

	nonSpace := false
	lastCancellation := 0
	for offset := 0; offset < len(value); {
		runeValue, width := utf8.DecodeRuneInString(value[offset:])
		if offset-lastCancellation+width > commentTextCancellationBytes {
			if err := cancellationError(cancelled); err != nil {
				return false, err
			}
			lastCancellation = offset
		}
		if runeValue == utf8.RuneError || runeValue != '\n' && unicode.IsControl(runeValue) ||
			isBidirectionalControl(runeValue) {
			return false, errInvalidCommentText
		}
		if !unicode.IsSpace(runeValue) {
			nonSpace = true
		}
		offset += width
		if offset-lastCancellation == commentTextCancellationBytes {
			if err := cancellationError(cancelled); err != nil {
				return false, err
			}
			lastCancellation = offset
		}
	}
	if err := cancellationError(cancelled); err != nil {
		return false, err
	}
	return nonSpace, nil
}

// ValidateCommentTextFragment validates unretained editor input. Empty and
// whitespace-only fragments are valid because they may extend an existing body.
func ValidateCommentTextFragment(value string) error {
	if _, err := scanCommentText(nil, value); err != nil {
		return ErrInvalidReviewTransition
	}
	return nil
}
