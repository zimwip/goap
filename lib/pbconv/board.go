package pbconv

import (
	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
)

func BoardIssueToPB(i domain.BoardIssue) *graphv1.BoardIssue {
	return &graphv1.BoardIssue{Item: string(i.Item), Culprit: string(i.Culprit), Code: i.Code, Message: i.Message, Severity: i.Severity}
}

func BoardIssueFromPB(i *graphv1.BoardIssue) domain.BoardIssue {
	return domain.BoardIssue{Item: domain.ItemID(i.Item), Culprit: domain.ItemID(i.Culprit), Code: i.Code, Message: i.Message, Severity: i.Severity}
}
