package handlers_test

import (
	"context"
	"errors"
	"fmt"
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

type stubLobbyService struct {
	lobbyFn       func(ctx context.Context, caller, roomID uuid.UUID) (app.RoomLobby, error)
	raceTypesFn   func(ctx context.Context) ([]app.RaceTypeOption, error)
	changeTypeFn  func(ctx context.Context, caller, roomID, raceTypeID uuid.UUID) error
	unreadyFn     func(ctx context.Context, userID, roomID uuid.UUID) error
	joinFn        func(ctx context.Context, userID uuid.UUID, code string) (uuid.UUID, error)
	inviteFn      func(ctx context.Context, inviter, roomID, invitee uuid.UUID) error
	pendingFn     func(ctx context.Context, userID uuid.UUID) ([]app.RoomInvite, error)
	acceptFn      func(ctx context.Context, userID, inviteID uuid.UUID) (uuid.UUID, error)
	declineFn     func(ctx context.Context, userID, inviteID uuid.UUID) error
	listFriendsFn func(ctx context.Context, userID uuid.UUID) ([]app.PublicPlayer, error)
}

func (s *stubLobbyService) Lobby(ctx context.Context, caller, roomID uuid.UUID) (app.RoomLobby, error) {
	return s.lobbyFn(ctx, caller, roomID)
}
func (s *stubLobbyService) RaceTypeOptions(ctx context.Context) ([]app.RaceTypeOption, error) {
	return s.raceTypesFn(ctx)
}
func (s *stubLobbyService) ChangeRaceType(ctx context.Context, caller, roomID, raceTypeID uuid.UUID) error {
	return s.changeTypeFn(ctx, caller, roomID, raceTypeID)
}
func (s *stubLobbyService) MarkUnready(ctx context.Context, userID, roomID uuid.UUID) error {
	return s.unreadyFn(ctx, userID, roomID)
}
func (s *stubLobbyService) JoinRoom(ctx context.Context, userID uuid.UUID, code string) (uuid.UUID, error) {
	return s.joinFn(ctx, userID, code)
}
func (s *stubLobbyService) InviteFriend(ctx context.Context, inviter, roomID, invitee uuid.UUID) error {
	return s.inviteFn(ctx, inviter, roomID, invitee)
}
func (s *stubLobbyService) PendingInvites(ctx context.Context, userID uuid.UUID) ([]app.RoomInvite, error) {
	return s.pendingFn(ctx, userID)
}
func (s *stubLobbyService) AcceptInvite(ctx context.Context, userID, inviteID uuid.UUID) (uuid.UUID, error) {
	return s.acceptFn(ctx, userID, inviteID)
}
func (s *stubLobbyService) DeclineInvite(ctx context.Context, userID, inviteID uuid.UUID) error {
	return s.declineFn(ctx, userID, inviteID)
}
func (s *stubLobbyService) ListFriendEntries(ctx context.Context, userID uuid.UUID) ([]app.PublicPlayer, error) {
	return s.listFriendsFn(ctx, userID)
}

var lobbyUser = db.User{ID: uuid.MustParse("01950000-0000-7000-8000-0000000000b1"), Email: "host@example.com", EmailVerified: true}

func serveLobby(t *testing.T, svc *stubLobbyService, method, path string, pathValues map[string]string, form url.Values,
	endpoint func(*handlers.LobbyHandler, http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	h := handlers.NewLobbyHandler(svc, svc, newTestRenderer(t), "https://pixelpace.test/")
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	return serveAuthenticated(t, lobbyUser, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { endpoint(h, w, r) }), req)
}

func waitingLobby(roomID uuid.UUID) app.RoomLobby {
	guest := uuid.MustParse("01950000-0000-7000-8000-0000000000b2")
	return app.RoomLobby{
		RoomID: roomID, Status: "waiting", JoinCode: "ABC234", RaceLabel: "2000m", IsHost: true, CanStart: true,
		Participants: []app.RoomRosterEntry{
			{PublicPlayer: app.PublicPlayer{UserID: lobbyUser.ID, Name: "Hosty"}, Host: true},
			{PublicPlayer: app.PublicPlayer{UserID: guest, Name: "Guesty"}, Ready: true},
		},
		InvitedIDs: map[uuid.UUID]bool{uuid.MustParse("01950000-0000-7000-8000-0000000000b3"): true},
	}
}

func TestLobbyPageShowsJoinLinkRaceTypesFriendsAndRoster(t *testing.T) {
	roomID := uuid.New()
	svc := &stubLobbyService{
		lobbyFn: func(_ context.Context, caller, gotRoom uuid.UUID) (app.RoomLobby, error) {
			if caller != lobbyUser.ID || gotRoom != roomID {
				t.Errorf("Lobby(%s, %s)", caller, gotRoom)
			}
			return waitingLobby(roomID), nil
		},
		raceTypesFn: func(context.Context) ([]app.RaceTypeOption, error) {
			return []app.RaceTypeOption{{ID: uuid.New(), Label: "500m"}, {ID: uuid.New(), Label: "2000m"}}, nil
		},
		listFriendsFn: func(_ context.Context, userID uuid.UUID) ([]app.PublicPlayer, error) {
			return []app.PublicPlayer{
				{UserID: uuid.MustParse("01950000-0000-7000-8000-0000000000b2"), Name: "Guesty"},
				{UserID: uuid.MustParse("01950000-0000-7000-8000-0000000000b3"), Name: "Pending"},
				{UserID: uuid.New(), Name: "Fresh"},
			}, nil
		},
	}
	rr := serveLobby(t, svc, http.MethodGet, "/rooms/"+roomID.String()+"/lobby", map[string]string{"id": roomID.String()}, nil, (*handlers.LobbyHandler).Page)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%q", rr.Code, rr.Body.String())
	}
	for _, want := range []string{
		"lobby:https://pixelpace.test/join/ABC234",
		"types:500m,2000m,",
		"friends:Guesty+joined,Pending+invited,Fresh,",
		"panel:2000m|Hosty|Guesty+ready|can-start",
	} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Errorf("body = %q, want it to contain %q", rr.Body.String(), want)
		}
	}
}

