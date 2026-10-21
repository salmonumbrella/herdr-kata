package kata

import "testing"

func TestCommentUIDAndReader(t *testing.T) {
	c := fakeCLI(t, `{"comment":{"uid":"canonical","body":"finding"}}`, nil)
	v, e := c.Comment(t.Context(), CommentReq{Ref: "issue", Body: "finding", IdempotencyKey: "key"})
	if e != nil || v.UID != "canonical" {
		t.Fatal(v, e)
	}
	c = fakeCLI(t, `{"issue":{"uid":"issue"},"comments":[{"uid":"canonical","body":"finding"}]}`, nil)
	i, e := c.ShowIssue(t.Context(), "issue")
	if e != nil || len(i.Comments) != 1 || i.Comments[0].UID != "canonical" {
		t.Fatal(i, e)
	}
}
