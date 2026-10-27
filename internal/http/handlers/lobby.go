package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/http/middleware"
)

// lobbyServicer defines the subset of app.RoomService that LobbyHandler requires.
type lobbyServicer interface {
	Lobby(ctx context.Context, callerUserID, roomID uuid.UUID) (app.RoomLobby, error)
	RaceTypeOptions(ctx context.Context) ([]app.RaceTypeOption, error)
	ChangeRaceType(ctx context.Context, callerUserID, roomID, raceTypeID uuid.UUID) error
	MarkUnready(ctx context.Context, userID, roomID uuid.UUID) error
	JoinRoom(ctx context.Context, userID uuid.UUID, joinCode string) (uuid.UUID, error)
	InviteFriend(ctx context.Context, inviterUserID, roomID, inviteeUserID uuid.UUID) error
	PendingInvites(ctx context.Context, userID uuid.UUID) ([]app.RoomInvite, error)
	AcceptInvite(ctx context.Context, userID, inviteID uuid.UUID) (uuid.UUID, error)
	DeclineInvite(ctx context.Context, userID, inviteID uuid.UUID) error
}

var _ lobbyServicer = (*app.RoomService)(nil)

// friendLister lists a user's friends for the lobby's invite dialog.
type friendLister interface {
	ListFriendEntries(ctx context.Context, userID uuid.UUID) ([]app.PublicPlayer, error)
}

var _ friendLister = (*app.FriendService)(nil)

// LobbyFriend is a friend in the lobby's invite dialog.
type LobbyFriend struct {
	app.PublicPlayer
	Invited bool
	Joined  bool
}

// LobbyHandler serves the pages and fragments around a room before its race:
// the dashboard's race card and invites, the lobby, and join links.
type LobbyHandler struct {
	rooms    lobbyServicer
	friends  friendLister
	renderer *TemplateRenderer
	baseURL  string
}

// NewLobbyHandler constructs a LobbyHandler. baseURL prefixes shareable join links.
func NewLobbyHandler(rooms lobbyServicer, friends friendLister, renderer *TemplateRenderer, baseURL string) *LobbyHandler {
	return &LobbyHandler{rooms: rooms, friends: friends, renderer: renderer, baseURL: strings.TrimRight(baseURL, "/")}
}

// Page renders a room's lobby. A room whose race has started sends its
// participants to the race page.
func (h *LobbyHandler) Page(w http.ResponseWriter, r *http.Request) {
	user, roomID, ok := lobbyRequest(w, r)
	if !ok {
		return
	}
	lobby, err := h.rooms.Lobby(r.Context(), user, roomID)
	if err != nil {
		h.unavailable(w, r, err)
		return
	}
	if started(lobby.Status) {
		http.Redirect(w, r, racePath(roomID), http.StatusSeeOther)
		return
	}
	if lobby.Status != "waiting" {
		h.unavailable(w, r, app.ErrRoomNotJoinable)
		return
	}
	raceTypes, err := h.rooms.RaceTypeOptions(r.Context())
	if err != nil {
		writeRoomError(w, r, err)
		return
	}
	friendEntries, err := h.friends.ListFriendEntries(r.Context(), user)
	if err != nil {
		writeRoomError(w, r, err)
		return
	}
	joined := map[uuid.UUID]bool{}
	for _, p := range lobby.Participants {
		joined[p.UserID] = true
	}
	friends := make([]LobbyFriend, 0, len(friendEntries))
	for _, f := range friendEntries {
		friends = append(friends, LobbyFriend{PublicPlayer: f, Invited: lobby.InvitedIDs[f.UserID], Joined: joined[f.UserID]})
	}
	authUser, _ := middleware.UserFromContext(r.Context())
	h.renderer.Page(w, http.StatusOK, "lobby.html", map[string]any{
		"User":      authUser,
		"Lobby":     lobby,
		"RaceTypes": raceTypes,
		"Friends":   friends,
		"JoinURL":   h.baseURL + "/join/" + url.PathEscape(lobby.JoinCode),
	})
}