func TestLobbyPageForStartedRoomRedirectsToRace(t *testing.T) {
	roomID := uuid.New()
	for _, status := range []string{"counting_down", "in_progress", "finished"} {
		t.Run(status, func(t *testing.T) {
			svc := &stubLobbyService{lobbyFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomLobby, error) {
				lobby := waitingLobby(roomID)
				lobby.Status = status
				return lobby, nil
			}}
			rr := serveLobby(t, svc, http.MethodGet, "/", map[string]string{"id": roomID.String()}, nil, (*handlers.LobbyHandler).Page)
			if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/rooms/"+roomID.String()+"/race" {
				t.Fatalf("status = %d, Location = %q", rr.Code, rr.Header().Get("Location"))
			}
		})
	}
}

func TestLobbyPageUnavailableRoom(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		err    error
		code   int
	}{
		{"not a member", "", app.ErrParticipantNotFound, http.StatusNotFound},
		{"missing", "", app.ErrRoomNotFound, http.StatusNotFound},
		{"dissolved", "dissolved", nil, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubLobbyService{lobbyFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomLobby, error) {
				return app.RoomLobby{Status: tc.status}, tc.err
			}}
			rr := serveLobby(t, svc, http.MethodGet, "/", map[string]string{"id": uuid.NewString()}, nil, (*handlers.LobbyHandler).Page)
			if rr.Code != tc.code || !strings.Contains(rr.Body.String(), "unavailable:") {
				t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestLobbyPanelRefreshesOrRedirects(t *testing.T) {
	roomID := uuid.New()
	for _, tc := range []struct {
		name     string
		status   string
		err      error
		code     int
		redirect string
		body     string
	}{
		{"waiting", "waiting", nil, http.StatusOK, "", "panel:2000m|Hosty|Guesty+ready|can-start"},
		{"race started", "counting_down", nil, http.StatusNoContent, "/rooms/" + roomID.String() + "/race", ""},
		{"kicked", "", app.ErrParticipantNotFound, http.StatusNoContent, "/dashboard", ""},
		{"dissolved", "dissolved", nil, http.StatusNoContent, "/dashboard", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubLobbyService{lobbyFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomLobby, error) {
				lobby := waitingLobby(roomID)
				lobby.Status = tc.status
				return lobby, tc.err
			}}
			rr := serveLobby(t, svc, http.MethodGet, "/", map[string]string{"id": roomID.String()}, nil, (*handlers.LobbyHandler).Panel)
			if rr.Code != tc.code || rr.Header().Get("HX-Redirect") != tc.redirect {
				t.Fatalf("status = %d, HX-Redirect = %q; want %d, %q", rr.Code, rr.Header().Get("HX-Redirect"), tc.code, tc.redirect)
			}
			if tc.body != "" && strings.TrimSpace(rr.Body.String()) != tc.body {
				t.Errorf("body = %q, want only the panel %q", rr.Body.String(), tc.body)
			}
		})
	}
}

