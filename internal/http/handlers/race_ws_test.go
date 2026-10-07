package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

type stubRaceIngester struct {
	ingestFn func(context.Context, uuid.UUID, uuid.UUID, app.TelemetrySample) error
}

func (s *stubRaceIngester) Ingest(ctx context.Context, raceID, userID uuid.UUID, sample app.TelemetrySample) error {
	return s.ingestFn(ctx, raceID, userID, sample)
}

type raceIngestCall struct {
	raceID uuid.UUID
	userID uuid.UUID
	sample app.TelemetrySample
}

func recordingRaceIngester(calls chan<- raceIngestCall) *stubRaceIngester {
	return &stubRaceIngester{ingestFn: func(ctx context.Context, raceID, userID uuid.UUID, sample app.TelemetrySample) error {
		select {
		case calls <- raceIngestCall{raceID, userID, sample}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
}

func raceWSContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func raceWSHeaders(t *testing.T, userID uuid.UUID) http.Header {
	t.Helper()
	token, err := auth.IssueAccessToken(userID, "racer@example.com", testJWTSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return http.Header{"Cookie": {(&http.Cookie{Name: "access_token", Value: token.AccessToken}).String()}}
}

func raceWSServer(t *testing.T, h *handlers.RaceWSHandler, userID uuid.UUID) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	returned := make(chan struct{}, 16)
	mux := http.NewServeMux()
	endpoint := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.Serve(w, r)
		returned <- struct{}{}
	})
	mux.Handle("GET /races/{id}/ws", middleware.Authenticate(&handlerUserFetcher{
		user: db.User{ID: userID, Email: "racer@example.com"},
	}, testJWTSecret)(endpoint))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, returned
}

func dialRaceWS(t *testing.T, ctx context.Context, srv *httptest.Server, raceID, userID uuid.UUID) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, srv.URL+"/races/"+raceID.String()+"/ws", &websocket.DialOptions{HTTPHeader: raceWSHeaders(t, userID)})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func waitRaceWS(t *testing.T, ctx context.Context, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("handler/subscription did not finish before deadline")
	}
}

func receiveRaceIngest(t *testing.T, ctx context.Context, calls <-chan raceIngestCall) raceIngestCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-ctx.Done():
		t.Fatal("Ingest not called before deadline")
		return raceIngestCall{}
	}
}

func assertRaceWSRejected(t *testing.T, ctx context.Context, url string, headers http.Header, status int) *http.Response {
	t.Helper()
	conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
	if conn != nil {
		_ = conn.CloseNow()
		t.Fatal("unexpected established WebSocket")
	}
	if err == nil || response == nil || response.StatusCode != status {
		t.Fatalf("upgrade: response=%v error=%v, want HTTP %d rejection", response, err, status)
	}
	return response
}

func TestRaceWSRejectsUnauthenticatedUpgrade(t *testing.T) {
	ctx := raceWSContext(t)
	// Exercise the production route, not a separately registered test route.
	srv := httptest.NewServer(internalhttp.NewRouter(ctx, internalhttp.RouterConfig{JWTSecret: testJWTSecret}))
	defer srv.Close()
	for _, cookie := range []string{"", "access_token=invalid"} {
		headers := http.Header{"HX-Request": {"true"}, "Cookie": {cookie}}
		response := assertRaceWSRejected(t, ctx, srv.URL+"/races/"+uuid.NewString()+"/ws", headers, http.StatusUnauthorized)
		// The handler's defense-in-depth 401 does not set this header. This
		// assertion detects removing authMW from the production registration.
		if response.Header.Get("HX-Redirect") != "/login" {
			t.Fatal("rejection did not come from authentication middleware")
		}
	}
}

func TestRaceWSRejectsSecondConcurrentConnection(t *testing.T) {
	ctx := raceWSContext(t)
	userID, raceID := uuid.New(), uuid.New()
	calls := make(chan raceIngestCall, 1)
	fake := realtime.NewFake()
	srv, _ := raceWSServer(t, handlers.NewRaceWSHandler(recordingRaceIngester(calls), fake), userID)
	first := dialRaceWS(t, ctx, srv, raceID, userID)
	assertRaceWSRejected(t, ctx, srv.URL+"/races/"+raceID.String()+"/ws", raceWSHeaders(t, userID), http.StatusConflict)
	// Verify both directions on the original socket after the rejected attempt.
	if err := first.Write(ctx, websocket.MessageText, []byte(`{"power":123}`)); err != nil {
		t.Fatal(err)
	}
	call := receiveRaceIngest(t, ctx, calls)
	if call.userID != userID || call.raceID != raceID || call.sample.Power != 123 {
		t.Fatalf("unexpected ingest: %+v", call)
	}
	payload := []byte(`{"power":456}`)
	if err := fake.Publish(ctx, raceID.String(), payload); err != nil {
		t.Fatal(err)
	}
	_, got, err := first.Read(ctx)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("original socket relay = %s, %v", got, err)
	}
}

