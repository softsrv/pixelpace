//go:build integration

package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	internalhttp "github.com/softsrv/starter/internal/http"
)

func TestFriendsResponseTypesNoEmailOrUser(t *testing.T) {
	// Guard: friends response types are dedicated structs that never embed or encode db.User or carry an email.
	types := []reflect.Type{
		reflect.TypeOf(app.PublicPlayer{}),
		reflect.TypeOf(app.FriendRequestEntry{}),
		reflect.TypeOf(app.FriendRequests{}),
	}
	for _, typ := range types {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Type == reflect.TypeOf(db.User{}) {
				t.Fatalf("type %s embeds/contains db.User", typ.Name())
			}
			if strings.EqualFold(field.Name, "email") || strings.Contains(strings.ToLower(field.Tag.Get("json")), "email") {
				t.Fatalf("type %s contains email field: %s", typ.Name(), field.Name)
			}
		}
	}
}

func TestFriendsRoutesNoEmailLeakIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	queries := db.New(pool)
	jwtSecret := "integration-test-secret-friends-32b!"

	aID, bID, cID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	bLocal := fmt.Sprintf("friend-b-%s", bID)
	bEmail := fmt.Sprintf("%s@example.test", bLocal)
	cLocal := fmt.Sprintf("friend-c-%s", cID)
	cEmail := fmt.Sprintf("%s@example.test", cLocal)
	aLocal := fmt.Sprintf("friend-a-%s", aID)
	aEmail := fmt.Sprintf("%s@example.test", aLocal)

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, statement := range []string{
			"DELETE FROM friend_requests WHERE sender_id = $1 OR recipient_id = $1",
			"DELETE FROM friendships WHERE user_id_a = $1 OR user_id_b = $1",
			"DELETE FROM users WHERE id = $1",
		} {
			for _, id := range []uuid.UUID{aID, bID, cID} {
				_, _ = pool.Exec(cleanupCtx, statement, id)
			}
		}
	})

	for _, u := range []struct {
		id    uuid.UUID
		email string
		name  string
	}{
		{aID, aEmail, "PlayerA"},
		{bID, bEmail, "PlayerB"},
		{cID, cEmail, "PlayerC"},
	} {
		if _, err := queries.CreateUser(ctx, db.CreateUserParams{
			ID:           u.id,
			Email:        u.email,
			PasswordHash: "unused",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "UPDATE users SET display_name = $2 WHERE id = $1", u.id, u.name); err != nil {
			t.Fatal(err)
		}
	}

	friendSvc := app.NewFriendService(queries, pool, app.FriendServiceConfig{Cooldown: 10 * 24 * time.Hour})
	routerCtx, cancelRouter := context.WithCancel(ctx)
	defer cancelRouter()

	router := internalhttp.NewRouter(routerCtx, internalhttp.RouterConfig{
		Queries:   queries,
		FriendSvc: friendSvc,
		JWTSecret: jwtSecret,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	tokenA, err := auth.IssueAccessToken(aID, aEmail, jwtSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tokenB, err := auth.IssueAccessToken(bID, bEmail, jwtSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tokenC, err := auth.IssueAccessToken(cID, cEmail, jwtSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	client := srv.Client()
	forbiddenStrings := []string{bEmail, bLocal, cEmail, cLocal}

	assertNoLeak := func(t *testing.T, body string) {
		t.Helper()
		for _, forbidden := range forbiddenStrings {
			if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
				t.Fatalf("leaked email token %q in response body: %s", forbidden, body)
			}
		}
	}

	// 1. POST /friends/requests (A sends to B)
	form := url.Values{"recipient_id": {bID.String()}}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/friends/requests", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "access_token", Value: tokenA.AccessToken})
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /friends/requests status = %d, body = %s", resp.StatusCode, string(bodyBytes))
	}
	assertNoLeak(t, string(bodyBytes))

	var reqAB struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(bodyBytes, &reqAB); err != nil {
		t.Fatalf("unmarshal send response: %v", err)
	}

	// 2. GET /friends/requests as A
	req, err = http.NewRequest(http.MethodGet, srv.URL+"/friends/requests", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "access_token", Value: tokenA.AccessToken})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /friends/requests (A) status = %d, body = %s", resp.StatusCode, string(bodyBytes))
	}
	assertNoLeak(t, string(bodyBytes))

	// 3. GET /friends/requests as B
	req, err = http.NewRequest(http.MethodGet, srv.URL+"/friends/requests", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "access_token", Value: tokenB.AccessToken})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /friends/requests (B) status = %d, body = %s", resp.StatusCode, string(bodyBytes))
	}
	assertNoLeak(t, string(bodyBytes))

	// 4. POST /friends/requests/{id}/accept (B accepts A's request)
	req, err = http.NewRequest(http.MethodPost, fmt.Sprintf("%s/friends/requests/%s/accept", srv.URL, reqAB.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "access_token", Value: tokenB.AccessToken})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("accept status = %d, body = %s", resp.StatusCode, string(bodyBytes))
	}
	assertNoLeak(t, string(bodyBytes))

	// 5. GET /friends as A
	req, err = http.NewRequest(http.MethodGet, srv.URL+"/friends", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "access_token", Value: tokenA.AccessToken})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /friends (A) status = %d, body = %s", resp.StatusCode, string(bodyBytes))
	}
	assertNoLeak(t, string(bodyBytes))

	// 6. POST /friends/requests (C sends to A)
	form = url.Values{"recipient_id": {aID.String()}}
	req, err = http.NewRequest(http.MethodPost, srv.URL+"/friends/requests", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "access_token", Value: tokenC.AccessToken})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /friends/requests (C to A) status = %d, body = %s", resp.StatusCode, string(bodyBytes))
	}
	assertNoLeak(t, string(bodyBytes))

	var reqCA struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(bodyBytes, &reqCA); err != nil {
		t.Fatalf("unmarshal send CA response: %v", err)
	}

	// 7. POST /friends/requests/{id}/reject (A rejects C's request)
	req, err = http.NewRequest(http.MethodPost, fmt.Sprintf("%s/friends/requests/%s/reject", srv.URL, reqCA.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "access_token", Value: tokenA.AccessToken})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reject status = %d, body = %s", resp.StatusCode, string(bodyBytes))
	}
	assertNoLeak(t, string(bodyBytes))
}