func TestLobbyChangeRaceTypeAndUnreadyRefreshLobby(t *testing.T) {
	roomID, raceTypeID := uuid.New(), uuid.New()
	var calls []string
	svc := &stubLobbyService{
		changeTypeFn: func(_ context.Context, caller, gotRoom, gotType uuid.UUID) error {
			calls = append(calls, fmt.Sprintf("type:%v:%v:%v", caller == lobbyUser.ID, gotRoom == roomID, gotType == raceTypeID))
			return nil
		},
		unreadyFn: func(_ context.Context, userID, gotRoom uuid.UUID) error {
			calls = append(calls, fmt.Sprintf("unready:%v:%v", userID == lobbyUser.ID, gotRoom == roomID))
			return nil
		},
	}
	ids := map[string]string{"id": roomID.String()}
	for _, rr := range []*httptest.ResponseRecorder{
		serveLobby(t, svc, http.MethodPost, "/", ids, url.Values{"race_type_id": {raceTypeID.String()}}, (*handlers.LobbyHandler).ChangeRaceType),
		serveLobby(t, svc, http.MethodPost, "/", ids, nil, (*handlers.LobbyHandler).MarkUnready),
	} {
		if rr.Code != http.StatusNoContent || rr.Header().Get("HX-Trigger") != "room-updated" {
			t.Errorf("status = %d, HX-Trigger = %q", rr.Code, rr.Header().Get("HX-Trigger"))
		}
	}
	if strings.Join(calls, ",") != "type:true:true:true,unready:true:true" {
		t.Errorf("calls = %v", calls)
	}

	svc.changeTypeFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error { return app.ErrNotHost }
	if rr := serveLobby(t, svc, http.MethodPost, "/", ids, url.Values{"race_type_id": {raceTypeID.String()}}, (*handlers.LobbyHandler).ChangeRaceType); rr.Code != http.StatusForbidden {
		t.Errorf("guest change status = %d, want 403", rr.Code)
	}
	if rr := serveLobby(t, svc, http.MethodPost, "/", ids, url.Values{"race_type_id": {"nope"}}, (*handlers.LobbyHandler).ChangeRaceType); rr.Code != http.StatusBadRequest {
		t.Errorf("invalid race type status = %d, want 400", rr.Code)
	}
}

func TestLobbyInviteFriend(t *testing.T) {
	roomID, friend := uuid.New(), uuid.New()
	for _, tc := range []struct {
		err  error
		code int
	}{
		{nil, http.StatusOK},
		{app.ErrForbidden, http.StatusForbidden},
		{app.ErrRoomNotJoinable, http.StatusConflict},
		{app.ErrParticipantNotFound, http.StatusNotFound},
	} {
		t.Run(fmt.Sprint(tc.err), func(t *testing.T) {
			svc := &stubLobbyService{inviteFn: func(_ context.Context, inviter, gotRoom, invitee uuid.UUID) error {
				if inviter != lobbyUser.ID || gotRoom != roomID || invitee != friend {
					t.Errorf("InviteFriend(%s, %s, %s)", inviter, gotRoom, invitee)
				}
				return tc.err
			}}
			rr := serveLobby(t, svc, http.MethodPost, "/", map[string]string{"id": roomID.String()}, url.Values{"user_id": {friend.String()}}, (*handlers.LobbyHandler).Invite)
			if rr.Code != tc.code {
				t.Fatalf("status = %d, want %d", rr.Code, tc.code)
			}
			if tc.err == nil && rr.Body.String() != "invited" {
				t.Errorf("body = %q, want the invited fragment", rr.Body.String())
			}
		})
	}
}

func TestLobbyJoinLink(t *testing.T) {
	roomID := uuid.New()
	for _, tc := range []struct {
		name     string
		roomID   uuid.UUID
		err      error
		code     int
		location string
	}{
		{"joins", roomID, nil, http.StatusSeeOther, "/rooms/" + roomID.String() + "/lobby"},
		{"already joined", roomID, app.ErrAlreadyParticipant, http.StatusSeeOther, "/rooms/" + roomID.String() + "/lobby"},
		{"unknown code", uuid.Nil, app.ErrRoomNotFound, http.StatusNotFound, ""},
		{"started", uuid.Nil, app.ErrRoomNotJoinable, http.StatusConflict, ""},
		{"full", uuid.Nil, app.ErrRoomFull, http.StatusConflict, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubLobbyService{joinFn: func(_ context.Context, userID uuid.UUID, code string) (uuid.UUID, error) {
				if userID != lobbyUser.ID || code != "ABC234" {
					t.Errorf("JoinRoom(%s, %q)", userID, code)
				}
				return tc.roomID, tc.err
			}}
			rr := serveLobby(t, svc, http.MethodGet, "/join/abc234", map[string]string{"code": "abc234"}, nil, (*handlers.LobbyHandler).JoinLink)
			if rr.Code != tc.code || rr.Header().Get("Location") != tc.location {
				t.Fatalf("status = %d, Location = %q; want %d, %q", rr.Code, rr.Header().Get("Location"), tc.code, tc.location)
			}
		})
	}
}

