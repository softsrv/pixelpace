package email_test

import (
	"strings"
	"testing"

	"github.com/softsrv/starter/internal/email"
)

func TestEmailsHaveNoFooter(t *testing.T) {
	_, vHTML, _ := email.VerificationEmail("PixelPace", "https://example.com/verify?token=test")
	if vHTML == "" {
		t.Fatal("VerificationEmail returned empty HTML body")
	}
	if strings.Contains(vHTML, "<footer") {
		t.Errorf("VerificationEmail HTML contains '<footer': %s", vHTML)
	}
	if strings.Contains(vHTML, "2026 softsrv") {
		t.Errorf("VerificationEmail HTML contains '2026 softsrv': %s", vHTML)
	}

	_, prHTML, _ := email.PasswordResetEmail("PixelPace", "https://example.com/reset?token=test")
	if prHTML == "" {
		t.Fatal("PasswordResetEmail returned empty HTML body")
	}
	if strings.Contains(prHTML, "<footer") {
		t.Errorf("PasswordResetEmail HTML contains '<footer': %s", prHTML)
	}
	if strings.Contains(prHTML, "2026 softsrv") {
		t.Errorf("PasswordResetEmail HTML contains '2026 softsrv': %s", prHTML)
	}
}
