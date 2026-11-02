package handlers

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/concept2"
	"github.com/softsrv/starter/internal/http/middleware"
)

// concept2Servicer is the subset of app.Concept2Service the Concept2Handler
// needs. Every call is keyed by the signed-in user's id.
type concept2Servicer interface {
	Status(ctx context.Context, userID uuid.UUID) (app.Concept2Connection, error)
	SaveConnection(ctx context.Context, userID uuid.UUID, g app.Concept2Grant) (app.Concept2Connection, error)
	Disconnect(ctx context.Context, userID uuid.UUID) error
}

// concept2OAuthClient is the OAuth half of the Concept2 Logbook client the
// handler needs; *concept2.Client satisfies it.
type concept2OAuthClient interface {
	AuthorizeURL(state string) string
	ExchangeCodeContext(ctx context.Context, code string) (*concept2.Token, error)
	GetMeContext(ctx context.Context, tokens ...string) (*concept2.User, error)
}

// maxConcept2TZLen caps the browser-supplied timezone carried in the state
// cookie; IANA names are far shorter.
const maxConcept2TZLen = 64

// sanitizeConcept2TZ returns the browser-supplied timezone as a loadable IANA
// name, or "UTC" when it is empty, longer than maxConcept2TZLen bytes,
// "Local" (the server's zone, not the user's) or unknown to time.LoadLocation.
func sanitizeConcept2TZ(raw string) string {
	tz := strings.TrimSpace(raw)
	if tz == "" || tz == "Local" || len(tz) > maxConcept2TZLen {
		return "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "UTC"
	}
	return tz
}

// Concept2Handler serves the profile's Concept2 Logbook panel and the OAuth
// connect flow.
type Concept2Handler struct {
	svc      concept2Servicer
	client   concept2OAuthClient
	renderer *TemplateRenderer
	secure   bool
}

// NewConcept2Handler constructs a Concept2Handler.
func NewConcept2Handler(svc concept2Servicer, client concept2OAuthClient, renderer *TemplateRenderer, secure bool) *Concept2Handler {
	return &Concept2Handler{svc: svc, client: client, renderer: renderer, secure: secure}
}

// Panel renders the Concept2 panel for the signed-in user: the Connect state
// when they have no connection, otherwise the connected account.
func (h *Concept2Handler) Panel(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	data := map[string]any{"Connected": false}
	conn, err := h.svc.Status(r.Context(), user.ID)
	switch {
	case err == nil:
		data["Connected"] = true
		data["Connection"] = conn
	case errors.Is(err, app.ErrConcept2NotConnected):
	default:
		slog.ErrorContext(r.Context(), "concept2 status", "user_id", user.ID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	h.renderer.Partial(w, http.StatusOK, "partials/concept2-panel.html", data)
}

// Connect starts the OAuth flow: it generates a fresh random state, stores it
// (with the browser's timezone) in the concept2_oauth_state cookie and
// redirects to Concept2's authorization page.
func (h *Concept2Handler) Connect(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	state, err := newConcept2State()
	if err != nil {
		slog.ErrorContext(r.Context(), "concept2 state", "user_id", user.ID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	tz := sanitizeConcept2TZ(r.URL.Query().Get("tz"))
	setConcept2StateCookie(w, state, tz, user.ID, h.secure)
	http.Redirect(w, r, h.client.AuthorizeURL(state), http.StatusFound)
}

// Concept2 callback outcomes, carried to the profile page as ?concept2=.
const (
	concept2Connected = "/profile?concept2=connected"
	concept2Failed    = "/profile?concept2=error"
)

// Callback finishes the OAuth flow. It accepts only a callback whose state
// matches the concept2_oauth_state cookie and whose cookie was issued to the
// signed-in user; it then exchanges the code, looks up the Logbook account
// and stores the connection under the signed-in user's id. The state cookie
// is cleared on every outcome, and any failure stores nothing and redirects
// to /profile?concept2=error.
func (h *Concept2Handler) Callback(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	clearConcept2StateCookie(w, h.secure)
	fail := func(reason string, err error) {
		slog.WarnContext(r.Context(), "concept2 callback failed", "user_id", user.ID, "reason", reason, "error", err)
		http.Redirect(w, r, concept2Failed, http.StatusFound)
	}

	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		fail("authorization refused", errors.New(e))
		return
	}
	code := q.Get("code")
	if code == "" {
		fail("missing code", nil)
		return
	}
	cookie, ok := readConcept2StateCookie(r)
	if !ok {
		fail("missing or malformed state cookie", nil)
		return
	}
	// The state must be the one this browser was issued, and it must have
	// been issued to the user now signed in.
	if subtle.ConstantTimeCompare([]byte(cookie.State), []byte(q.Get("state"))) != 1 || cookie.UserID != user.ID {
		fail("state mismatch", nil)
		return
	}

	tok, err := h.client.ExchangeCodeContext(r.Context(), code)
	if err != nil {
		fail("code exchange", err)
		return
	}
	me, err := h.client.GetMeContext(r.Context(), tok.AccessToken)
	if err != nil {
		fail("users/me lookup", err)
		return
	}
	// Always the context user's id: never one taken from the query or cookie.
	if _, err := h.svc.SaveConnection(r.Context(), user.ID, app.Concept2Grant{
		C2UserID:     me.ID,
		Username:     me.Username,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    tok.ExpiresAt,
		Scope:        tok.Scope,
		// The cookie is browser-held: validate its timezone again before saving.
		Timezone: sanitizeConcept2TZ(cookie.TZ),
	}); err != nil {
		slog.ErrorContext(r.Context(), "concept2 save connection", "user_id", user.ID, "error", err)
		http.Redirect(w, r, concept2Failed, http.StatusFound)
		return
	}
	http.Redirect(w, r, concept2Connected, http.StatusFound)
}

// Disconnect deletes the signed-in user's Concept2 connection and returns the
// panel in its Connect state. It makes no request to the Concept2 Logbook, so
// results already posted there stay. Disconnecting again is a no-op.
func (h *Concept2Handler) Disconnect(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := h.svc.Disconnect(r.Context(), user.ID); err != nil {
		slog.ErrorContext(r.Context(), "concept2 disconnect", "user_id", user.ID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	h.renderer.Partial(w, http.StatusOK, "partials/concept2-panel.html", map[string]any{"Connected": false})
}

// newConcept2State returns 32 random bytes, base64url-encoded.
func newConcept2State() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
