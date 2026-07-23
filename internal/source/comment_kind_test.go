package source

import "github.com/jacoelho/rvw/internal/review"

func testReviewRequest(snapshot review.Snapshot) review.ReviewRequest {
	kinds, err := review.NewCommentKinds("issue", "note", "praise", "suggestion")
	if err != nil {
		panic(err)
	}
	policies, err := review.NewCommentPolicies(review.CommentTypePrefixForGitHub)
	if err != nil {
		panic(err)
	}
	return review.ReviewRequest{
		Snapshot: snapshot, CommentKinds: kinds, CommentPolicies: policies,
	}
}
