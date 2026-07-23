package review

import (
	"fmt"
	"strings"
)

// commentContent is the semantic value synchronized with an RVW-owned GitHub
// comment. wire text and ownership markers are retained separately.
type commentContent struct {
	body string
}

func commentContentFrom(comment Comment) commentContent {
	return commentContent{body: comment.body.String()}
}

func cloneCommentContent(content commentContent) commentContent {
	content.body = strings.Clone(content.body)
	return content
}

func encodeCommentWire(content commentContent) (string, error) {
	if int64(len(content.body)) > MaxCommentBodyBytes {
		return "", fmt.Errorf("%w: GitHub comment exceeds %d bytes", ErrInvalidComment, MaxCommentBodyBytes)
	}
	return strings.Clone(content.body), nil
}

// decodeOwnedCommentWire decodes only text whose RVW ownership marker was
// recognized by the caller. The marker carries ownership; visible text has no
// additional rvw metadata.
func decodeOwnedCommentWire(wire string) commentContent {
	return commentContent{body: wire}
}
