package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	internalhttp "github.com/softsrv/starter/internal/http"
	"github.com/softsrv/starter/internal/http/handlers"
	"github.com/softsrv/starter/internal/http/middleware"
	"github.com/softsrv/starter/internal/realtime"
)

type telemetryIngestFunc func(context.Context, uuid.UUID, uuid.UUID, app.TelemetrySample) error

func (f telemetryIngestFunc) Ingest(ctx context.Context, race, user uuid.UUID, sample app.TelemetrySample) error {
	return f(ctx, race, user, sample)
}

// participationFunc decides participation; its roster is empty, for tests
// about the transport rather than who is in the race.
type participationFunc func(context.Context, uuid.UUID, uuid.UUID) (bool, error)

func (f participationFunc) IsParticipant(ctx context.Context, race, user uuid.UUID) (bool, error) {
	return f(ctx, race, user)
}

func (f participationFunc) Roster(context.Context, uuid.UUID) ([]app.PublicPlayer, error) {
	return []app.PublicPlayer{}, nil
}

// allowParticipants treats every caller as a participant, for tests about the
// transport rather than the participation gate.
var allowParticipants = participationFunc(func(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return true, nil })

// rosterSource admits every caller and serves a fixed roster.
type rosterSource struct {
	players []app.PublicPlayer
	err     error
}

func (rosterSource) IsParticipant(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}

func (s rosterSource) Roster(context.Context, uuid.UUID) ([]app.PublicPlayer, error) {
	return s.players, s.err
}

// participantDB answers the real RaceService's IsRaceParticipant query, and
// its ListRaceRoster query with no rows.
type participantDB struct {
	db.DBTX
	member bool
}

func (d participantDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	return participantRow{ok: strings.HasPrefix(sql, "-- name: IsRaceParticipant "), member: d.member}
}

func (d participantDB) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	if !strings.HasPrefix(sql, "-- name: ListRaceRoster ") {
		return nil, errors.New("unexpected query")
	}
	return emptyRows{}, nil
}

type emptyRows struct{ pgx.Rows }

func (emptyRows) Next() bool { return false }
func (emptyRows) Close()     {}
func (emptyRows) Err() error { return nil }

type participantRow struct{ ok, member bool }

func (r participantRow) Scan(dest ...any) error {
	if !r.ok || len(dest) != 1 {
		return errors.New("unexpected query")
	}
	*dest[0].(*bool) = r.member
	return nil
}

type wsSubscription struct {
	ctx  context.Context
	race string
}

// Observe readiness and cancellation without sleeps or access to handler internals.
type observedBroadcaster struct {
	realtime.Broadcaster
	subscribed chan wsSubscription
}

func (b *observedBroadcaster) Subscribe(ctx context.Context, race string) (<-chan []byte, error) {
	sub, err := b.Broadcaster.Subscribe(ctx, race)
	b.subscribed <- wsSubscription{ctx: ctx, race: race}
	return sub, err
}

func newObservedBroadcaster() *observedBroadcaster {
	return &observedBroadcaster{Broadcaster: realtime.NewFake(), subscribed: make(chan wsSubscription, 16)}
}

func wsContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func wsUser() db.User {
	return db.User{ID: uuid.Must(uuid.NewV7()), Email: "racer@example.com"}
}

