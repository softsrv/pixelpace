package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
)

type stubFriendService struct {
	sendFn   func(context.Context, uuid.UUID, uuid.UUID) (db.FriendRequest, error)
	acceptFn func(context.Context, uuid.UUID, uuid.UUID) error
	rejectFn func(context.Context, uuid.UUID, uuid.UUID) error
	listFn   func(context.Context, uuid.UUID) ([]uuid.UUID, error)
}

func (s *stubFriendService) Send(ctx context.Context, a, b uuid.UUID) (db.FriendRequest, error) {
	return s.sendFn(ctx, a, b)
}
func (s *stubFriendService) Accept(ctx context.Context, a, b uuid.UUID) error {
	return s.acceptFn(ctx, a, b)
}
func (s *stubFriendService) Reject(ctx context.Context, a, b uuid.UUID) error {
	return s.rejectFn(ctx, a, b)
}
func (s *stubFriendService) ListFriends(ctx context.Context, a uuid.UUID) ([]uuid.UUID, error) {
	return s.listFn(ctx, a)
}

func TestFriendsAuthenticatedActor(t *testing.T) {
	actor, target, spoof := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, op := range []string{"send", "accept", "reject", "list"} {
		t.Run(op, func(t *testing.T) {
			called := false
			check := func(_ context.Context, gotActor, gotTarget uuid.UUID) error {
				called = true
				if gotActor != actor || gotTarget != target {
					t.Fatalf("actor/target = %s/%s, want %s/%s", gotActor, gotTarget, actor, target)
				}
				return nil
			}
			stub := &stubFriendService{
				sendFn: func(ctx context.Context, a, b uuid.UUID) (db.FriendRequest, error) {
					return db.FriendRequest{ID: target, SenderID: a, RecipientID: b, Status: "pending"}, check(ctx, a, b)
				},
				acceptFn: check,
				rejectFn: check,
				listFn: func(_ context.Context, a uuid.UUID) ([]uuid.UUID, error) {
					called = true
					if a != actor {
						t.Fatalf("actor = %s, want %s", a, actor)
					}
					return []uuid.UUID{target}, nil
				},
			}
			h := handlers.NewFriendHandler(stub)
			fn, method, status := h.Send, http.MethodPost, http.StatusCreated
			switch op {
			case "accept":
				fn, status = h.Accept, http.StatusNoContent
			case "reject":
				fn, status = h.Reject, http.StatusNoContent
			case "list":
				fn, method, status = h.ListFriends, http.MethodGet, http.StatusOK
			}
			protected, req := authedRequest(t, fn, actor, method, "/friends")
			req.SetPathValue("id", target.String())
			form := url.Values{"recipient_id": {target.String()}, "sender_id": {spoof.String()}, "user_id": {spoof.String()}, "acting_user_id": {spoof.String()}}
			req.Body = io.NopCloser(strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			protected.ServeHTTP(rr, req)
			if rr.Code != status || !called {
				t.Fatalf("status = %d, called = %v", rr.Code, called)
			}
			if op == "list" {
				var ids []uuid.UUID
				if err := json.Unmarshal(rr.Body.Bytes(), &ids); err != nil || len(ids) != 1 || ids[0] != target {
					t.Fatalf("list response = %s (%v)", rr.Body, err)
				}
			}
			if op == "send" {
				var got db.FriendRequest
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || got.SenderID != actor || got.RecipientID != target || got.Status != "pending" {
					t.Fatalf("send response = %s (%v)", rr.Body, err)
				}
			}
		})
	}
}

func TestFriendsErrorStatuses(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{app.ErrNotRecipient, 404}, {app.ErrForbidden, 404}, {app.ErrNoSuchRequest, 404},
		{app.ErrAlreadyFriends, 409}, {app.ErrRequestPending, 409}, {app.ErrCooldownActive, 409},
		{errors.New("private database details"), 500},
	} {
		for _, op := range []string{"send", "accept", "reject", "list"} {
			t.Run(op+"/"+tc.err.Error(), func(t *testing.T) {
				wrapped := fmt.Errorf("wrapped: %w", tc.err)
				decide := func(context.Context, uuid.UUID, uuid.UUID) error { return wrapped }
				h := handlers.NewFriendHandler(&stubFriendService{
					sendFn: func(context.Context, uuid.UUID, uuid.UUID) (db.FriendRequest, error) {
						return db.FriendRequest{}, wrapped
					},
					acceptFn: decide, rejectFn: decide,
					listFn: func(context.Context, uuid.UUID) ([]uuid.UUID, error) { return nil, wrapped },
				})
				fn, method := h.Send, http.MethodPost
				switch op {
				case "accept":
					fn = h.Accept
				case "reject":
					fn = h.Reject
				case "list":
					fn, method = h.ListFriends, http.MethodGet
				}
				id := uuid.Must(uuid.NewV7())
				protected, req := authedRequest(t, fn, id, method, "/friends")
				req.SetPathValue("id", id.String())
				req.Body = io.NopCloser(strings.NewReader("recipient_id=" + id.String()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rr := httptest.NewRecorder()
				protected.ServeHTTP(rr, req)
				if rr.Code != tc.status {
					t.Fatalf("status = %d, want %d", rr.Code, tc.status)
				}
				if strings.Contains(rr.Body.String(), "private database details") {
					t.Fatal("leaked internal error")
				}
			})
		}
	}
}

func TestFriendsInvalidIDAndAuthentication(t *testing.T) {
	h := handlers.NewFriendHandler(&stubFriendService{}) // Any unexpected service call fails the test.
	for _, fn := range []http.HandlerFunc{h.Send, h.Accept, h.Reject} {
		protected, req := authedRequest(t, fn, uuid.Must(uuid.NewV7()), http.MethodPost, "/friends")
		req.SetPathValue("id", "bad-id")
		rr := httptest.NewRecorder()
		protected.ServeHTTP(rr, req)
		if rr.Code != 400 {
			t.Fatalf("invalid ID status = %d", rr.Code)
		}
	}
	for _, fn := range []http.HandlerFunc{h.Send, h.Accept, h.Reject, h.ListFriends} {
		protected, req := authedRequest(t, fn, uuid.Must(uuid.NewV7()), http.MethodPost, "/friends")
		req.Header.Del("Cookie")
		rr := httptest.NewRecorder()
		protected.ServeHTTP(rr, req)
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
			t.Fatalf("unauthenticated status = %d, location = %q", rr.Code, rr.Header().Get("Location"))
		}
		rr = httptest.NewRecorder()
		fn(rr, req)
		if rr.Code != 401 {
			t.Fatalf("missing context status = %d", rr.Code)
		}
	}
}
