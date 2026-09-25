package pbconv

import (
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/pkg/domain"
)

func BoardIssueToPB(i domain.BoardIssue) *graphv1.BoardIssue {
	return &graphv1.BoardIssue{Item: string(i.Item), Culprit: string(i.Culprit), Code: i.Code, Message: i.Message, Severity: i.Severity}
}

func BoardIssueFromPB(i *graphv1.BoardIssue) domain.BoardIssue {
	return domain.BoardIssue{Item: domain.ItemID(i.Item), Culprit: domain.ItemID(i.Culprit), Code: i.Code, Message: i.Message, Severity: i.Severity}
}
