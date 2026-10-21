package sched

// ChildUID identifies either inbox source without assigning cron provenance.
func (d Due) ChildUID() string {
	if d.IssueRequest != nil {
		return d.IssueRequest.IssueUID
	}
	if d.Child != nil {
		return d.Child.IssueUID
	}
	return ""
}
