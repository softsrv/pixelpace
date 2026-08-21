package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
	"github.com/softsrv/starter/internal/http/middleware"
)

// stubUserService implements the (unexported) userServicer interface that
// SessionHandler depends on.
type stubUserService struct {
	listFn   func(context.Context, uuid.UUID) ([]db.RefreshToken, error)
	revokeFn func(context.Context, uuid.UUID, uuid.UUID) (db.RefreshToken, error)
}

func (s *stubUserService) ListSessions(ctx context.Context, id uuid.UUID) ([]db.RefreshToken, error) {
	if s.listFn != nil {
		return s.listFn(ctx, id)
	}
	return nil, nil
}

func (s *stubUserService) RevokeSession(ctx context.Context, userID, tokenID uuid.UUID) (db.RefreshToken, error) {
	if s.revokeFn != nil {
		return s.revokeFn(ctx, userID, tokenID)
	}
	return db.RefreshToken{}, nil
}

// authedRequest wraps h in the real Authenticate middleware and returns a
// request carrying a valid access-token cookie for userID, so the handler sees
// the user in context exactly as it would in production.
func authedRequest(t *testing.T, h http.HandlerFunc, userID uuid.UUID, method, target string) (http.Handler, *http.Request) {
	t.Helper()
	fetcher := &handlerUserFetcher{user: db.User{ID: userID, Email: "a@b.com", EmailVerified: true}}
	protected := middleware.Authenticate(fetcher, testJWTSecret)(h)

	tp, err := auth.IssueAccessToken(userID, "a@b.com", testJWTSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	req := httptest.NewRequest(method, target, nil)
	req.AddCookie(&http.Cookie{Name: "access_token", Value: tp.AccessToken})
	return protected, req
}

func TestListSessions_Success(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	stub := &stubUserService{
		listFn: func(context.Context, uuid.UUID) ([]db.RefreshToken, error) {
			return []db.RefreshToken{{ID: uuid.New()}, {ID: uuid.New()}}, nil
		},
	}
	h := handlers.NewSessionHandler(stub, newTestRenderer(t), false)

	protected, req := authedRequest(t, h.ListSessions, userID, http.MethodGet, "/auth/sessions")
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if body := rr.Body.String(); body != "sessions:2" {
		t.Errorf("body = %q, want sessions:2", body)
	}
}

func TestRevokeSession_InvalidIDReturns400(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	h := handlers.NewSessionHandler(&stubUserService{}, newTestRenderer(t), false)

	protected, req := authedRequest(t, h.RevokeSession, userID, http.MethodDelete, "/auth/sessions/not-a-uuid")
	req.SetPathValue("id", "not-a-uuid")
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestRevokeSession_ForbiddenReturns404(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	tokenID := uuid.New()
	stub := &stubUserService{
		revokeFn: func(context.Context, uuid.UUID, uuid.UUID) (db.RefreshToken, error) {
			return db.RefreshToken{}, app.ErrForbidden
		},
	}
	h := handlers.NewSessionHandler(stub, newTestRenderer(t), false)

	protected, req := authedRequest(t, h.RevokeSession, userID, http.MethodDelete, "/auth/sessions/"+tokenID.String())
	req.SetPathValue("id", tokenID.String())
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	// Ownership failures are masked as 404 so they don't confirm a token exists.
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestRevokeSession_SuccessReturnsList(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	tokenID := uuid.New()
	stub := &stubUserService{
		revokeFn: func(context.Context, uuid.UUID, uuid.UUID) (db.RefreshToken, error) {
			// TokenHash won't match any request cookie, so the handler returns
			// the refreshed session-list fragment rather than a logout redirect.
			return db.RefreshToken{ID: tokenID, TokenHash: "some-other-hash"}, nil
		},
		listFn: func(context.Context, uuid.UUID) ([]db.RefreshToken, error) {
			return []db.RefreshToken{{ID: uuid.New()}}, nil
		},
	}
	h := handlers.NewSessionHandler(stub, newTestRenderer(t), false)

	protected, req := authedRequest(t, h.RevokeSession, userID, http.MethodDelete, "/auth/sessions/"+tokenID.String())
	req.SetPathValue("id", tokenID.String())
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if body := rr.Body.String(); body != "sessions:1" {
		t.Errorf("body = %q, want sessions:1", body)
	}
}