func TestRaceWSAuthenticatedIdentityAndRelay(t *testing.T) {
	ctx := raceWSContext(t)
	userID, raceID := uuid.New(), uuid.New()
	calls := make(chan raceIngestCall, 2)
	fake := realtime.NewFake()
	srv, _ := raceWSServer(t, handlers.NewRaceWSHandler(recordingRaceIngester(calls), fake), userID)
	conn, _, err := websocket.Dial(ctx, srv.URL+"/races/"+raceID.String()+"/ws?user_id="+uuid.NewString()+"&race_id="+uuid.NewString(), &websocket.DialOptions{HTTPHeader: raceWSHeaders(t, userID)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	sample := app.TelemetrySample{ElapsedMilliseconds: 1000, DistanceMillimeters: 2400, StrokeRate: 25, Power: 180, SampledAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	for _, want := range []app.TelemetrySample{sample, {}} {
		payload, err := json.Marshal(struct {
			app.TelemetrySample
			UserID uuid.UUID `json:"user_id"`
			RaceID uuid.UUID `json:"race_id"`
		}{want, uuid.New(), uuid.New()})
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
			t.Fatal(err)
		}
		call := receiveRaceIngest(t, ctx, calls)
		if call != (raceIngestCall{raceID, userID, want}) {
			t.Fatalf("Ingest = %+v, want authenticated user, route race and %+v", call, want)
		}
		// Receipt by Ingest establishes that Subscribe has registered already.
		if err := fake.Publish(ctx, raceID.String(), payload); err != nil {
			t.Fatal(err)
		}
		kind, got, err := conn.Read(ctx)
		if err != nil || kind != websocket.MessageText || !bytes.Equal(got, payload) {
			t.Fatalf("relay = %s, %v (type %v)", got, err, kind)
		}
	}
}

func TestRaceWSConnectionKeyIncludesUserAndRace(t *testing.T) {
	ctx := raceWSContext(t)
	userID, otherUserID, raceID, otherRaceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	calls := make(chan raceIngestCall, 3)
	h := handlers.NewRaceWSHandler(recordingRaceIngester(calls), realtime.NewFake())
	srv, _ := raceWSServer(t, h, userID)
	otherSrv, _ := raceWSServer(t, h, otherUserID)
	for _, tc := range []struct {
		srv            *httptest.Server
		userID, raceID uuid.UUID
	}{{srv, userID, raceID}, {otherSrv, otherUserID, raceID}, {srv, userID, otherRaceID}} {
		conn := dialRaceWS(t, ctx, tc.srv, tc.raceID, tc.userID)
		if err := conn.Write(ctx, websocket.MessageText, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		call := receiveRaceIngest(t, ctx, calls)
		if call.userID != tc.userID || call.raceID != tc.raceID {
			t.Fatalf("wrong connection identity: %+v", call)
		}
	}
}

func TestRaceWSRejectsMissingContextAndInvalidRace(t *testing.T) {
	ctx := raceWSContext(t)
	h := handlers.NewRaceWSHandler(nil, nil)
	direct := httptest.NewServer(http.HandlerFunc(h.Serve))
	defer direct.Close()
	assertRaceWSRejected(t, ctx, direct.URL+"/races/"+uuid.NewString()+"/ws", nil, http.StatusUnauthorized)
	userID := uuid.New()
	srv, _ := raceWSServer(t, h, userID)
	assertRaceWSRejected(t, ctx, srv.URL+"/races/not-a-uuid/ws", raceWSHeaders(t, userID), http.StatusBadRequest)
}

// observableRaceBroadcaster keeps the real Fake fan-out and exposes only its
// subscription lifecycle, so tests synchronize without sleeps or lost messages.
type observableRaceBroadcaster struct {
	*realtime.Fake
	subscribed chan context.Context
}

func (b *observableRaceBroadcaster) Subscribe(ctx context.Context, raceID string) (<-chan []byte, error) {
	ch, err := b.Fake.Subscribe(ctx, raceID)
	b.subscribed <- ctx
	return ch, err
}

func TestRaceWSRelaysToEveryConnectedRacer(t *testing.T) {
	ctx := raceWSContext(t)
	raceID := uuid.New()
	b := &observableRaceBroadcaster{realtime.NewFake(), make(chan context.Context, 2)}
	h := handlers.NewRaceWSHandler(nil, b)
	var connections []*websocket.Conn
	for i := 0; i < 2; i++ {
		userID := uuid.New()
		srv, _ := raceWSServer(t, h, userID)
		connections = append(connections, dialRaceWS(t, ctx, srv, raceID, userID))
		select {
		case <-b.subscribed:
		case <-ctx.Done():
			t.Fatal("racer did not subscribe")
		}
	}
	for _, payload := range [][]byte{[]byte(`{"power":123}`), []byte(`{"power":456}`)} {
		if err := b.Publish(ctx, raceID.String(), payload); err != nil {
			t.Fatal(err)
		}
		for _, conn := range connections {
			_, got, err := conn.Read(ctx)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("racer relay = %s, %v", got, err)
			}
		}
	}
}

func TestRaceWSProductionRouterUpgradesAndRelays(t *testing.T) {
	ctx := raceWSContext(t)
	userID, raceID := uuid.New(), uuid.New()
	b := &observableRaceBroadcaster{realtime.NewFake(), make(chan context.Context, 1)}
	srv := httptest.NewServer(internalhttp.NewRouter(ctx, internalhttp.RouterConfig{
		Queries: &handlerUserFetcher{user: db.User{ID: userID}}, JWTSecret: testJWTSecret,
		RaceSvc: app.NewRaceService(nil, nil, b, app.RaceServiceConfig{}), Broadcaster: b,
	}))
	defer srv.Close()
	conn := dialRaceWS(t, ctx, srv, raceID, userID)
	select {
	case <-b.subscribed:
	case <-ctx.Done():
		t.Fatal("router did not subscribe")
	}
	payload := []byte(`{"power":200}`)
	if err := b.Publish(ctx, raceID.String(), payload); err != nil {
		t.Fatal(err)
	}
	_, got, err := conn.Read(ctx)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("router relay = %s, %v", got, err)
	}
}

type stubRaceBroadcaster struct {
	subscribeFn func(context.Context, string) (<-chan []byte, error)
}

func (b *stubRaceBroadcaster) Subscribe(ctx context.Context, raceID string) (<-chan []byte, error) {
	return b.subscribeFn(ctx, raceID)
}

func TestRaceWSReleasesConnectionAndSubscription(t *testing.T) {
	for _, ending := range []string{"client_close", "invalid_json", "ingest_error", "subscription_error", "subscription_closed"} {
		t.Run(ending, func(t *testing.T) {
			ctx := raceWSContext(t)
			userID, raceID := uuid.New(), uuid.New()
			subscriptions := make(chan context.Context, 2)
			payloads := make(chan []byte)
			b := &stubRaceBroadcaster{subscribeFn: func(ctx context.Context, _ string) (<-chan []byte, error) {
				subscriptions <- ctx
				if ending == "subscription_error" {
					return nil, errors.New("subscription failed")
				}
				return payloads, nil
			}}
			ingester := &stubRaceIngester{ingestFn: func(context.Context, uuid.UUID, uuid.UUID, app.TelemetrySample) error {
				return errors.New("ingest failed")
			}}
			srv, returned := raceWSServer(t, handlers.NewRaceWSHandler(ingester, b), userID)
			conn := dialRaceWS(t, ctx, srv, raceID, userID)
			var subscription context.Context
			select {
			case subscription = <-subscriptions:
			case <-ctx.Done():
				t.Fatal("no subscription")
			}
			switch ending {
			case "client_close":
				_ = conn.CloseNow()
			case "invalid_json", "ingest_error":
				payload := []byte(`{}`)
				if ending == "invalid_json" {
					payload = []byte(`{`)
				}
				if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
					t.Fatal(err)
				}
			case "subscription_closed":
				close(payloads)
			}
			waitRaceWS(t, ctx, returned)
			waitRaceWS(t, ctx, subscription.Done())
			// Reconnect after the handler exits: the old key must be released.
			dialRaceWS(t, ctx, srv, raceID, userID)
		})
	}
}

func TestRaceWSFailedUpgradeReleasesKey(t *testing.T) {
	ctx := raceWSContext(t)
	userID, raceID := uuid.New(), uuid.New()
	srv, returned := raceWSServer(t, handlers.NewRaceWSHandler(nil, realtime.NewFake()), userID)
	headers := raceWSHeaders(t, userID)
	headers.Set("Origin", "https://untrusted.example")
	assertRaceWSRejected(t, ctx, srv.URL+"/races/"+raceID.String()+"/ws", headers, http.StatusForbidden)
	waitRaceWS(t, ctx, returned)
	dialRaceWS(t, ctx, srv, raceID, userID)
}
