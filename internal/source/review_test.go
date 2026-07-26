package source

import "github.com/jacoelho/rvw/internal/change"

func testReviewRequest(snapshot change.Snapshot) change.ReviewRequest {
	return change.ReviewRequest{Snapshot: snapshot}
}
