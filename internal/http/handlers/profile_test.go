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
