package handlers_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
	"github.com/softsrv/starter/internal/users"
)

type stubProfileUserService struct {
	deleteAccountFn  func(ctx context.Context, userID uuid.UUID) error
	setDisplayNameFn func(ctx context.Context, userID uuid.UUID, name string) error
}

func (s *stubProfileUserService) DeleteAccount(ctx context.Context, userID uuid.UUID) error {
	if s.deleteAccountFn != nil {
		return s.deleteAccountFn(ctx, userID)
	}
	return nil
}

func (s *stubProfileUserService) SetDisplayName(ctx context.Context, userID uuid.UUID, name string) error {
	if s.setDisplayNameFn != nil {
		return s.setDisplayNameFn(ctx, userID, name)
	}
	return nil
}

type stubProfileAuthService struct {
	changePasswordFn func(ctx context.Context, userID uuid.UUID, currentPassword, newPassword string) error
}

func (s *stubProfileAuthService) ChangePassword(ctx context.Context, userID uuid.UUID, currentPassword, newPassword string) error {
	if s.changePasswordFn != nil {
		return s.changePasswordFn(ctx, userID, currentPassword, newPassword)
	}
	return nil
}

func TestProfileSetDisplayName_Success(t *testing.T) {
	actorID := uuid.New()
	var gotUserID uuid.UUID
	var gotName string
	called := false

	userSvc := &stubProfileUserService{
		setDisplayNameFn: func(ctx context.Context, userID uuid.UUID, name string) error {
			called = true
			gotUserID = userID
			gotName = name
			return nil
		},
	}

	h := handlers.NewProfileHandler(&stubProfileAuthService{}, userSvc, newTestRenderer(t), false)
	protected, req := authedRequest(t, h.SetDisplayName, actorID, http.MethodPost, "/profile/display-name")

	form := url.Values{"display_name": {"  RowerOne  "}}
	req.Body = io.NopCloser(strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if !called {
		t.Fatal("SetDisplayName was not called")
	}
	if gotUserID != actorID {
		t.Fatalf("got userID %v, want %v", gotUserID, actorID)
	}
	if gotName != "RowerOne" {
		t.Fatalf("got name %q, want %q", gotName, "RowerOne")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), "flash:") {
		t.Fatalf("body %q does not contain flash partial prefix", rr.Body.String())
	}
}

func TestProfileSetDisplayName_IgnoresPostedUserID(t *testing.T) {
	actorID := uuid.New()
	spoofID := uuid.New()
	var gotUserID uuid.UUID

	userSvc := &stubProfileUserService{
		setDisplayNameFn: func(ctx context.Context, userID uuid.UUID, name string) error {
			gotUserID = userID
			return nil
		},
	}

	h := handlers.NewProfileHandler(&stubProfileAuthService{}, userSvc, newTestRenderer(t), false)
	protected, req := authedRequest(t, h.SetDisplayName, actorID, http.MethodPost, "/profile/display-name")

	form := url.Values{
		"display_name": {"RowerOne"},
		"user_id":      {spoofID.String()},
	}
	req.Body = io.NopCloser(strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if gotUserID != actorID {
		t.Fatalf("got userID %v, want context user %v (spoofed was %v)", gotUserID, actorID, spoofID)
	}
}

func TestProfileSetDisplayName_ErrDisplayNameTaken(t *testing.T) {
	actorID := uuid.New()
	userSvc := &stubProfileUserService{
		setDisplayNameFn: func(ctx context.Context, userID uuid.UUID, name string) error {
			return app.ErrDisplayNameTaken
		},
	}

	h := handlers.NewProfileHandler(&stubProfileAuthService{}, userSvc, newTestRenderer(t), false)
	protected, req := authedRequest(t, h.SetDisplayName, actorID, http.MethodPost, "/profile/display-name")

	form := url.Values{"display_name": {"RowerOne"}}
	req.Body = io.NopCloser(strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnprocessableEntity)
	}
	if !strings.Contains(rr.Body.String(), "name is already taken") {
		t.Fatalf("body %q does not contain 'name is already taken'", rr.Body.String())
	}
}

func TestProfileSetDisplayName_ValidationError(t *testing.T) {
	validationErrors := []error{
		users.ErrDisplayNameEmpty,
		users.ErrDisplayNameTooShort,
		users.ErrDisplayNameTooLong,
		users.ErrDisplayNameLooksLikeEmail,
		users.ErrDisplayNameInvalidChars,
	}

	for _, vErr := range validationErrors {
		t.Run(vErr.Error(), func(t *testing.T) {
			actorID := uuid.New()
			userSvc := &stubProfileUserService{
				setDisplayNameFn: func(ctx context.Context, userID uuid.UUID, name string) error {
					return vErr
				},
			}

			h := handlers.NewProfileHandler(&stubProfileAuthService{}, userSvc, newTestRenderer(t), false)
			protected, req := authedRequest(t, h.SetDisplayName, actorID, http.MethodPost, "/profile/display-name")

			form := url.Values{"display_name": {"invalid"}}
			req.Body = io.NopCloser(strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			rr := httptest.NewRecorder()
			protected.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnprocessableEntity)
			}
			if !strings.Contains(rr.Body.String(), vErr.Error()) {
				t.Fatalf("body %q does not contain sentinel message %q", rr.Body.String(), vErr.Error())
			}
		})
	}
}

