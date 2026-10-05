package access

import (
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// UserPrefix starts the key of a user node (UserKey). The node doubles as the personal unit of the user: the
// OrgUnit of the "organisation" namespace that holds the changes of one person only (their preferences, ADR
// 0037, 0039). A change held by it is personal: nobody else reads or changes it, and it is never shared through
// sub-changes.
const UserPrefix = "USR:"

// UserKey is the key of the node of a user, also the key of its personal unit.
func UserKey(subject string) string { return UserPrefix + subject }

// PersonalUnit returns the key of the personal unit of a subject (the key of its user node).
func PersonalUnit(subject string) string { return UserKey(subject) }

// IsPersonalUnit reports whether a unit key designates a personal unit.
func IsPersonalUnit(key string) bool { return strings.HasPrefix(key, UserPrefix) }

// PersonalSubject returns the subject a personal unit belongs to ("" for any other unit).
func PersonalSubject(key string) string {
	if !IsPersonalUnit(key) {
		return ""
	}
	return strings.TrimPrefix(key, UserPrefix)
}

// IsPersonal reports whether the change is held by a personal unit.
func IsPersonal(c domain.Change) bool { return IsPersonalUnit(c.OwnerOrg) }

// PersonalSubjectOf returns the subject a personal change belongs to ("" for a change that is not personal).
func PersonalSubjectOf(c domain.Change) string { return PersonalSubject(c.OwnerOrg) }

// IsPersonalTo reports whether the change is personal to the subject.
func IsPersonalTo(c domain.Change, subject string) bool {
	return subject != "" && IsPersonal(c) && PersonalSubjectOf(c) == subject
}
