package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
	"github.com/softsrv/starter/internal/http/middleware"
)

// stubAuthService implements the (unexported) authServicer interface that
// AuthHandler depends on. Each behavior is a function field so individual tests
// can inject exactly the outcome they need.
type stubAuthService struct {
	registerFn   func(context.Context, string, string) (db.User, error)
	loginFn      func(context.Context, string, string, app.DeviceMeta) (app.TokenResult, error)
	logoutFn     func(context.Context, string) error
	refreshFn    func(context.Context, string, app.DeviceMeta) (app.TokenResult, error)
	reqResetFn   func(context.Context, string) error
	complResetFn func(context.Context, string, string) error
	verifyFn     func(context.Context, string, app.DeviceMeta) (app.TokenResult, error)
	resendFn     func(context.Context, uuid.UUID) error
}

func (s *stubAuthService) Register(ctx context.Context, email, password string) (db.User, error) {
	if s.registerFn != nil {
		return s.registerFn(ctx, email, password)
	}
	return db.User{}, nil
}

func (s *stubAuthService) Login(ctx context.Context, email, password string, m app.DeviceMeta) (app.TokenResult, error) {
	if s.loginFn != nil {
		return s.loginFn(ctx, email, password, m)
	}
	return app.TokenResult{}, nil
}

func (s *stubAuthService) Logout(ctx context.Context, raw string) error {
	if s.logoutFn != nil {
		return s.logoutFn(ctx, raw)
	}
	return nil
}

func (s *stubAuthService) Refresh(ctx context.Context, raw string, m app.DeviceMeta) (app.TokenResult, error) {
	if s.refreshFn != nil {
		return s.refreshFn(ctx, raw, m)
	}
	return app.TokenResult{}, nil
}

func (s *stubAuthService) RequestPasswordReset(ctx context.Context, email string) error {
	if s.reqResetFn != nil {
		return s.reqResetFn(ctx, email)
	}
	return nil
}

func (s *stubAuthService) CompletePasswordReset(ctx context.Context, token, pw string) error {
	if s.complResetFn != nil {
		return s.complResetFn(ctx, token, pw)
	}
	return nil
}

func (s *stubAuthService) VerifyEmail(ctx context.Context, token string, m app.DeviceMeta) (app.TokenResult, error) {
	if s.verifyFn != nil {
		return s.verifyFn(ctx, token, m)
	}
	return app.TokenResult{}, nil
}

func (s *stubAuthService) ResendVerification(ctx context.Context, id uuid.UUID) error {
	if s.resendFn != nil {
		return s.resendFn(ctx, id)
	}
	return nil
}

func okTokens(verified bool) app.TokenResult {
	return app.TokenResult{
		AccessToken:        "access-token-value",
		AccessTokenExpiry:  time.Now().Add(15 * time.Minute),
		RefreshToken:       "refresh-token-value",
		RefreshTokenExpiry: time.Now().Add(720 * time.Hour),
		EmailVerified:      verified,
	}
}

