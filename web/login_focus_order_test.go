package web

import (
	"html/template"
	"regexp"
	"strings"
	"testing"
)

func parseBaseTemplates(t *testing.T) *template.Template {
	t.Helper()
	base, err := template.ParseFS(FS, "templates/base.html")
	if err != nil {
		t.Fatalf("parse base.html: %v", err)
	}
	if _, err := base.ParseFS(FS, "templates/partials/*.html"); err != nil {
		t.Fatalf("parse partials: %v", err)
	}
	return base
}

func renderPage(t *testing.T, base *template.Template, pagePath string, data any) string {
	t.Helper()
	clone, err := base.Clone()
	if err != nil {
		t.Fatalf("clone base: %v", err)
	}
	if _, err := clone.ParseFS(FS, pagePath); err != nil {
		t.Fatalf("parse %s: %v", pagePath, err)
	}
	var buf strings.Builder
	if err := clone.ExecuteTemplate(&buf, "base.html", data); err != nil {
		t.Fatalf("execute %s: %v", pagePath, err)
	}
	return buf.String()
}

// TestLoginFocusOrder proves CLM-1 through CLM-5:
// - CLM-1: Document focus order is email -> password -> forgot link -> submit with no tabindex and no focusable element between email and password.
// - CLM-2: Anchor href="/forgot-password" with text "Forgot password?" is positioned below Password input, label sits above input, anchor has no tabindex.
// - CLM-3: Both inputs have required attribute, form has no novalidate attribute.
// - CLM-4: id="form-error" is positioned before <form>, and partials/error.html has no focusable elements or tabindex.
// - CLM-5: Sibling auth pages maintain field order with submit last, no forgot link before first input, and no tabindex.
func TestLoginFocusOrder(t *testing.T) {
	base := parseBaseTemplates(t)

	loginHTML := renderPage(t, base, "templates/auth/login.html", nil)

	formStart := strings.Index(loginHTML, "<form")
	formEnd := strings.Index(loginHTML, "</form>")
	if formStart == -1 || formEnd == -1 || formEnd < formStart {
		t.Fatalf("could not extract login <form> from rendered page")
	}
	loginForm := loginHTML[formStart : formEnd+len("</form>")]

	t.Run("CLM-1_FocusOrderAndNoTabindex", func(t *testing.T) {
		emailIdx := strings.Index(loginForm, `id="email"`)
		passIdx := strings.Index(loginForm, `id="password"`)
		forgotIdx := strings.Index(loginForm, `href="/forgot-password"`)
		submitIdx := strings.Index(loginForm, `type="submit"`)

		if emailIdx == -1 || passIdx == -1 || forgotIdx == -1 || submitIdx == -1 {
			t.Fatalf("missing required element in login form: email=%d, password=%d, forgot=%d, submit=%d",
				emailIdx, passIdx, forgotIdx, submitIdx)
		}

		if !(emailIdx < passIdx && passIdx < forgotIdx && forgotIdx < submitIdx) {
			t.Errorf("expected focus order email (%d) < password (%d) < forgot (%d) < submit (%d)",
				emailIdx, passIdx, forgotIdx, submitIdx)
		}

		// Assert no <a>, <button>, <input>, <select> or <textarea> opens between email and password inputs
		emailInputClose := emailIdx + strings.Index(loginForm[emailIdx:], ">") + 1
		passInputOpen := strings.LastIndex(loginForm[:passIdx], "<input")
		if passInputOpen == -1 {
			t.Fatalf("could not find <input tag opening for password")
		}
		between := loginForm[emailInputClose:passInputOpen]
		for _, tag := range []string{"<a", "<button", "<input", "<select", "<textarea"} {
			if strings.Contains(strings.ToLower(between), tag) {
				t.Errorf("found forbidden tag %q between email and password inputs: %q", tag, between)
			}
		}

		// Form must not contain any tabindex attribute
		if strings.Contains(loginForm, "tabindex") {
			t.Errorf("login form contains tabindex: %s", loginForm)
		}
	})

	t.Run("CLM-2_ForgotPasswordLinkPositionAndAttributes", func(t *testing.T) {
		if count := strings.Count(loginHTML, `href="/forgot-password"`); count != 1 {
			t.Errorf("expected exactly 1 href=\"/forgot-password\" on login page, got %d", count)
		}

		anchorRe := regexp.MustCompile(`<a\s+[^>]*href="/forgot-password"[^>]*>(.*?)</a>`)
		matches := anchorRe.FindStringSubmatch(loginHTML)
		if len(matches) < 2 {
			t.Fatalf("could not match anchor with href=\"/forgot-password\"")
		}

		if strings.TrimSpace(matches[1]) != "Forgot password?" {
			t.Errorf("expected anchor text %q, got %q", "Forgot password?", strings.TrimSpace(matches[1]))
		}

		if strings.Contains(matches[0], "tabindex") {
			t.Errorf("forgot-password anchor carries tabindex: %s", matches[0])
		}

		// Anchor must come after id="password" and before type="submit"
		passIdx := strings.Index(loginForm, `id="password"`)
		forgotIdx := strings.Index(loginForm, `href="/forgot-password"`)
		submitIdx := strings.Index(loginForm, `type="submit"`)
		if !(passIdx < forgotIdx && forgotIdx < submitIdx) {
			t.Errorf("expected password (%d) < forgot (%d) < submit (%d)", passIdx, forgotIdx, submitIdx)
		}

		// Password label comes before input#password
		labelPassIdx := strings.Index(loginForm, `for="password"`)
		if labelPassIdx == -1 {
			t.Errorf("missing label for=\"password\" in login form")
		} else if labelPassIdx > passIdx {
			t.Errorf("Password label at %d must come before input#password at %d", labelPassIdx, passIdx)
		}
	})

	t.Run("CLM-3_RequiredAttributesAndNoNovalidate", func(t *testing.T) {
		emailIdx := strings.Index(loginForm, `id="email"`)
		emailTagEnd := emailIdx + strings.Index(loginForm[emailIdx:], ">")
		emailTag := loginForm[emailIdx:emailTagEnd]
		if !strings.Contains(emailTag, "required") {
			t.Errorf("email input missing required attribute: %s", emailTag)
		}

		passIdx := strings.Index(loginForm, `id="password"`)
		passTagEnd := passIdx + strings.Index(loginForm[passIdx:], ">")
		passTag := loginForm[passIdx:passTagEnd]
		if !strings.Contains(passTag, "required") {
			t.Errorf("password input missing required attribute: %s", passTag)
		}

		formTagEnd := strings.Index(loginForm, ">")
		formTag := loginForm[:formTagEnd]
		if strings.Contains(formTag, "novalidate") {
			t.Errorf("login form has novalidate attribute: %s", formTag)
		}
	})

	t.Run("CLM-4_ErrorStatePlacementAndPartial", func(t *testing.T) {
		formErrorIdx := strings.Index(loginHTML, `id="form-error"`)
		formIdx := strings.Index(loginHTML, "<form")
		if formErrorIdx == -1 {
			t.Fatalf("missing id=\"form-error\" in login page")
		}
		if formIdx == -1 {
			t.Fatalf("missing <form in login page")
		}
		if formErrorIdx >= formIdx {
			t.Errorf("id=\"form-error\" at %d must appear before <form at %d", formErrorIdx, formIdx)
		}

		formErrorClose := formErrorIdx + strings.Index(loginHTML[formErrorIdx:], "</div>")
		if formIdx <= formErrorClose {
			t.Errorf("<form is nested inside form-error div")
		}

		// Render partials/error.html with error data on a clone so base is not marked executed
		errClone, err := base.Clone()
		if err != nil {
			t.Fatalf("clone base for error partial: %v", err)
		}
		var errBuf strings.Builder
		errData := map[string]any{"Error": "Invalid email or password"}
		if err := errClone.ExecuteTemplate(&errBuf, "partials/error.html", errData); err != nil {
			t.Fatalf("execute partials/error.html: %v", err)
		}
		errOut := errBuf.String()

		if !strings.Contains(errOut, "Invalid email or password") {
			t.Errorf("expected error text in partials/error.html output, got: %s", errOut)
		}

		for _, focusable := range []string{"<a", "<button", "<input", "<select", "<textarea", "tabindex"} {
			if strings.Contains(strings.ToLower(errOut), focusable) {
				t.Errorf("partials/error.html contains focusable element or tabindex %q: %s", focusable, errOut)
			}
		}
	})

	t.Run("CLM-5_SiblingPagesGuard", func(t *testing.T) {
		siblings := []struct {
			name     string
			pagePath string
			inputIDs []string
		}{
			{
				name:     "register",
				pagePath: "templates/auth/register.html",
				inputIDs: []string{`id="email"`, `id="password"`},
			},
			{
				name:     "forgot-password",
				pagePath: "templates/auth/forgot-password.html",
				inputIDs: []string{`id="email"`},
			},
			{
				name:     "reset-password",
				pagePath: "templates/auth/reset-password.html",
				inputIDs: []string{`id="password"`},
			},
		}

		for _, tc := range siblings {
			t.Run(tc.name, func(t *testing.T) {
				pageData := map[string]any{"Token": "test-token"}
				pageOut := renderPage(t, base, tc.pagePath, pageData)

				fStart := strings.Index(pageOut, "<form")
				fEnd := strings.Index(pageOut, "</form>")
				if fStart == -1 || fEnd == -1 || fEnd < fStart {
					t.Fatalf("could not extract form from %s", tc.name)
				}
				sForm := pageOut[fStart : fEnd+len("</form>")]

				if strings.Contains(sForm, "tabindex") {
					t.Errorf("%s form contains tabindex: %s", tc.name, sForm)
				}

				lastIdx := -1
				for _, inputID := range tc.inputIDs {
					idx := strings.Index(sForm, inputID)
					if idx == -1 {
						t.Fatalf("%s form missing %s", tc.name, inputID)
					}
					if idx <= lastIdx {
						t.Errorf("%s form input %s at %d is not after previous input at %d", tc.name, inputID, idx, lastIdx)
					}
					lastIdx = idx
				}

				submitIdx := strings.Index(sForm, `type="submit"`)
				if submitIdx == -1 {
					t.Fatalf("%s form missing type=\"submit\"", tc.name)
				}
				if submitIdx <= lastIdx {
					t.Errorf("%s submit button at %d is before inputs (last input at %d)", tc.name, submitIdx, lastIdx)
				}

				forgotIdx := strings.Index(sForm, `href="/forgot-password"`)
				firstInputIdx := strings.Index(sForm, tc.inputIDs[0])
				if forgotIdx != -1 && forgotIdx < firstInputIdx {
					t.Errorf("%s has href=\"/forgot-password\" at %d before first input at %d", tc.name, forgotIdx, firstInputIdx)
				}
			})
		}
	})
}
