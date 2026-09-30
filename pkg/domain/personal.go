package domain

import "strings"

// PersonalUnitPrefix starts the key of a personal unit: the OrgUnit of the "organisation" namespace that holds the
// changes of one person only (their preferences, ADR 0037). A change held by it is personal: nobody else reads or
// changes it, and it is never shared through sub-changes.
const PersonalUnitPrefix = "USR:"

// PersonalUnit returns the key of the personal unit of a subject.
func PersonalUnit(subject string) string { return PersonalUnitPrefix + subject }

// IsPersonalUnit reports whether a unit key designates a personal unit.
func IsPersonalUnit(key string) bool { return strings.HasPrefix(key, PersonalUnitPrefix) }

// PersonalSubject returns the subject a personal unit belongs to ("" for any other unit).
func PersonalSubject(key string) string { return strings.TrimPrefix(key, PersonalUnitPrefix) }

// Personal reports whether the change is held by a personal unit.
func (c Change) Personal() bool { return IsPersonalUnit(c.OwnerOrg) }

// PersonalTo reports whether the change is personal to the subject.
func (c Change) PersonalTo(subject string) bool {
	return c.Personal() && subject != "" && PersonalSubject(c.OwnerOrg) == subject
}
