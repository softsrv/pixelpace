package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

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

func TestRaceWSRouterAuthentication(t *testing.T) {
	ctx := wsContext(t)
	user, race := wsUser(), uuid.Must(uuid.NewV7())
	b := newObservedBroadcaster()
	srv := httptest.NewServer(internalhttp.NewRouter(ctx, internalhttp.RouterConfig{
		Queries: &handlerUserFetcher{user: user}, JWTSecret: testJWTSecret,
		RaceSvc: app.NewRaceService(nil, nil, b, app.RaceServiceConfig{}), Broadcaster: b,
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
	srv := wsServer(t, user, handlers.NewRaceWSHandler(ingest, realtime.NewFake()))
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
	srv := wsServer(t, user, handlers.NewRaceWSHandler(ingest, b))
	url, headers := srv.URL+"/races/"+race.String()+"/ws", wsHeaders(t, user)
	first := wsDial(t, ctx, url, headers)
	sub := wsWaitSubscription(t, ctx, b, race)
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
	h := handlers.NewRaceWSHandler(ingest, b)
	userA, userB := wsUser(), wsUser()
	srvA, srvB := wsServer(t, userA, h), wsServer(t, userB, h)
	race, otherRace := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	a := wsDial(t, ctx, srvA.URL+"/races/"+race.String()+"/ws", wsHeaders(t, userA))
	wsWaitSubscription(t, ctx, b, race)
	peer := wsDial(t, ctx, srvB.URL+"/races/"+race.String()+"/ws", wsHeaders(t, userB))
	wsWaitSubscription(t, ctx, b, race)
	other := wsDial(t, ctx, srvA.URL+"/races/"+otherRace.String()+"/ws", wsHeaders(t, userA))
	wsWaitSubscription(t, ctx, b, otherRace)
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
	h := handlers.NewRaceWSHandler(nil, realtime.NewFake())
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
			srv := wsServer(t, user, handlers.NewRaceWSHandler(ingest, b))
			conn := wsDial(t, ctx, srv.URL+"/races/"+uuid.Must(uuid.NewV7()).String()+"/ws", wsHeaders(t, user))
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
