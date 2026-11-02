package handlers_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/concept2"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
)

const (
	c2TestBaseURL  = "https://log-dev.concept2.com"
	c2TestAppBase  = "https://pixelpace.example"
	c2TestClientID = "pixelpace-client"
)

// stubConcept2Service stands in for the database-backed Concept2Service:
// it holds at most one connection per user, keyed by user id, and
// SaveConnection upserts (a reconnect overwrites the user's entry).
type stubConcept2Service struct {
	conns   map[uuid.UUID]app.Concept2Connection
	grants  map[uuid.UUID]app.Concept2Grant
	saves   int
	saveErr error

	disconnects []uuid.UUID
}

func (s *stubConcept2Service) Status(_ context.Context, userID uuid.UUID) (app.Concept2Connection, error) {
	if c, ok := s.conns[userID]; ok {
		return c, nil
	}
	return app.Concept2Connection{}, app.ErrConcept2NotConnected
}

func (s *stubConcept2Service) SaveConnection(_ context.Context, userID uuid.UUID, g app.Concept2Grant) (app.Concept2Connection, error) {
	s.saves++
	if s.saveErr != nil {
		return app.Concept2Connection{}, s.saveErr
	}
	if s.conns == nil {
		s.conns = map[uuid.UUID]app.Concept2Connection{}
	}
	if s.grants == nil {
		s.grants = map[uuid.UUID]app.Concept2Grant{}
	}
	connectedAt := time.Now()
	if prev, ok := s.conns[userID]; ok {
		connectedAt = prev.ConnectedAt
	}
	c := app.Concept2Connection{
		C2UserID: g.C2UserID, Username: g.Username, ConnectedAt: connectedAt,
		AutoPost: true, Timezone: g.Timezone,
	}
	s.conns[userID] = c
	s.grants[userID] = g
	return c, nil
}

// Disconnect removes only userID's entry and records whom it was called for.
func (s *stubConcept2Service) Disconnect(_ context.Context, userID uuid.UUID) error {
	s.disconnects = append(s.disconnects, userID)
	delete(s.conns, userID)
	return nil
}

func newConcept2TestHandler(t *testing.T, svc *stubConcept2Service, secure bool) *handlers.Concept2Handler {
	t.Helper()
	client := concept2.New(c2TestBaseURL, c2TestClientID, "client-secret", c2TestAppBase+"/profile/concept2/callback")
	return handlers.NewConcept2Handler(svc, client, newProductionRenderer(t), secure)
}

func verifiedConcept2User() db.User {
	return db.User{ID: uuid.New(), Email: "rower@example.com", EmailVerified: true}
}

