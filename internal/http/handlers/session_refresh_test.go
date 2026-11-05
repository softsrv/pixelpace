package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/http/handlers"
)

// refreshCookies returns the response's refresh_token cookies keyed by path.
func refreshCookies(rr *httptest.ResponseRecorder) map[string]*http.Cookie {
	out := map[string]*http.Cookie{}
	for _, c := range rr.Result().Cookies() {
		if c.Name == "refresh_token" {
			out[c.Path] = c
		}
	}
	return out
}

func TestRefreshSession_RenewsFromRefreshCookie(t *testing.T) {
	t.Parallel()
	var gotRaw string
	stub := &stubAuthService{
		refreshFn: func(_ context.Context, raw string, _ app.DeviceMeta) (app.TokenResult, error) {
			gotRaw = raw
			return okTokens(true), nil
		},
	}
	h := handlers.NewAuthHandler(stub, newTestRenderer(t), false, 0)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "rt-123"})
	rr := httptest.NewRecorder()
	token, ok := h.RefreshSession(rr, req)

	if !ok || token != "access-token-value" {
		t.Fatalf("RefreshSession = %q, %v; want the new access token", token, ok)
	}
	if gotRaw != "rt-123" {
		t.Fatalf("refreshed with %q, want the cookie's refresh token", gotRaw)
	}
	if c := cookieByName(rr.Result().Cookies(), "access_token"); c == nil || c.Value != "access-token-value" {
		t.Fatalf("access_token cookie = %+v, want the new token", c)
	}
	rc := refreshCookies(rr)
	if c := rc["/"]; c == nil || c.Value != "refresh-token-value" || !c.HttpOnly {
		t.Fatalf("refresh_token cookie at Path=/ = %+v, want the session's httpOnly refresh token", c)
	}
	if c := rc["/auth"]; c == nil || c.MaxAge >= 0 {
		t.Fatalf("legacy refresh_token cookie at Path=/auth = %+v, want it expired", c)
	}
}

func TestRefreshSession_NoCookie(t *testing.T) {
	t.Parallel()
	called := false
	stub := &stubAuthService{
		refreshFn: func(context.Context, string, app.DeviceMeta) (app.TokenResult, error) {
			called = true
			return okTokens(true), nil
		},
	}
	h := handlers.NewAuthHandler(stub, newTestRenderer(t), false, 0)

	rr := httptest.NewRecorder()
	if _, ok := h.RefreshSession(rr, httptest.NewRequest(http.MethodGet, "/dashboard", nil)); ok || called {
		t.Fatalf("RefreshSession without a cookie: ok=%v, service called=%v; want neither", ok, called)
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Fatalf("cookies = %v, want none", rr.Result().Cookies())
	}
}

func TestRefreshSession_Failures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		err   error
		clear bool
	}{
		{"unknown token", app.ErrTokenNotFound, true},
		{"revoked token", app.ErrTokenRevoked, true},
		{"expired token", app.ErrTokenExpired, true},
		{"server failure", errors.New("database unavailable"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stub := &stubAuthService{
				refreshFn: func(context.Context, string, app.DeviceMeta) (app.TokenResult, error) {
					return app.TokenResult{}, tc.err
				},
			}
			h := handlers.NewAuthHandler(stub, newTestRenderer(t), false, 0)

			req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
			req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "rt-123"})
			rr := httptest.NewRecorder()
			if _, ok := h.RefreshSession(rr, req); ok {
				t.Fatal("RefreshSession succeeded, want false")
			}
			cleared := false
			if c := refreshCookies(rr)["/"]; c != nil && c.MaxAge < 0 {
				cleared = true
			}
			if cleared != tc.clear {
				t.Fatalf("refresh cookie cleared = %v, want %v", cleared, tc.clear)
			}
		})
	}
}

func TestLogout_ClearsRefreshCookieAtBothPaths(t *testing.T) {
	t.Parallel()
	h := handlers.NewAuthHandler(&stubAuthService{}, newTestRenderer(t), false, 0)

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "rt-123"})
	rr := httptest.NewRecorder()
	h.Logout(rr, req)

	rc := refreshCookies(rr)
	for _, path := range []string{"/", "/auth"} {
		if c := rc[path]; c == nil || c.MaxAge >= 0 {
			t.Fatalf("refresh_token cookie at Path=%s = %+v, want it expired", path, c)
		}
	}
}

// A login or email-verification auto-login hands the service every refresh
// token the browser presents, so the new session replaces the old one(s) —
// including a legacy Path=/auth cookie sent alongside the Path=/ one.
func TestNewSession_PassesPresentedRefreshTokens(t *testing.T) {
	t.Parallel()
	var loginGot, verifyGot []string
	stub := &stubAuthService{
		loginFn: func(_ context.Context, _, _ string, m app.DeviceMeta) (app.TokenResult, error) {
			loginGot = m.ReplacesRefreshTokens
			return okTokens(true), nil
		},
		verifyFn: func(_ context.Context, _ string, m app.DeviceMeta) (app.TokenResult, error) {
			verifyGot = m.ReplacesRefreshTokens
			return okTokens(true), nil
		},
	}
	h := handlers.NewAuthHandler(stub, newTestRenderer(t), false, 0)

	req := postForm(url.Values{"email": {"a@b.com"}, "password": {"password123"}})
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "legacy"})
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "current"})
	h.Login(httptest.NewRecorder(), req)
	if want := []string{"legacy", "current"}; !slices.Equal(loginGot, want) {
		t.Errorf("Login ReplacesRefreshTokens = %q, want %q", loginGot, want)
	}

	req = httptest.NewRequest(http.MethodGet, "/auth/verify-email?token=tok", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "current"})
	h.VerifyEmail(httptest.NewRecorder(), req)
	if want := []string{"current"}; !slices.Equal(verifyGot, want) {
		t.Errorf("VerifyEmail ReplacesRefreshTokens = %q, want %q", verifyGot, want)
	}

	loginGot = []string{"unset"}
	h.Login(httptest.NewRecorder(), postForm(url.Values{"email": {"a@b.com"}, "password": {"password123"}}))
	if len(loginGot) != 0 {
		t.Errorf("Login without cookies ReplacesRefreshTokens = %q, want none", loginGot)
	}
}