func TestProfileSetDisplayName_UnexpectedError(t *testing.T) {
	actorID := uuid.New()
	userSvc := &stubProfileUserService{
		setDisplayNameFn: func(ctx context.Context, userID uuid.UUID, name string) error {
			return errors.New("db outage")
		},
	}

	h := handlers.NewProfileHandler(&stubProfileAuthService{}, userSvc, newTestRenderer(t), false)
	protected, req := authedRequest(t, h.SetDisplayName, actorID, http.MethodPost, "/profile/display-name")

	form := url.Values{"display_name": {"RowerOne"}}
	req.Body = io.NopCloser(strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(rr.Body.String(), "Failed to update display name") {
		t.Fatalf("body %q does not contain generic error message", rr.Body.String())
	}
}

// renderProfilePage serves ProfilePage over the production templates as a
// verified user, with Concept2 enabled or not.
func renderProfilePage(t *testing.T, concept2Enabled bool, target string) string {
	t.Helper()
	h := handlers.NewProfileHandler(&stubProfileAuthService{}, &stubProfileUserService{}, newProductionRenderer(t), false)
	h.SetConcept2Enabled(concept2Enabled)
	user := db.User{ID: uuid.New(), Email: "rower@example.com", EmailVerified: true}
	rr := serveAuthenticated(t, user, http.HandlerFunc(h.ProfilePage), httptest.NewRequest(http.MethodGet, target, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	return rr.Body.String()
}

// CLM-11: the existing profile sections render unchanged whether Concept2 is
// enabled or not; the Concept2 section appears only when enabled.
func TestProfilePage_ExistingSectionsWithConcept2EnabledOrNot(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		body := renderProfilePage(t, enabled, "/profile")
		for _, want := range []string{
			`hx-post="/profile/display-name"`,
			`name="display_name"`,
			`hx-post="/profile/change-password"`,
			`name="current_password"`,
			`name="new_password"`,
			`hx-get="/auth/sessions"`,
			`hx-post="/profile/delete"`,
			`Delete Account</button>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("enabled=%v: profile missing %q", enabled, want)
			}
		}
		for _, marker := range []string{"Concept2 Logbook", `hx-get="/profile/concept2"`} {
			if got := strings.Contains(body, marker); got != enabled {
				t.Errorf("enabled=%v: profile contains %q = %v", enabled, marker, got)
			}
		}
	}
}

// CLM-2 + CLM-6: the profile shows the success flash after a connect and the
// error partial after a failed one, inside the Concept2 section; without a
// ?concept2= outcome it shows neither.
func TestProfilePage_Concept2Flash(t *testing.T) {
	t.Parallel()
	const okMsg = "Your Concept2 Logbook is connected."
	const errMsg = "Could not connect your Concept2 Logbook. Nothing was changed; please try again."
	for _, tc := range []struct {
		target, want, notWant, noticeClass string
	}{
		{"/profile?concept2=connected", okMsg, errMsg, "pp-notice-ok"},
		{"/profile?concept2=error", errMsg, okMsg, "pp-notice-error"},
	} {
		body := renderProfilePage(t, true, tc.target)
		section := body[strings.Index(body, `aria-labelledby="pp-concept2-title"`):]
		if !strings.Contains(section, tc.want) || !strings.Contains(section, tc.noticeClass) {
			t.Errorf("%s: Concept2 section missing %q in a %s notice", tc.target, tc.want, tc.noticeClass)
		}
		if strings.Contains(body, tc.notWant) {
			t.Errorf("%s: profile also shows %q", tc.target, tc.notWant)
		}
	}
	for _, target := range []string{"/profile", "/profile?concept2=bogus"} {
		body := renderProfilePage(t, true, target)
		if strings.Contains(body, okMsg) || strings.Contains(body, errMsg) {
			t.Errorf("%s: profile shows a Concept2 flash", target)
		}
	}
	// With Concept2 disabled there is no section to flash in.
	if body := renderProfilePage(t, false, "/profile?concept2=error"); strings.Contains(body, errMsg) {
		t.Error("disabled Concept2: profile shows the Concept2 error")
	}
}
