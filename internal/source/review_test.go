package source

import "github.com/jacoelho/rvw/internal/review"

func testReviewRequest(snapshot review.Snapshot) review.ReviewRequest {
	return review.ReviewRequest{Snapshot: snapshot}
}