// Panel serves the lobby's live region, which the page polls. Once the race
// has started, or the caller is no longer in the room, it redirects instead.
func (h *LobbyHandler) Panel(w http.ResponseWriter, r *http.Request) {
	user, roomID, ok := lobbyRequest(w, r)
	if !ok {
		return
	}
	lobby, err := h.rooms.Lobby(r.Context(), user, roomID)
	switch {
	case errors.Is(err, app.ErrParticipantNotFound), errors.Is(err, app.ErrRoomNotFound):
		w.Header().Set("HX-Redirect", "/dashboard")
		w.WriteHeader(http.StatusNoContent)
		return
	case err != nil:
		writeRoomError(w, r, err)
		return
	case started(lobby.Status):
		w.Header().Set("HX-Redirect", racePath(roomID))
		w.WriteHeader(http.StatusNoContent)
		return
	case lobby.Status != "waiting":
		w.Header().Set("HX-Redirect", "/dashboard")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.renderer.Fragment(w, http.StatusOK, "lobby.html", "lobby-panel", map[string]any{"Lobby": lobby})
}

// ChangeRaceType lets the host pick the race; every guest must ready up again.
func (h *LobbyHandler) ChangeRaceType(w http.ResponseWriter, r *http.Request) {
	user, roomID, ok := lobbyRequest(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	raceTypeID, err := uuid.Parse(r.PostForm.Get("race_type_id"))
	if err != nil {
		http.Error(w, "Invalid race type ID", http.StatusBadRequest)
		return
	}
	if err := h.rooms.ChangeRaceType(r.Context(), user, roomID, raceTypeID); err != nil {
		writeRoomError(w, r, err)
		return
	}
	w.Header().Set("HX-Trigger", roomUpdatedEvent)
	w.WriteHeader(http.StatusNoContent)
}

// MarkUnready withdraws the caller's ready flag.
func (h *LobbyHandler) MarkUnready(w http.ResponseWriter, r *http.Request) {
	user, roomID, ok := lobbyRequest(w, r)
	if !ok {
		return
	}
	if err := h.rooms.MarkUnready(r.Context(), user, roomID); err != nil {
		writeRoomError(w, r, err)
		return
	}
	w.Header().Set("HX-Trigger", roomUpdatedEvent)
	w.WriteHeader(http.StatusNoContent)
}

// Invite invites a friend to the room and answers with the dialog's updated
// button for that friend.
func (h *LobbyHandler) Invite(w http.ResponseWriter, r *http.Request) {
	user, roomID, ok := lobbyRequest(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	friendID, err := uuid.Parse(r.PostForm.Get("user_id"))
	if err != nil {
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}
	if err := h.rooms.InviteFriend(r.Context(), user, roomID, friendID); err != nil {
		if errors.Is(err, app.ErrForbidden) {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		writeRoomError(w, r, err)
		return
	}
	h.renderer.Fragment(w, http.StatusOK, "lobby.html", "invite-sent", nil)
}

// JoinLink joins the caller to the room a shared link names and opens its lobby.
func (h *LobbyHandler) JoinLink(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	code := strings.ToUpper(strings.TrimSpace(r.PathValue("code")))
	roomID, err := h.rooms.JoinRoom(r.Context(), user.ID, code)
	if err != nil && !(errors.Is(err, app.ErrAlreadyParticipant) && roomID != uuid.Nil) {
		h.unavailable(w, r, err)
		return
	}
	http.Redirect(w, r, lobbyPath(roomID), http.StatusSeeOther)
}

// RaceCard serves the dashboard's card for creating or joining a room.
func (h *LobbyHandler) RaceCard(w http.ResponseWriter, r *http.Request) {
	raceTypes, err := h.rooms.RaceTypeOptions(r.Context())
	if err != nil {
		writeRoomError(w, r, err)
		return
	}
	h.renderer.Fragment(w, http.StatusOK, "dashboard.html", "race-card", map[string]any{"RaceTypes": raceTypes})
}

// Invites serves the caller's pending room invites for the dashboard.
func (h *LobbyHandler) Invites(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	invites, err := h.rooms.PendingInvites(r.Context(), user.ID)
	if err != nil {
		writeRoomError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.renderer.Fragment(w, http.StatusOK, "dashboard.html", "room-invites", map[string]any{"Invites": invites})
}

// AcceptInvite joins the invite's room and opens its lobby.
func (h *LobbyHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	user, inviteID, ok := lobbyRequest(w, r)
	if !ok {
		return
	}
	roomID, err := h.rooms.AcceptInvite(r.Context(), user, inviteID)
	if err != nil {
		w.Header().Set("HX-Trigger", invitesUpdatedEvent)
		writeRoomError(w, r, err)
		return
	}
	w.Header().Set("HX-Redirect", lobbyPath(roomID))
	w.WriteHeader(http.StatusNoContent)
}

// DeclineInvite discards one of the caller's invites.
func (h *LobbyHandler) DeclineInvite(w http.ResponseWriter, r *http.Request) {
	user, inviteID, ok := lobbyRequest(w, r)
	if !ok {
		return
	}
	if err := h.rooms.DeclineInvite(r.Context(), user, inviteID); err != nil && !errors.Is(err, app.ErrRoomNotFound) {
		writeRoomError(w, r, err)
		return
	}
	w.Header().Set("HX-Trigger", invitesUpdatedEvent)
	w.WriteHeader(http.StatusNoContent)
}

// invitesUpdatedEvent is the HX-Trigger event that refreshes the dashboard's invites.
const invitesUpdatedEvent = "invites-updated"

func (h *LobbyHandler) unavailable(w http.ResponseWriter, r *http.Request, err error) {
	status, message := http.StatusConflict, "This race is no longer open to join."
	switch {
	case errors.Is(err, app.ErrRoomNotFound), errors.Is(err, app.ErrParticipantNotFound):
		status, message = http.StatusNotFound, "We couldn't find that race. Check the link or code and try again."
	case errors.Is(err, app.ErrRoomFull):
		message = "That race is full."
	case errors.Is(err, app.ErrRoomNotJoinable):
	default:
		writeRoomError(w, r, err)
		return
	}
	authUser, _ := middleware.UserFromContext(r.Context())
	h.renderer.Page(w, status, "room-unavailable.html", map[string]any{"User": authUser, "Message": message})
}

// lobbyRequest reads the authenticated user and the {id} path value.
func lobbyRequest(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return uuid.Nil, uuid.Nil, false
	}
	return user.ID, id, true
}

// started reports whether a room's race is underway or over, so its
// participants belong on the race page.
func started(status string) bool {
	switch status {
	case "counting_down", "in_progress", "finished":
		return true
	}
	return false
}
