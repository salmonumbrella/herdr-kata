package kata

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (c *CLI) issueCall(ctx context.Context, body []byte, args ...string) (IssueView, error) {
	var v struct {
		Comments []Comment `json:"comments"`
		Issue    IssueView `json:"issue"`
		Parent   *LinkPeer `json:"parent"`
		Links    []Link    `json:"links"`
		Labels   []struct {
			Label string `json:"label"`
		} `json:"labels"`
	}
	err := c.call(ctx, body, &v, args...)
	if v.Parent != nil {
		v.Issue.ParentUID = v.Parent.UID
	}
	v.Issue.Links = v.Links
	if v.Issue.Labels == nil {
		for _, label := range v.Labels {
			v.Issue.Labels = append(v.Issue.Labels, label.Label)
		}
	}
	v.Issue.Comments = v.Comments
	c.qualify(&v.Issue)
	return v.Issue, err
}
func (c *CLI) qualify(i *IssueView) {
	if i.QualifiedID == "" && i.ShortID != "" {
		i.QualifiedID = c.cfg.Project + "#" + i.ShortID
	}
}
func (c *CLI) CreateIssue(ctx context.Context, req CreateReq) (IssueView, error) {
	args := []string{"create", "--body-stdin"}
	if req.IdempotencyKey != "" {
		args = append(args, "--idempotency-key", req.IdempotencyKey)
	}
	if req.Parent != "" {
		args = append(args, "--parent", req.Parent)
	}
	labels := append([]string(nil), req.Labels...)
	sort.Strings(labels)
	for _, label := range labels {
		args = append(args, "--label", label)
	}
	if req.ForceNew {
		args = append(args, "--force-new")
	}
	keys := make([]string, 0, len(req.Meta))
	for key := range req.Meta {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--meta", key+"="+req.Meta[key])
	}
	return c.issueCall(ctx, []byte(req.Body), append(args, "--", req.Title)...)
}
func (c *CLI) ShowIssue(ctx context.Context, ref string) (IssueView, error) {
	return c.issueCall(ctx, nil, "show", "--", ref)
}

// PlanningDate is one native planning date at the issue revision Kata read.
// Kata resolves the instant from the raw value and the issue's timezone.
type PlanningDate struct {
	Field    string    `json:"field"`
	Value    string    `json:"value"`
	Timezone string    `json:"timezone"`
	Instant  time.Time `json:"instant"`
}
type PlanningDates struct {
	IssueUID  string        `json:"issue_uid"`
	Revision  int64         `json:"revision"`
	Scheduled *PlanningDate `json:"scheduled_on"`
}

func (c *CLI) PlanningDates(ctx context.Context, ref string) (PlanningDates, error) {
	var v PlanningDates
	err := c.call(ctx, nil, &v, "show", "--planning-dates", "--", ref)
	return v, err
}
func (c *CLI) Close(ctx context.Context, req CloseReq) error {
	args := []string{"close", "--reason", req.Reason, "--message", req.Message}
	if req.IfMatch != "" {
		args = append(args, "--if-match", req.IfMatch)
	}
	if req.IdempotencyKey != "" {
		args = append(args, "--idempotency-key", req.IdempotencyKey)
	}
	for _, e := range req.Evidence {
		args = append(args, "--evidence", e)
	}
	return c.call(ctx, nil, nil, append(args, "--", req.Ref)...)
}
func (c *CLI) Comment(ctx context.Context, req CommentReq) (Comment, error) {
	args := []string{"comment", "--body-stdin"}
	if req.IdempotencyKey != "" {
		args = append(args, "--idempotency-key", req.IdempotencyKey)
	}
	if req.ReplyTo != "" {
		if req.Kind != "reply" && req.Kind != "confirm" && req.Kind != "refute" {
			return Comment{}, fmt.Errorf("invalid reply kind %q", req.Kind)
		}
		args = append(args, "--"+req.Kind, req.ReplyTo)
	}
	var v struct {
		Comment Comment `json:"comment"`
	}
	err := c.call(ctx, []byte(req.Body), &v, append(args, "--", req.Ref)...)
	return v.Comment, err
}
func conditions(args []string, cond Cond) []string {
	if cond.IfMatch != "" {
		args = append(args, "--if-match", cond.IfMatch)
	}
	if cond.IfAbsent {
		args = append(args, "--if-absent")
	}
	if cond.IfValue != nil {
		args = append(args, "--if-value", *cond.IfValue)
	}
	if cond.JSONValue {
		args = append(args, "--json-value")
	}
	return args
}
func (c *CLI) MetaSet(ctx context.Context, ref, key, value string, cond Cond) (int64, error) {
	i, err := c.issueCall(ctx, nil, append(conditions([]string{"meta", "set"}, cond), "--", ref, key, value)...)
	return i.Revision, err
}
func (c *CLI) MetaUnset(ctx context.Context, ref, key string, cond Cond) (int64, error) {
	i, err := c.issueCall(ctx, nil, append(conditions([]string{"meta", "unset"}, cond), "--", ref, key)...)
	return i.Revision, err
}
func (c *CLI) Deadline(ctx context.Context, ref, at string, cond Cond) error {
	args := []string{"deadline"}
	if cond.IfMatch != "" {
		args = append(args, "--if-match", cond.IfMatch)
	}
	return c.call(ctx, nil, nil, append(args, "--", ref, at)...)
}
func (c *CLI) Notify(ctx context.Context, ref, to, msg string) error {
	return c.call(ctx, nil, nil, "notify", "--to", to, "--message", msg, "--", ref)
}
func (c *CLI) LabelAdd(ctx context.Context, ref, label string) error {
	return c.call(ctx, nil, nil, "label", "add", "--", ref, label)
}
func (c *CLI) Claim(ctx context.Context, ref string, ifUnowned bool) (IssueView, error) {
	args := []string{"claim"}
	if ifUnowned {
		args = append(args, "--if-unowned")
	}
	return c.issueCall(ctx, nil, append(args, "--", ref)...)
}
func (c *CLI) Ready(ctx context.Context) ([]IssueView, error) {
	var v struct {
		Issues []IssueView `json:"issues"`
	}
	err := c.call(ctx, nil, &v, "ready", "--limit", "0")
	for i := range v.Issues {
		c.qualify(&v.Issues[i])
	}
	return v.Issues, err
}
func (c *CLI) Inbox(ctx context.Context, addr string) ([]InboxRequest, error) {
	var v struct {
		Requests []InboxRequest `json:"requests"`
	}
	err := c.call(ctx, nil, &v, "inbox", "--for", addr)
	return v.Requests, err
}

// IssueReference retains the authoritative short ID, including collision extensions.
func IssueReference(project string, issue IssueView) string {
	if i := strings.LastIndex(issue.QualifiedID, "#"); i > 0 && i < len(issue.QualifiedID)-1 {
		return issue.QualifiedID
	}
	if project != "" && issue.ShortID != "" {
		return project + "#" + issue.ShortID
	}
	return ""
}

// OldestLabeled returns the project's oldest issue with label, open or
// closed, in one list call.
func (c *CLI) OldestLabeled(ctx context.Context, label string) (IssueView, bool, error) {
	var v struct {
		Issues []IssueView `json:"issues"`
	}
	if err := c.call(ctx, nil, &v, "list", "--status", "all", "--label", label, "--sort", "oldest", "--limit", "1"); err != nil {
		return IssueView{}, false, err
	}
	if len(v.Issues) == 0 {
		return IssueView{}, false, nil
	}
	c.qualify(&v.Issues[0])
	return v.Issues[0], true, nil
}
