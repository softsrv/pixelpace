package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
)

func TestQuickMatchRoutesRequireVerifiedUser(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, verified := range []bool{false, true} {
		user := db.User{ID: uuid.Must(uuid.NewV7()), Email: "match@example.com", EmailVerified: verified}
		token, err := auth.IssueAccessToken(user.ID, user.Email, routerTestJWTSecret, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		h := NewRouter(ctx, RouterConfig{Queries: routerUserFetcher{user: user}, JWTSecret: routerTestJWTSecret})
		for _, path := range []string{"/quick-match", "/quick-match/leave", "/quick-match/match"} {
			for _, authenticated := range []bool{false, true} {
				// An invalid form lets a verified request reach the real handler
				// without requiring a database or allowing a nil-service call.
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("race_type_id=invalid"))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("HX-Request", "true")
				if authenticated {
					req.AddCookie(&http.Cookie{Name: "access_token", Value: token.AccessToken})
				}
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, req)
				want := http.StatusBadRequest
				if !authenticated {
					want = http.StatusUnauthorized
				} else if !verified {
					want = http.StatusForbidden
				}
				if rr.Code != want {
					t.Fatalf("%s authenticated=%v verified=%v: %d, want %d", path, authenticated, verified, rr.Code, want)
				}
			}
		}
	}
}