func TestLobbyDashboardRaceCardAndInvites(t *testing.T) {
	inviteID := uuid.New()
	svc := &stubLobbyService{
		raceTypesFn: func(context.Context) ([]app.RaceTypeOption, error) {
			return []app.RaceTypeOption{{ID: uuid.New(), Label: "1000m"}}, nil
		},
		pendingFn: func(_ context.Context, userID uuid.UUID) ([]app.RoomInvite, error) {
			if userID != lobbyUser.ID {
				t.Errorf("PendingInvites(%s)", userID)
			}
			return []app.RoomInvite{{ID: inviteID, RaceLabel: "500m", Inviter: app.PublicPlayer{Name: "Ada"}}}, nil
		},
	}
	if rr := serveLobby(t, svc, http.MethodGet, "/", nil, nil, (*handlers.LobbyHandler).RaceCard); rr.Body.String() != "race-card:1000m," {
		t.Errorf("race card = %q", rr.Body.String())
	}
	if rr := serveLobby(t, svc, http.MethodGet, "/", nil, nil, (*handlers.LobbyHandler).Invites); rr.Body.String() != "invites:Ada/500m," {
		t.Errorf("invites = %q", rr.Body.String())
	}
}

func TestLobbyAcceptAndDeclineInvite(t *testing.T) {
	roomID, inviteID := uuid.New(), uuid.New()
	ids := map[string]string{"id": inviteID.String()}
	svc := &stubLobbyService{
		acceptFn: func(_ context.Context, userID, gotInvite uuid.UUID) (uuid.UUID, error) {
			if userID != lobbyUser.ID || gotInvite != inviteID {
				t.Errorf("AcceptInvite(%s, %s)", userID, gotInvite)
			}
			return roomID, nil
		},
		declineFn: func(_ context.Context, userID, gotInvite uuid.UUID) error {
			if userID != lobbyUser.ID || gotInvite != inviteID {
				t.Errorf("DeclineInvite(%s, %s)", userID, gotInvite)
			}
			return nil
		},
	}
	rr := serveLobby(t, svc, http.MethodPost, "/", ids, nil, (*handlers.LobbyHandler).AcceptInvite)
	if rr.Code != http.StatusNoContent || rr.Header().Get("HX-Redirect") != "/rooms/"+roomID.String()+"/lobby" {
		t.Errorf("accept: status = %d, HX-Redirect = %q", rr.Code, rr.Header().Get("HX-Redirect"))
	}
	rr = serveLobby(t, svc, http.MethodPost, "/", ids, nil, (*handlers.LobbyHandler).DeclineInvite)
	if rr.Code != http.StatusNoContent || rr.Header().Get("HX-Trigger") != "invites-updated" {
		t.Errorf("decline: status = %d, HX-Trigger = %q", rr.Code, rr.Header().Get("HX-Trigger"))
	}

	// An invite to a room that has started or filled is refused and the list refreshes.
	svc.acceptFn = func(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, error) {
		return uuid.Nil, app.ErrRoomNotJoinable
	}
	rr = serveLobby(t, svc, http.MethodPost, "/", ids, nil, (*handlers.LobbyHandler).AcceptInvite)
	if rr.Code != http.StatusConflict || rr.Header().Get("HX-Trigger") != "invites-updated" || rr.Header().Get("HX-Redirect") != "" {
		t.Errorf("stale accept: status = %d, headers = %v", rr.Code, rr.Header())
	}
	// Declining twice is harmless.
	svc.declineFn = func(context.Context, uuid.UUID, uuid.UUID) error { return app.ErrRoomNotFound }
	if rr = serveLobby(t, svc, http.MethodPost, "/", ids, nil, (*handlers.LobbyHandler).DeclineInvite); rr.Code != http.StatusNoContent {
		t.Errorf("repeat decline status = %d", rr.Code)
	}
	svc.declineFn = func(context.Context, uuid.UUID, uuid.UUID) error { return errors.New("db down") }
	if rr = serveLobby(t, svc, http.MethodPost, "/", ids, nil, (*handlers.LobbyHandler).DeclineInvite); rr.Code != http.StatusInternalServerError {
		t.Errorf("failed decline status = %d", rr.Code)
	}
}