func cookieByName(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func postForm(values url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestLogin_SuccessVerified(t *testing.T) {
	t.Parallel()
	stub := &stubAuthService{
		loginFn: func(context.Context, string, string, app.DeviceMeta) (app.TokenResult, error) {
			return okTokens(true), nil
		},
	}
	h := handlers.NewAuthHandler(stub, newTestRenderer(t), false, 0)

	rr := httptest.NewRecorder()
	h.Login(rr, postForm(url.Values{"email": {"a@b.com"}, "password": {"password123"}}))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("HX-Redirect"); got != "/dashboard" {
		t.Errorf("HX-Redirect = %q, want /dashboard", got)
	}
	cookies := rr.Result().Cookies()
	if cookieByName(cookies, "access_token") == nil {
		t.Error("access_token cookie not set")
	}
	if cookieByName(cookies, "refresh_token") == nil {
		t.Error("refresh_token cookie not set")
	}
}

func TestLogin_SuccessUnverifiedRedirectsToVerify(t *testing.T) {
	t.Parallel()
	stub := &stubAuthService{
		loginFn: func(context.Context, string, string, app.DeviceMeta) (app.TokenResult, error) {
			return okTokens(false), nil
		},
	}
	h := handlers.NewAuthHandler(stub, newTestRenderer(t), false, 0)

	rr := httptest.NewRecorder()
	h.Login(rr, postForm(url.Values{"email": {"a@b.com"}, "password": {"password123"}}))

	if got := rr.Header().Get("HX-Redirect"); got != "/verify-email" {
		t.Errorf("HX-Redirect = %q, want /verify-email", got)
	}
}

func TestLogin_InvalidCredentialsHTMXDowngradesTo200(t *testing.T) {
	t.Parallel()
	stub := &stubAuthService{
		loginFn: func(context.Context, string, string, app.DeviceMeta) (app.TokenResult, error) {
			return app.TokenResult{}, app.ErrInvalidCredentials
		},
	}
	h := handlers.NewAuthHandler(stub, newTestRenderer(t), false, 0)

	req := postForm(url.Values{"email": {"a@b.com"}, "password": {"wrong"}})
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	// HTMX only swaps 2xx responses, so the error partial is served as 200.
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (HTMX downgrade)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "error:") {
		t.Errorf("body = %q, want error partial", rr.Body.String())
	}
}

func TestLogin_AccountLockedNonHTMXReturns423(t *testing.T) {
	t.Parallel()
	stub := &stubAuthService{
		loginFn: func(context.Context, string, string, app.DeviceMeta) (app.TokenResult, error) {
			return app.TokenResult{}, app.ErrAccountLocked
		},
	}
	h := handlers.NewAuthHandler(stub, newTestRenderer(t), false, 0)

	rr := httptest.NewRecorder()
	h.Login(rr, postForm(url.Values{"email": {"a@b.com"}, "password": {"x"}}))

	if rr.Code != http.StatusLocked {
		t.Errorf("status = %d, want 423", rr.Code)
	}
}

func TestRegister_SuccessSetsCookiesAndRedirects(t *testing.T) {
	t.Parallel()
	stub := &stubAuthService{
		registerFn: func(context.Context, string, string) (db.User, error) {
			return db.User{ID: uuid.New(), Email: "a@b.com"}, nil
		},
		loginFn: func(context.Context, string, string, app.DeviceMeta) (app.TokenResult, error) {
			return okTokens(false), nil
		},
	}
	h := handlers.NewAuthHandler(stub, newTestRenderer(t), false, 0)

	rr := httptest.NewRecorder()
	h.Register(rr, postForm(url.Values{"email": {"a@b.com"}, "password": {"password123"}}))

	if got := rr.Header().Get("HX-Redirect"); got != "/verify-email" {
		t.Errorf("HX-Redirect = %q, want /verify-email", got)
	}
	if cookieByName(rr.Result().Cookies(), "access_token") == nil {
		t.Error("access_token cookie not set after register")
	}
}

func TestLogout_ClearsCookiesAndRevokes(t *testing.T) {
	t.Parallel()
	var revoked string
	stub := &stubAuthService{
		logoutFn: func(_ context.Context, raw string) error { revoked = raw; return nil },
	}
	h := handlers.NewAuthHandler(stub, newTestRenderer(t), false, 0)

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "rt-123"})
	rr := httptest.NewRecorder()
	h.Logout(rr, req)

	if revoked != "rt-123" {
		t.Errorf("Logout called with %q, want rt-123", revoked)
	}
	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rr.Code)
	}
	if c := cookieByName(rr.Result().Cookies(), "access_token"); c == nil || c.MaxAge >= 0 {
		t.Error("access_token cookie not expired on logout")
	}
}

func TestRefresh_MissingCookieReturns401(t *testing.T) {
	t.Parallel()
	h := handlers.NewAuthHandler(&stubAuthService{}, newTestRenderer(t), false, 0)

	rr := httptest.NewRecorder()
	h.Refresh(rr, httptest.NewRequest(http.MethodPost, "/auth/refresh", nil))

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestRefresh_SuccessSetsCookies(t *testing.T) {
	t.Parallel()
	stub := &stubAuthService{
		refreshFn: func(context.Context, string, app.DeviceMeta) (app.TokenResult, error) {
			return okTokens(true), nil
		},
	}
	h := handlers.NewAuthHandler(stub, newTestRenderer(t), false, 0)

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "rt-123"})
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if cookieByName(rr.Result().Cookies(), "access_token") == nil {
		t.Error("access_token cookie not refreshed")
	}
}

// TestResendVerification_Success exercises the handler through the real
// Authenticate middleware so the user is populated in the request context the
// same way it is in production.
func TestResendVerification_Success(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	called := false
	stub := &stubAuthService{
		resendFn: func(_ context.Context, id uuid.UUID) error {
			called = id == userID
			return nil
		},
	}
	h := handlers.NewAuthHandler(stub, newTestRenderer(t), false, 0)

	fetcher := &handlerUserFetcher{user: db.User{ID: userID, Email: "a@b.com", EmailVerified: false}}
	protected := middleware.Authenticate(fetcher, testJWTSecret)(http.HandlerFunc(h.ResendVerification))

	tp, err := auth.IssueAccessToken(userID, "a@b.com", testJWTSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/resend-verification", nil)
	req.AddCookie(&http.Cookie{Name: "access_token", Value: tp.AccessToken})
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if !called {
		t.Error("ResendVerification service method not called with the authenticated user id")
	}
	if got := rr.Header().Get("Location"); got != "/verify-email?resent=1" {
		t.Errorf("redirect = %q, want /verify-email?resent=1", got)
	}
}

// handlerUserFetcher satisfies middleware.UserFetcher for context population.
type handlerUserFetcher struct {
	user db.User
	err  error
}

func (f *handlerUserFetcher) GetUserByID(context.Context, uuid.UUID) (db.User, error) {
	return f.user, f.err
}
