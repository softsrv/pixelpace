package users

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"
)

var (
	ErrEmailInvalid     = errors.New("email address is invalid")
	ErrPasswordTooShort = errors.New("password is too short")
	ErrPasswordTooLong  = errors.New("password is too long")

	ErrDisplayNameEmpty          = errors.New("name must not be empty")
	ErrDisplayNameTooShort       = errors.New("name must be at least 3 characters")
	ErrDisplayNameTooLong        = errors.New("name must be at most 20 characters")
	ErrDisplayNameLooksLikeEmail = errors.New("name must not look like an email address")
	ErrDisplayNameInvalidChars   = errors.New("name may only contain letters A-Z, digits, underscore, hyphen and period")
)

const (
	minDisplayNameLen = 3
	maxDisplayNameLen = 20
)

// maxPasswordBytes is the effective ceiling imposed by bcrypt, which silently
// truncates any input beyond 72 bytes. We reject longer passwords outright so
// that the bytes a user types are the bytes that actually protect the account.
const maxPasswordBytes = 72

// NormalizeEmail lowercases and trims an email address.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateEmail returns nil if the email is RFC 5322 compliant after normalization.
func ValidateEmail(email string) error {
	if _, err := mail.ParseAddress(email); err != nil {
		return fmt.Errorf("%w: %s", ErrEmailInvalid, email)
	}
	return nil
}

// ValidatePassword checks the password meets minimum requirements.
func ValidatePassword(password string, minLength int) error {
	if len(password) < minLength {
		return fmt.Errorf("%w: minimum %d characters", ErrPasswordTooShort, minLength)
	}
	// bcrypt truncates silently at 72 bytes; reject anything longer so the user
	// isn't lulled into thinking a long passphrase is fully protecting them.
	if len(password) > maxPasswordBytes {
		return fmt.Errorf("%w: maximum %d bytes", ErrPasswordTooLong, maxPasswordBytes)
	}
	// Passphrases are encouraged up to the bcrypt limit.
	// No complexity requirements — length > complexity for user-chosen passwords.
	return nil
}

// ValidateDisplayName checks a public display name after trimming surrounding
// whitespace. It returns one ErrDisplayName* sentinel naming the broken rule.
// The email check runs before the character-set check so an email-like input
// reports why it is refused rather than just flagging the '@'.
func ValidateDisplayName(name string) error {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	switch {
	case n == 0:
		return ErrDisplayNameEmpty
	case n < minDisplayNameLen:
		return ErrDisplayNameTooShort
	case n > maxDisplayNameLen:
		return ErrDisplayNameTooLong
	case strings.Contains(name, "@"):
		return ErrDisplayNameLooksLikeEmail
	}
	for _, r := range name {
		if !isDisplayNameRune(r) {
			return ErrDisplayNameInvalidChars
		}
	}
	return nil
}

func isDisplayNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '_', r == '-', r == '.':
		return true
	}
	return false
}
