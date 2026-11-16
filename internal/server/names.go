package server

import (
	"unicode"
	"unicode/utf8"

	"cafeteria-management/internal/i18n"
	"cafeteria-management/internal/textnorm"
)

// maxNameLen is the longest template or list name, in characters.
const maxNameLen = 80

// Problems found by checkName.
const (
	nameInvalid  = "invalid"
	nameRequired = "required"
	nameTooLong  = "too_long"
)

// checkName cleans a template or list name as typed by the user. problem is
// "" (clean is then the name to store), nameInvalid, nameRequired or
// nameTooLong. The rules are written once for templates and lists.
func checkName(name string) (clean, problem string) {
	switch c := textnorm.Clean(name); {
	case !utf8.ValidString(name) || hasControl(name):
		return "", nameInvalid
	case c == "":
		return "", nameRequired
	case utf8.RuneCountInString(c) > maxNameLen:
		return "", nameTooLong
	default:
		return c, ""
	}
}

// nameError returns the localized message of a checkName problem, using the
// keys prefix + "name_invalid" / "name_required" / "name_too_long".
func nameError(loc i18n.Localizer, prefix, problem string) string {
	if problem == nameTooLong {
		return loc.T(prefix+"name_too_long", maxNameLen)
	}
	return loc.T(prefix + "name_" + problem)
}

// validateListName checks a list name and returns the cleaned name, or a
// localized error message.
func validateListName(loc i18n.Localizer, name string) (clean, errMsg string) {
	clean, problem := checkName(name)
	if problem != "" {
		return "", nameError(loc, "lists.error.", problem)
	}
	return clean, ""
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