// CLM-1 + CLM-12: a verified user with no connection sees only the Connect form.
func TestConcept2Panel_UnconnectedShowsOnlyConnect(t *testing.T) {
	t.Parallel()
	user := verifiedConcept2User()
	other := uuid.New()
	svc := &stubConcept2Service{conns: map[uuid.UUID]app.Concept2Connection{
		other: {Username: "someone-else"},
	}}
	h := newConcept2TestHandler(t, svc, false)

	rr := serveAuthenticated(t, user, http.HandlerFunc(h.Panel), httptest.NewRequest(http.MethodGet, "/profile/concept2", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`<form method="get" action="/profile/concept2/connect" data-concept2-connect>`,
		`<input type="hidden" name="tz"`,
		`<button type="submit" class="pp-btn pp-btn-cyan">Connect Concept2</button>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("panel missing %q\nbody:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"Connected as", "Disconnect", "Reconnect", "someone-else"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("unconnected panel contains %q\nbody:\n%s", unwanted, body)
		}
	}
}

// The panel branches on state: a connected user does not get the Connect form.
func TestConcept2Panel_ConnectedShowsAccount(t *testing.T) {
	t.Parallel()
	user := verifiedConcept2User()
	svc := &stubConcept2Service{conns: map[uuid.UUID]app.Concept2Connection{
		user.ID: {Username: "erg_hero"},
	}}
	h := newConcept2TestHandler(t, svc, false)

	rr := serveAuthenticated(t, user, http.HandlerFunc(h.Panel), httptest.NewRequest(http.MethodGet, "/profile/concept2", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Connected as erg_hero") {
		t.Errorf("connected panel missing account name\nbody:\n%s", body)
	}
	if strings.Contains(body, "Connect Concept2") || strings.Contains(body, "data-concept2-connect") {
		t.Errorf("connected panel still offers Connect\nbody:\n%s", body)
	}
}

// CLM-1: Connect redirects to Concept2's authorize page with every OAuth
// parameter and a fresh state, and sets the scoped state cookie.
func TestConcept2Connect_RedirectsWithStateCookie(t *testing.T) {
	t.Parallel()
	for _, secure := range []bool{false, true} {
		h := newConcept2TestHandler(t, &stubConcept2Service{}, secure)
		user := verifiedConcept2User()

		connect := func() (state string, cookie *http.Cookie) {
			t.Helper()
			rr := serveAuthenticated(t, user, http.HandlerFunc(h.Connect),
				httptest.NewRequest(http.MethodGet, "/profile/concept2/connect?tz=Europe%2FBerlin", nil))
			if rr.Code != http.StatusFound {
				t.Fatalf("secure=%v: status = %d, want 302", secure, rr.Code)
			}
			loc, err := url.Parse(rr.Header().Get("Location"))
			if err != nil {
				t.Fatalf("parse Location: %v", err)
			}
			if got := loc.Scheme + "://" + loc.Host + loc.Path; got != c2TestBaseURL+"/oauth/authorize" {
				t.Errorf("redirect target = %q, want %q", got, c2TestBaseURL+"/oauth/authorize")
			}
			q := loc.Query()
			for key, want := range map[string]string{
				"client_id":     c2TestClientID,
				"redirect_uri":  c2TestAppBase + "/profile/concept2/callback",
				"response_type": "code",
				"scope":         "user:read,results:write",
			} {
				if got := q.Get(key); got != want {
					t.Errorf("query %s = %q, want %q", key, got, want)
				}
			}
			state = q.Get("state")
			if state == "" {
				t.Fatal("state is empty")
			}
			for _, c := range rr.Result().Cookies() {
				if c.Name == "concept2_oauth_state" {
					cookie = c
				}
			}
			if cookie == nil {
				t.Fatal("concept2_oauth_state cookie not set")
			}
			return state, cookie
		}

		state1, cookie := connect()
		if !cookie.HttpOnly {
			t.Error("cookie not HttpOnly")
		}
		if cookie.SameSite != http.SameSiteLaxMode {
			t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
		}
		if cookie.Path != "/profile/concept2" {
			t.Errorf("Path = %q, want /profile/concept2", cookie.Path)
		}
		if cookie.MaxAge <= 0 || cookie.MaxAge > 900 {
			t.Errorf("MaxAge = %d, want short and positive", cookie.MaxAge)
		}
		if cookie.Secure != secure {
			t.Errorf("Secure = %v, want %v", cookie.Secure, secure)
		}
		// The cookie carries the state it was issued for, the timezone and the
		// id of the user who started the connect.
		if want := state1 + ".RXVyb3BlL0Jlcmxpbg." + user.ID.String(); cookie.Value != want {
			t.Errorf("cookie value = %q, want %q", cookie.Value, want)
		}

		state2, _ := connect()
		if state1 == state2 {
			t.Errorf("state reused across calls: %q", state1)
		}
	}
}

// fakeLogbook stands in for the Concept2 Logbook (an external service): it
// serves the token exchange and /api/users/me the way Concept2 does, with
// configurable failures.
type fakeLogbook struct {
	srv         *httptest.Server
	code        string // the only code the exchange accepts
	access      string
	refresh     string
	c2ID        int64
	username    string
	tokenStatus int // non-zero: the exchange answers with this status
	meStatus    int // non-zero: /api/users/me answers with this status
	exchanges   int
}

func newFakeLogbook(t *testing.T) *fakeLogbook {
	t.Helper()
	f := &fakeLogbook{code: "good-code", access: "access-1", refresh: "refresh-1", c2ID: 4242, username: "erg_hero"}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		f.exchanges++
		if f.tokenStatus != 0 {
			http.Error(w, `{"error":"invalid_grant"}`, f.tokenStatus)
			return
		}
		if err := r.ParseForm(); err != nil || r.PostForm.Get("code") != f.code ||
			r.PostForm.Get("grant_type") != "authorization_code" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":604800,"scope":"user:read,results:write"}`, f.access, f.refresh)
	})
	mux.HandleFunc("GET /api/users/me", func(w http.ResponseWriter, r *http.Request) {
		if f.meStatus != 0 {
			http.Error(w, "boom", f.meStatus)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+f.access {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"id":%d,"username":%q}}`, f.c2ID, f.username)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func newConcept2CallbackHandler(t *testing.T, svc *stubConcept2Service, lb *fakeLogbook) *handlers.Concept2Handler {
	t.Helper()
	client := concept2.New(lb.srv.URL, c2TestClientID, "client-secret", c2TestAppBase+"/profile/concept2/callback")
	return handlers.NewConcept2Handler(svc, client, newProductionRenderer(t), false)
}

// startConcept2Connect runs Connect as user and returns the state sent to
// Concept2 and the state cookie the browser received.
func startConcept2Connect(t *testing.T, h *handlers.Concept2Handler, user db.User, tz string) (string, *http.Cookie) {
	t.Helper()
	rr := serveAuthenticated(t, user, http.HandlerFunc(h.Connect),
		httptest.NewRequest(http.MethodGet, "/profile/concept2/connect?tz="+url.QueryEscape(tz), nil))
	if rr.Code != http.StatusFound {
		t.Fatalf("connect status = %d, want 302", rr.Code)
	}
	loc, err := url.Parse(rr.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == "concept2_oauth_state" {
			return loc.Query().Get("state"), c
		}
	}
	t.Fatal("connect set no concept2_oauth_state cookie")
	return "", nil
}

// serveConcept2Callback serves the callback as user with query and, when
// cookieValue is non-empty, the state cookie.
func serveConcept2Callback(t *testing.T, h *handlers.Concept2Handler, user db.User, query url.Values, cookieValue string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/profile/concept2/callback?"+query.Encode(), nil)
	if cookieValue != "" {
		req.AddCookie(&http.Cookie{Name: "concept2_oauth_state", Value: cookieValue})
	}
	return serveAuthenticated(t, user, http.HandlerFunc(h.Callback), req)
}

func assertConcept2Redirect(t *testing.T, rr *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rr.Code)
	}
	if got := rr.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func assertConcept2StateCleared(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	for _, c := range rr.Result().Cookies() {
		if c.Name == "concept2_oauth_state" {
			if c.MaxAge >= 0 || c.Value != "" || c.Path != "/profile/concept2" {
				t.Errorf("state cookie not cleared: %+v", c)
			}
			return
		}
	}
	t.Error("callback did not clear the concept2_oauth_state cookie")
}

// CLM-2: a callback with a matching state exchanges the code, fetches the
// account, stores the connection under the signed-in user, clears the state
// cookie and lands on /profile?concept2=connected; the panel then shows the
// account. A reconnect overwrites the tokens and timezone and clears
// needs_reconnect.
func TestConcept2Callback_ConnectsAndReconnects(t *testing.T) {
	t.Parallel()
	user := verifiedConcept2User()
	svc := &stubConcept2Service{}
	lb := newFakeLogbook(t)
	h := newConcept2CallbackHandler(t, svc, lb)

	state, cookie := startConcept2Connect(t, h, user, "Europe/Berlin")
	before := time.Now()
	rr := serveConcept2Callback(t, h, user, url.Values{"code": {"good-code"}, "state": {state}}, cookie.Value)
	assertConcept2Redirect(t, rr, "/profile?concept2=connected")
	assertConcept2StateCleared(t, rr)

	g, ok := svc.grants[user.ID]
	if !ok || svc.saves != 1 {
		t.Fatalf("saves = %d, grant stored for user = %v; want one save for the signed-in user", svc.saves, ok)
	}
	if g.C2UserID != 4242 || g.Username != "erg_hero" || g.AccessToken != "access-1" ||
		g.RefreshToken != "refresh-1" || g.Scope != "user:read,results:write" || g.Timezone != "Europe/Berlin" {
		t.Errorf("stored grant = %+v", g)
	}
	if exp := g.ExpiresAt; exp.Before(before.Add(604800*time.Second-time.Minute)) || exp.After(time.Now().Add(604800*time.Second)) {
		t.Errorf("ExpiresAt = %v, want about a week from now", exp)
	}

	panel := serveAuthenticated(t, user, http.HandlerFunc(h.Panel), httptest.NewRequest(http.MethodGet, "/profile/concept2", nil))
	body := panel.Body.String()
	if !strings.Contains(body, "Connected as erg_hero") {
		t.Errorf("panel missing 'Connected as erg_hero'\nbody:\n%s", body)
	}
	if want := "Since " + svc.conns[user.ID].ConnectedAt.Format("Jan 2, 2006"); !strings.Contains(body, want) {
		t.Errorf("panel missing connected date %q\nbody:\n%s", want, body)
	}

	// Reconnect: the stored connection had been flagged for reconnect; new
	// tokens and a new timezone overwrite it.
	c := svc.conns[user.ID]
	c.NeedsReconnect = true
	svc.conns[user.ID] = c
	lb.access, lb.refresh, lb.code = "access-2", "refresh-2", "second-code"
	state2, cookie2 := startConcept2Connect(t, h, user, "America/New_York")
	rr = serveConcept2Callback(t, h, user, url.Values{"code": {"second-code"}, "state": {state2}}, cookie2.Value)
	assertConcept2Redirect(t, rr, "/profile?concept2=connected")
	assertConcept2StateCleared(t, rr)
	g = svc.grants[user.ID]
	if g.AccessToken != "access-2" || g.RefreshToken != "refresh-2" || g.Timezone != "America/New_York" {
		t.Errorf("reconnect grant = %+v, want the new tokens and timezone", g)
	}
	if got := svc.conns[user.ID]; got.NeedsReconnect || got.Timezone != "America/New_York" || len(svc.conns) != 1 {
		t.Errorf("after reconnect: conn = %+v (of %d), want one row, needs_reconnect=false", got, len(svc.conns))
	}
}

// CLM-6: every failed callback redirects to /profile?concept2=error, clears
// the state cookie, and leaves the user's existing connection untouched.
func TestConcept2Callback_FailuresStoreNothing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		query  func(state string) url.Values
		cookie func(valid string, user db.User) string
		setup  func(lb *fakeLogbook, svc *stubConcept2Service)
	}{
		{name: "access denied", query: func(s string) url.Values { return url.Values{"error": {"access_denied"}, "state": {s}} }},
		{name: "error param with code", query: func(s string) url.Values {
			return url.Values{"error": {"server_error"}, "code": {"good-code"}, "state": {s}}
		}},
		{name: "missing code", query: func(s string) url.Values { return url.Values{"state": {s}} }},
		{name: "missing state", query: func(string) url.Values { return url.Values{"code": {"good-code"}} }},
		{name: "missing cookie", cookie: func(string, db.User) string { return "" }},
		{name: "state mismatch", query: func(string) url.Values { return url.Values{"code": {"good-code"}, "state": {"forged"}} }},
		{name: "malformed cookie", cookie: func(string, db.User) string { return "not-a-state-cookie" }},
		{name: "cookie with bad timezone encoding", cookie: func(v string, u db.User) string {
			return strings.SplitN(v, ".", 2)[0] + ".!!!." + u.ID.String()
		}},
		{name: "cookie with bad user id", cookie: func(v string, _ db.User) string {
			parts := strings.Split(v, ".")
			return parts[0] + "." + parts[1] + ".not-a-uuid"
		}},
		{name: "cookie with extra part", cookie: func(v string, _ db.User) string { return v + ".extra" }},
		{name: "cookie with empty state", query: func(string) url.Values {
			return url.Values{"code": {"good-code"}, "state": {""}}
		}, cookie: func(v string, _ db.User) string { return v[strings.Index(v, "."):] }},
		{name: "exchange 400", setup: func(lb *fakeLogbook, _ *stubConcept2Service) { lb.tokenStatus = http.StatusBadRequest }},
		{name: "exchange 500", setup: func(lb *fakeLogbook, _ *stubConcept2Service) { lb.tokenStatus = http.StatusInternalServerError }},
		{name: "users/me 500", setup: func(lb *fakeLogbook, _ *stubConcept2Service) { lb.meStatus = http.StatusInternalServerError }},
		{name: "store fails", setup: func(_ *fakeLogbook, svc *stubConcept2Service) { svc.saveErr = errors.New("db down") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			user := verifiedConcept2User()
			existing := app.Concept2Connection{C2UserID: 7, Username: "old_account", Timezone: "UTC", NeedsReconnect: true}
			svc := &stubConcept2Service{conns: map[uuid.UUID]app.Concept2Connection{user.ID: existing}}
			lb := newFakeLogbook(t)
			if tc.setup != nil {
				tc.setup(lb, svc)
			}
			h := newConcept2CallbackHandler(t, svc, lb)
			state, cookie := startConcept2Connect(t, h, user, "Europe/Berlin")
			query := url.Values{"code": {"good-code"}, "state": {state}}
			if tc.query != nil {
				query = tc.query(state)
			}
			cookieValue := cookie.Value
			if tc.cookie != nil {
				cookieValue = tc.cookie(cookie.Value, user)
			}

			rr := serveConcept2Callback(t, h, user, query, cookieValue)
			assertConcept2Redirect(t, rr, "/profile?concept2=error")
			assertConcept2StateCleared(t, rr)
			if _, stored := svc.grants[user.ID]; stored {
				t.Errorf("a connection was stored: %+v", svc.grants[user.ID])
			}
			if got := svc.conns[user.ID]; got != existing || len(svc.conns) != 1 {
				t.Errorf("existing connection changed: %+v (of %d), want %+v", got, len(svc.conns), existing)
			}
		})
	}
}

// CLM-7: the state is bound to the user who started the connect. A callback
// served to a different signed-in user is a bad state, nothing is stored for
// either user, and the code is never exchanged.
func TestConcept2Callback_RejectsCrossUserState(t *testing.T) {
	t.Parallel()
	for _, variant := range []string{"B presents A's cookie and state", "B presents own cookie with A's state"} {
		t.Run(variant, func(t *testing.T) {
			t.Parallel()
			a, b := verifiedConcept2User(), verifiedConcept2User()
			svc := &stubConcept2Service{}
			lb := newFakeLogbook(t)
			h := newConcept2CallbackHandler(t, svc, lb)

			stateA, cookieA := startConcept2Connect(t, h, a, "Europe/Berlin")
			cookieValue := cookieA.Value
			if strings.HasPrefix(variant, "B presents own") {
				_, cookieB := startConcept2Connect(t, h, b, "UTC")
				cookieValue = cookieB.Value
			}
			rr := serveConcept2Callback(t, h, b, url.Values{"code": {"good-code"}, "state": {stateA}}, cookieValue)
			assertConcept2Redirect(t, rr, "/profile?concept2=error")
			assertConcept2StateCleared(t, rr)
			if svc.saves != 0 || len(svc.conns) != 0 {
				t.Errorf("stored %d connections (%d saves), want none for A or B", len(svc.conns), svc.saves)
			}
			if lb.exchanges != 0 {
				t.Errorf("code exchanged %d times, want 0", lb.exchanges)
			}
		})
	}
}

// CLM-13: the connected panel offers Disconnect as a pp-btn pp-btn-red that
// asks for confirmation and posts to /profile/concept2/disconnect; the
// disconnect response removes only the signed-in user's connection and
// renders the panel in its Connect state.
func TestConcept2Disconnect_RendersConnectState(t *testing.T) {
	t.Parallel()
	user := verifiedConcept2User()
	other := uuid.New()
	svc := &stubConcept2Service{conns: map[uuid.UUID]app.Concept2Connection{
		user.ID: {Username: "erg_hero"},
		other:   {Username: "someone-else"},
	}}
	h := newConcept2TestHandler(t, svc, false)

	panel := serveAuthenticated(t, user, http.HandlerFunc(h.Panel), httptest.NewRequest(http.MethodGet, "/profile/concept2", nil))
	body := panel.Body.String()
	for _, want := range []string{
		`<div id="concept2-panel"`,
		`class="pp-btn pp-btn-red"`,
		`hx-post="/profile/concept2/disconnect"`,
		`hx-target="#concept2-panel"`,
		`hx-swap="outerHTML"`,
		`hx-confirm="`,
		`>Disconnect</button>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("connected panel missing %q\nbody:\n%s", want, body)
		}
	}

	for i := 0; i < 2; i++ { // the second disconnect is a no-op
		rr := serveAuthenticated(t, user, http.HandlerFunc(h.Disconnect),
			httptest.NewRequest(http.MethodPost, "/profile/concept2/disconnect", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("disconnect #%d status = %d, want 200", i+1, rr.Code)
		}
		got := rr.Body.String()
		if !strings.Contains(got, `<div id="concept2-panel"`) || !strings.Contains(got, `data-concept2-connect`) ||
			!strings.Contains(got, "Connect Concept2") {
			t.Errorf("disconnect #%d did not render the Connect state\nbody:\n%s", i+1, got)
		}
		if strings.Contains(got, "Connected as") || strings.Contains(got, ">Disconnect<") {
			t.Errorf("disconnect #%d still shows the connected state\nbody:\n%s", i+1, got)
		}
	}
	if len(svc.disconnects) != 2 || svc.disconnects[0] != user.ID || svc.disconnects[1] != user.ID {
		t.Errorf("Disconnect called for %v, want the signed-in user twice", svc.disconnects)
	}
	if _, ok := svc.conns[user.ID]; ok {
		t.Error("the user's connection is still stored")
	}
	if svc.conns[other].Username != "someone-else" {
		t.Error("another user's connection was removed")
	}
}

// CLM-15: the timezone captured at Connect travels in the state cookie and is
// stored at the callback; a value that is invalid, "Local", longer than 64
// bytes, blank or missing is stored as UTC.
func TestConcept2Connect_TimezoneCapture(t *testing.T) {
	t.Parallel()
	long := "Europe/" + strings.Repeat("A", 58) // 65 bytes
	cases := []struct{ name, tz, want string }{
		{"valid", "Europe/Berlin", "Europe/Berlin"},
		{"valid with spaces", "  America/New_York ", "America/New_York"},
		{"unknown zone", "Not/AZone", "UTC"},
		{"Local", "Local", "UTC"},
		{"65 bytes", long, "UTC"},
		{"blank", "   ", "UTC"},
		{"missing", "", "UTC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.name == "65 bytes" && len(tc.tz) != 65 {
				t.Fatalf("test input is %d bytes, want 65", len(tc.tz))
			}
			user := verifiedConcept2User()
			svc := &stubConcept2Service{}
			lb := newFakeLogbook(t)
			h := newConcept2CallbackHandler(t, svc, lb)
			path := "/profile/concept2/connect"
			if tc.name != "missing" {
				path += "?tz=" + url.QueryEscape(tc.tz)
			}
			rr := serveAuthenticated(t, user, http.HandlerFunc(h.Connect), httptest.NewRequest(http.MethodGet, path, nil))
			loc, err := url.Parse(rr.Header().Get("Location"))
			if err != nil {
				t.Fatalf("parse Location: %v", err)
			}
			var cookie *http.Cookie
			for _, c := range rr.Result().Cookies() {
				if c.Name == "concept2_oauth_state" {
					cookie = c
				}
			}
			if cookie == nil {
				t.Fatal("no state cookie")
			}
			cb := serveConcept2Callback(t, h, user, url.Values{"code": {"good-code"}, "state": {loc.Query().Get("state")}}, cookie.Value)
			assertConcept2Redirect(t, cb, "/profile?concept2=connected")
			if got := svc.grants[user.ID].Timezone; got != tc.want {
				t.Errorf("stored timezone = %q, want %q", got, tc.want)
			}
		})
	}
}

// CLM-15: the callback validates the cookie's timezone again, so a cookie
// edited in the browser to carry a bad zone still stores UTC.
func TestConcept2Callback_RevalidatesCookieTimezone(t *testing.T) {
	t.Parallel()
	user := verifiedConcept2User()
	svc := &stubConcept2Service{}
	lb := newFakeLogbook(t)
	h := newConcept2CallbackHandler(t, svc, lb)
	state, cookie := startConcept2Connect(t, h, user, "Europe/Berlin")
	parts := strings.Split(cookie.Value, ".")
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte("Local"))
	rr := serveConcept2Callback(t, h, user, url.Values{"code": {"good-code"}, "state": {state}}, strings.Join(parts, "."))
	assertConcept2Redirect(t, rr, "/profile?concept2=connected")
	if got := svc.grants[user.ID].Timezone; got != "UTC" {
		t.Errorf("stored timezone = %q, want UTC", got)
	}
}
