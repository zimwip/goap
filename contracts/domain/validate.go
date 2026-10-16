package domain

// Severity of a blackboard issue.
const (
	IssueError   = "error"
	IssueWarning = "warning"
)

// BoardIssue is an inconsistency found in the content of a blackboard.
type BoardIssue struct {
	// Item is the item where the problem shows.
	Item ItemID `json:"item"`
	// Culprit is the item whose production is at fault: the item itself, or
	// the item another one wrongly builds on (a relaunch restarts the step that
	// produced the culprit).
	Culprit ItemID `json:"culprit"`
	// Code: structure | dangling | derived_from_invalid | reference | outdated | rule | impact | duplicate.
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}