func wsHeaders(t *testing.T, user db.User) http.Header {
	t.Helper()
	token, err := auth.IssueAccessToken(user.ID, user.Email, testJWTSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return http.Header{"Authorization": {"Bearer " + token.AccessToken}, "HX-Request": {"true"}}
}

func wsServer(t *testing.T, user db.User, h *handlers.RaceWSHandler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	authMW := middleware.Authenticate(&handlerUserFetcher{user: user}, testJWTSecret)
	mux.Handle("GET /races/{id}/ws", authMW(http.HandlerFunc(h.Serve)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func wsDial(t *testing.T, ctx context.Context, url string, headers http.Header) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func wsWaitSubscription(t *testing.T, ctx context.Context, b *observedBroadcaster, race uuid.UUID) wsSubscription {
	t.Helper()
	select {
	case sub := <-b.subscribed:
		if sub.race != race.String() {
			t.Fatalf("subscription race = %q, want %s", sub.race, race)
		}
		return sub
	case <-ctx.Done():
		t.Fatal("subscription not established")
		return wsSubscription{}
	}
}

func wsRead(t *testing.T, ctx context.Context, conn *websocket.Conn, want []byte) {
	t.Helper()
	_, got, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

// rosterMessage is the first frame on every race connection.
type rosterMessage struct {
	Type    string             `json:"type"`
	Players []app.PublicPlayer `json:"players"`
}

// wsReadRoster reads the connection's first frame, which must be the roster.
func wsReadRoster(t *testing.T, ctx context.Context, conn *websocket.Conn) rosterMessage {
	t.Helper()
	_, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got rosterMessage
	if err := json.Unmarshal(payload, &got); err != nil || got.Type != "roster" {
		t.Fatalf("first frame = %q, want a roster (%v)", payload, err)
	}
	return got
}

// CLM-5: the roster frame is the first frame, ahead of telemetry that is
// already waiting the moment the subscription exists.
func TestRaceWSRosterPrecedesTelemetry(t *testing.T) {
	ctx := wsContext(t)
	user, race := wsUser(), uuid.Must(uuid.NewV7())
	players := []app.PublicPlayer{
		{UserID: user.ID, Name: "RowerOne"},
		app.NewPublicPlayer(uuid.Must(uuid.NewV7()), pgtype.Text{}),
	}
	telemetry := []byte(`{"user_id":"` + user.ID.String() + `","power":180}`)
	b := &controlledBroadcaster{ch: make(chan []byte, 1)}
	b.ch <- telemetry // Published before the handler could write anything.
	srv := wsServer(t, user, handlers.NewRaceWSHandler(nil, rosterSource{players: players}, b))
	conn := wsDial(t, ctx, srv.URL+"/races/"+race.String()+"/ws", wsHeaders(t, user))
	if got := wsReadRoster(t, ctx, conn); !reflect.DeepEqual(got.Players, players) {
		t.Fatalf("roster players = %+v, want %+v", got.Players, players)
	}
	wsRead(t, ctx, conn, telemetry)

	// A roster that cannot be read closes the connection without telemetry.
	b = &controlledBroadcaster{ch: make(chan []byte, 1)}
	b.ch <- telemetry
	srv = wsServer(t, user, handlers.NewRaceWSHandler(nil, rosterSource{err: errors.New("unavailable")}, b))
	conn = wsDial(t, ctx, srv.URL+"/races/"+race.String()+"/ws", wsHeaders(t, user))
	if _, payload, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusInternalError {
		t.Fatalf("roster failure: payload=%q error=%v, want internal-error close", payload, err)
	}
}

func TestRaceWSRouterAuthentication(t *testing.T) {
	ctx := wsContext(t)
	user, race := wsUser(), uuid.Must(uuid.NewV7())
	b := newObservedBroadcaster()
	srv := httptest.NewServer(internalhttp.NewRouter(ctx, internalhttp.RouterConfig{
		Queries: &handlerUserFetcher{user: user}, JWTSecret: testJWTSecret,
		RaceSvc: app.NewRaceService(db.New(participantDB{member: true}), nil, b, app.RaceServiceConfig{}), Broadcaster: b,
	}))
	t.Cleanup(srv.Close)
	url := srv.URL + "/races/" + race.String() + "/ws"
	for _, headers := range []http.Header{
		{"HX-Request": {"true"}},
		{"HX-Request": {"true"}, "Authorization": {"Bearer invalid"}},
		{"HX-Request": {"true"}, "Cookie": {"access_token=invalid"}},
	} {
		conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
		if conn != nil {
			_ = conn.CloseNow()
		}
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("HX-Redirect") != "/login" {
			t.Fatalf("unauthenticated upgrade: response=%v error=%v", resp, err)
		}
	}
	for _, mode := range []string{"bearer", "cookie"} {
		headers := wsHeaders(t, user)
		if mode == "cookie" {
			token, err := auth.IssueAccessToken(user.ID, user.Email, testJWTSecret, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			headers.Del("Authorization")
			headers.Set("Cookie", "access_token="+token.AccessToken)
		}
		// Separate race keys allow both authenticated sockets to remain live.
		raceID := uuid.Must(uuid.NewV7())
		conn := wsDial(t, ctx, srv.URL+"/races/"+raceID.String()+"/ws", headers)
		wsWaitSubscription(t, ctx, b, raceID)
		wsReadRoster(t, ctx, conn)
		payload := []byte(`{"power":123}`)
		if err := b.Publish(ctx, raceID.String(), payload); err != nil {
			t.Fatal(err)
		}
		wsRead(t, ctx, conn, payload)
	}
}

func TestRaceWSAuthenticatedIdentityAndDecode(t *testing.T) {
	ctx := wsContext(t)
	user, race := wsUser(), uuid.Must(uuid.NewV7())
	type call struct {
		race, user, contextUser uuid.UUID
		sample                  app.TelemetrySample
	}
	calls := make(chan call, 4)
	ingest := telemetryIngestFunc(func(ctx context.Context, raceID, userID uuid.UUID, sample app.TelemetrySample) error {
		resolved, _ := middleware.UserFromContext(ctx)
		calls <- call{raceID, userID, resolved.ID, sample}
		return nil
	})
	srv := wsServer(t, user, handlers.NewRaceWSHandler(ingest, allowParticipants, realtime.NewFake()))
	conn := wsDial(t, ctx, srv.URL+"/races/"+race.String()+"/ws", wsHeaders(t, user))
	if err := conn.Write(ctx, websocket.MessageText, []byte(`not json`)); err != nil {
		t.Fatal(err)
	}
	sample := app.TelemetrySample{ElapsedMilliseconds: 1000, DistanceMillimeters: 2500, StrokeRate: 28, Power: 170, SampledAt: time.Now().UTC()}
	payload, err := json.Marshal(struct {
		UserID uuid.UUID `json:"user_id"`
		RaceID uuid.UUID `json:"race_id"`
		app.TelemetrySample
	}{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), sample})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []websocket.MessageType{websocket.MessageText, websocket.MessageBinary} {
		if err := conn.Write(ctx, kind, payload); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-calls:
			if got.race != race || got.user != user.ID || got.contextUser != user.ID || !reflect.DeepEqual(got.sample, sample) {
				t.Fatalf("ingest = %+v, want authenticated user %s, race %s, sample %+v", got, user.ID, race, sample)
			}
		case <-ctx.Done():
			t.Fatal("no ingest")
		}
	}
}

func TestRaceWSDuplicateRejectedFirstRemainsLive(t *testing.T) {
	ctx := wsContext(t)
	user, race := wsUser(), uuid.Must(uuid.NewV7())
	b := newObservedBroadcaster()
	ingest := telemetryIngestFunc(func(context.Context, uuid.UUID, uuid.UUID, app.TelemetrySample) error { return nil })
	srv := wsServer(t, user, handlers.NewRaceWSHandler(ingest, allowParticipants, b))
	url, headers := srv.URL+"/races/"+race.String()+"/ws", wsHeaders(t, user)
	first := wsDial(t, ctx, url, headers)
	sub := wsWaitSubscription(t, ctx, b, race)
	wsReadRoster(t, ctx, first)
	second, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
	if second != nil {
		_ = second.CloseNow()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate upgrade: response=%v error=%v", resp, err)
	}
	payload := []byte(`{"power":200}`)
	if err := b.Publish(ctx, race.String(), payload); err != nil {
		t.Fatal(err)
	}
	wsRead(t, ctx, first, payload)
	_ = first.CloseNow()
	select {
	case <-sub.ctx.Done():
	case <-ctx.Done():
		t.Fatal("disconnect did not cancel subscription")
	}
	// Cancellation precedes slot release; retry only a conflict until it is released.
	for {
		conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
		if err == nil {
			_ = conn.CloseNow()
			break
		}
		if ctx.Err() != nil || resp == nil || resp.StatusCode != http.StatusConflict {
			t.Fatalf("reconnect: response=%v error=%v", resp, err)
		}
	}
}

func TestRaceWSRelayBetweenRacersAndRaceIsolation(t *testing.T) {
	ctx := wsContext(t)
	b := newObservedBroadcaster()
	ingest := telemetryIngestFunc(func(ctx context.Context, raceID, userID uuid.UUID, sample app.TelemetrySample) error {
		payload, err := json.Marshal(struct {
			UserID uuid.UUID `json:"user_id"`
			app.TelemetrySample
		}{userID, sample})
		if err != nil {
			return err
		}
		return b.Publish(ctx, raceID.String(), payload)
	})
	h := handlers.NewRaceWSHandler(ingest, allowParticipants, b)
	userA, userB := wsUser(), wsUser()
	srvA, srvB := wsServer(t, userA, h), wsServer(t, userB, h)
	race, otherRace := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	a := wsDial(t, ctx, srvA.URL+"/races/"+race.String()+"/ws", wsHeaders(t, userA))
	wsWaitSubscription(t, ctx, b, race)
	peer := wsDial(t, ctx, srvB.URL+"/races/"+race.String()+"/ws", wsHeaders(t, userB))
	wsWaitSubscription(t, ctx, b, race)
	other := wsDial(t, ctx, srvA.URL+"/races/"+otherRace.String()+"/ws", wsHeaders(t, userA))
	wsWaitSubscription(t, ctx, b, otherRace)
	for _, conn := range []*websocket.Conn{a, peer, other} {
		wsReadRoster(t, ctx, conn)
	}
	if err := a.Write(ctx, websocket.MessageText, []byte(`{"power":175}`)); err != nil {
		t.Fatal(err)
	}
	_, payload, err := peer.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		UserID uuid.UUID `json:"user_id"`
		app.TelemetrySample
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.UserID != userA.ID || got.Power != 175 {
		t.Fatalf("peer received %+v", got)
	}
	wsRead(t, ctx, a, payload)
	marker := []byte(`{"other_race":true}`)
	if err := b.Publish(ctx, otherRace.String(), marker); err != nil {
		t.Fatal(err)
	}
	// The next payload must be this race's marker, not racer A's sample.
	wsRead(t, ctx, other, marker)
}

func TestRaceWSValidationAndFailedUpgradeRelease(t *testing.T) {
	h := handlers.NewRaceWSHandler(nil, allowParticipants, realtime.NewFake())
	rr := httptest.NewRecorder()
	h.Serve(rr, httptest.NewRequest(http.MethodGet, "/races/invalid/ws", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing context: status %d", rr.Code)
	}
	ctx := wsContext(t)
	user := wsUser()
	srv := wsServer(t, user, h)
	headers := wsHeaders(t, user)
	conn, resp, err := websocket.Dial(ctx, srv.URL+"/races/invalid/ws", &websocket.DialOptions{HTTPHeader: headers})
	if conn != nil {
		_ = conn.CloseNow()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid race: response=%v error=%v", resp, err)
	}
	url := srv.URL + "/races/" + uuid.Must(uuid.NewV7()).String() + "/ws"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = headers
	resp, err = srv.Client().Do(req) // Not an upgrade: the reserved slot must be released.
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusSwitchingProtocols {
		t.Fatal("ordinary HTTP request upgraded")
	}
	wsDial(t, ctx, url, headers)
}

type controlledBroadcaster struct {
	ch  chan []byte
	err error
}

func (b *controlledBroadcaster) Publish(context.Context, string, []byte) error { return nil }
func (b *controlledBroadcaster) Subscribe(context.Context, string) (<-chan []byte, error) {
	return b.ch, b.err
}

func TestRaceWSPumpFailuresCloseConnection(t *testing.T) {
	for _, mode := range []string{"subscribe error", "channel closed", "ingest error"} {
		t.Run(mode, func(t *testing.T) {
			ctx := wsContext(t)
			b := &controlledBroadcaster{ch: make(chan []byte)}
			if mode == "subscribe error" {
				b.err = errors.New("unavailable")
			}
			if mode == "channel closed" {
				close(b.ch)
			}
			ingest := telemetryIngestFunc(func(context.Context, uuid.UUID, uuid.UUID, app.TelemetrySample) error {
				return errors.New("unavailable")
			})
			user := wsUser()
			srv := wsServer(t, user, handlers.NewRaceWSHandler(ingest, allowParticipants, b))
			conn := wsDial(t, ctx, srv.URL+"/races/"+uuid.Must(uuid.NewV7()).String()+"/ws", wsHeaders(t, user))
			if mode != "subscribe error" {
				wsReadRoster(t, ctx, conn)
			}
			if mode == "ingest error" {
				if err := conn.Write(ctx, websocket.MessageText, []byte(`{}`)); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := conn.Read(ctx); err == nil || ctx.Err() != nil {
				t.Fatalf("connection not promptly closed: %v", err)
			}
		})
	}
}

// CLM-6: only a race's participants may open its live channel. Everyone else is
// refused before the upgrade, with no subscription (so no frames) and no Ingest.
func TestRaceWSNonParticipantRefusedBeforeUpgrade(t *testing.T) {
	ctx := wsContext(t)
	user, race := wsUser(), uuid.Must(uuid.NewV7())
	var ingested atomic.Int32
	ingest := telemetryIngestFunc(func(context.Context, uuid.UUID, uuid.UUID, app.TelemetrySample) error {
		ingested.Add(1)
		return nil
	})
	for _, tc := range []struct {
		name   string
		member bool
		err    error
		status int
	}{
		{"non-participant", false, nil, http.StatusForbidden},
		{"check failure", false, errors.New("database unavailable"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var checked atomic.Int32
			check := participationFunc(func(_ context.Context, gotRace, gotUser uuid.UUID) (bool, error) {
				checked.Add(1)
				if gotRace != race || gotUser != user.ID {
					t.Errorf("check(%s, %s), want (%s, %s)", gotRace, gotUser, race, user.ID)
				}
				return tc.member, tc.err
			})
			b := newObservedBroadcaster()
			srv := wsServer(t, user, handlers.NewRaceWSHandler(ingest, check, b))
			conn, resp, err := websocket.Dial(ctx, srv.URL+"/races/"+race.String()+"/ws", &websocket.DialOptions{HTTPHeader: wsHeaders(t, user)})
			if conn != nil {
				_ = conn.CloseNow()
				t.Fatal("refused caller was upgraded")
			}
			if err == nil || resp == nil || resp.StatusCode != tc.status {
				t.Fatalf("refused upgrade: response=%v error=%v, want %d", resp, err, tc.status)
			}
			if checked.Load() != 1 {
				t.Fatalf("participation checks = %d, want 1", checked.Load())
			}
			select {
			case sub := <-b.subscribed:
				t.Fatalf("refused caller subscribed to %s", sub.race)
			default:
			}
		})
	}
	if ingested.Load() != 0 {
		t.Fatalf("refused callers ingested %d samples", ingested.Load())
	}
	// The same caller, once a participant, proceeds through the upgrade.
	b := newObservedBroadcaster()
	srv := wsServer(t, user, handlers.NewRaceWSHandler(ingest, allowParticipants, b))
	conn := wsDial(t, ctx, srv.URL+"/races/"+race.String()+"/ws", wsHeaders(t, user))
	wsWaitSubscription(t, ctx, b, race)
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"power":1}`)); err != nil {
		t.Fatal(err)
	}
	for ingested.Load() == 0 {
		if ctx.Err() != nil {
			t.Fatal("participant telemetry not ingested")
		}
		time.Sleep(time.Millisecond)
	}
}

// Through the router, the real RaceService decides participation.
func TestRaceWSRouterRefusesNonParticipant(t *testing.T) {
	ctx := wsContext(t)
	user := wsUser()
	b := newObservedBroadcaster()
	srv := httptest.NewServer(internalhttp.NewRouter(ctx, internalhttp.RouterConfig{
		Queries: &handlerUserFetcher{user: user}, JWTSecret: testJWTSecret,
		RaceSvc: app.NewRaceService(db.New(participantDB{member: false}), nil, b, app.RaceServiceConfig{}), Broadcaster: b,
	}))
	t.Cleanup(srv.Close)
	conn, resp, err := websocket.Dial(ctx, srv.URL+"/races/"+uuid.Must(uuid.NewV7()).String()+"/ws", &websocket.DialOptions{HTTPHeader: wsHeaders(t, user)})
	if conn != nil {
		_ = conn.CloseNow()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-participant through router: response=%v error=%v", resp, err)
	}
}
