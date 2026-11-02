//go:build integration

package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/concept2"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
	"github.com/softsrv/starter/internal/http/middleware"
	"github.com/softsrv/starter/web"
)

// stubSealer stands in for the token sealer, which is not under test here:
// these tests prove which row each call touches, not how tokens are sealed.
// It binds the user id to the blob so a mix-up between users would show.
type stubSealer struct{}

func (stubSealer) Seal(pt []byte, userID uuid.UUID) ([]byte, error) {
	return append([]byte("sealed:"+userID.String()+":"), pt...), nil
}

func (stubSealer) Open(ct []byte, userID uuid.UUID) ([]byte, error) {
	prefix := "sealed:" + userID.String() + ":"
	if len(ct) < len(prefix) || string(ct[:len(prefix)]) != prefix {
		return nil, errors.New("wrong user")
	}
	return ct[len(prefix):], nil
}

func newConcept2Service(pool *pgxpool.Pool) *app.Concept2Service {
	return app.NewConcept2Service(db.New(pool), app.Concept2ServiceConfig{Sealer: stubSealer{}})
}

// rawConcept2Row returns every column of userID's stored row as text, so a
// before/after comparison catches a change to any byte of it.
func rawConcept2Row(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) string {
	t.Helper()
	var row string
	err := pool.QueryRow(context.Background(),
		"SELECT c::text FROM concept2_connections c WHERE user_id = $1", userID).Scan(&row)
	if err != nil {
		t.Fatalf("read raw concept2 row for %s: %v", userID, err)
	}
	return row
}

func concept2RowCount(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM concept2_connections WHERE user_id = $1", userID).Scan(&n); err != nil {
		t.Fatalf("count concept2 rows: %v", err)
	}
	return n
}

func grantFor(name string, c2ID int64, tz string) app.Concept2Grant {
	return app.Concept2Grant{
		C2UserID:     c2ID,
		Username:     name,
		AccessToken:  "access-" + name,
		RefreshToken: "refresh-" + name,
		ExpiresAt:    time.Now().Add(7 * 24 * time.Hour).UTC().Truncate(time.Second),
		Scope:        "user:read,results:write",
		Timezone:     tz,
	}
}

// CLM-4: a Concept2 connection is stored per user; connecting, reading,
// reconnecting or disconnecting as A never reads, creates, changes or
// deletes B's row.
func TestIntegration_Concept2_TwoUserIsolation(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := newConcept2Service(pool)
	a := newNamedTestUser(t, pool)
	b := newNamedTestUser(t, pool)

	// Neither user is connected yet; reading A creates nothing.
	if _, err := svc.Status(ctx, a.ID); !errors.Is(err, app.ErrConcept2NotConnected) {
		t.Fatalf("Status(A) before connect: err = %v, want ErrConcept2NotConnected", err)
	}

	// Connecting B alone leaves A unconnected, and A's status never shows B.
	if _, err := svc.SaveConnection(ctx, b.ID, grantFor("rower_b", 2002, "Europe/Berlin")); err != nil {
		t.Fatalf("SaveConnection(B): %v", err)
	}
	if _, err := svc.Status(ctx, a.ID); !errors.Is(err, app.ErrConcept2NotConnected) {
		t.Fatalf("Status(A) with only B connected: err = %v, want ErrConcept2NotConnected", err)
	}
	if n := concept2RowCount(t, pool, a.ID); n != 0 {
		t.Fatalf("A has %d rows after B connected, want 0", n)
	}
	bRow := rawConcept2Row(t, pool, b.ID)

	// Connecting A creates A's row only.
	got, err := svc.SaveConnection(ctx, a.ID, grantFor("rower_a", 1001, "America/New_York"))
	if err != nil {
		t.Fatalf("SaveConnection(A): %v", err)
	}
	if got.Username != "rower_a" || got.C2UserID != 1001 {
		t.Fatalf("SaveConnection(A) = %+v, want rower_a/1001", got)
	}
	if after := rawConcept2Row(t, pool, b.ID); after != bRow {
		t.Fatalf("B's row changed when A connected:\n before %s\n after  %s", bRow, after)
	}

	// Reading A returns A's account, never B's.
	st, err := svc.Status(ctx, a.ID)
	if err != nil {
		t.Fatalf("Status(A): %v", err)
	}
	if st.Username != "rower_a" || st.C2UserID != 1001 || st.Timezone != "America/New_York" {
		t.Fatalf("Status(A) = %+v, want rower_a/1001/America/New_York", st)
	}
	stB, err := svc.Status(ctx, b.ID)
	if err != nil {
		t.Fatalf("Status(B): %v", err)
	}
	if stB.Username != "rower_b" || stB.C2UserID != 2002 {
		t.Fatalf("Status(B) = %+v, want rower_b/2002", stB)
	}

	// Reconnecting A (upsert) overwrites A's row and leaves B untouched.
	if _, err := svc.SaveConnection(ctx, a.ID, grantFor("rower_a2", 1003, "")); err != nil {
		t.Fatalf("SaveConnection(A) reconnect: %v", err)
	}
	st, err = svc.Status(ctx, a.ID)
	if err != nil {
		t.Fatalf("Status(A) after reconnect: %v", err)
	}
	if st.Username != "rower_a2" || st.C2UserID != 1003 || st.Timezone != "UTC" {
		t.Fatalf("Status(A) after reconnect = %+v, want rower_a2/1003/UTC", st)
	}
	if n := concept2RowCount(t, pool, a.ID); n != 1 {
		t.Fatalf("A has %d rows after reconnect, want 1", n)
	}
	if after := rawConcept2Row(t, pool, b.ID); after != bRow {
		t.Fatalf("B's row changed when A reconnected:\n before %s\n after  %s", bRow, after)
	}

	// Disconnecting A deletes A's row; B's row is byte-identical.
	if err := svc.Disconnect(ctx, a.ID); err != nil {
		t.Fatalf("Disconnect(A): %v", err)
	}
	if _, err := svc.Status(ctx, a.ID); !errors.Is(err, app.ErrConcept2NotConnected) {
		t.Fatalf("Status(A) after disconnect: err = %v, want ErrConcept2NotConnected", err)
	}
	if after := rawConcept2Row(t, pool, b.ID); after != bRow {
		t.Fatalf("B's row changed when A disconnected:\n before %s\n after  %s", bRow, after)
	}

	// Disconnecting A again is a no-op and still leaves B untouched.
	if err := svc.Disconnect(ctx, a.ID); err != nil {
		t.Fatalf("Disconnect(A) again: %v", err)
	}
	if after := rawConcept2Row(t, pool, b.ID); after != bRow {
		t.Fatalf("B's row changed on A's repeat disconnect:\n before %s\n after  %s", bRow, after)
	}
}

// CLM-4: user_id is the primary key and references users(id), so a
// connection can exist only for a real user and only once per user.
func TestIntegration_Concept2_ConnectionRequiresExistingUser(t *testing.T) {
	pool := testDB(t)
	svc := newConcept2Service(pool)
	if _, err := svc.SaveConnection(context.Background(), uuid.New(), grantFor("ghost", 9, "UTC")); err == nil {
		t.Fatal("SaveConnection for a user that does not exist succeeded, want a foreign-key error")
	}
}

// CLM-5: access and refresh tokens are stored sealed (encrypted), never in
// plaintext: access_token_enc and refresh_token_enc bytes do not contain the
// plaintext tokens, and open back to the original tokens under the real sealer.
func TestIntegration_Concept2_TokensAtRestEncrypted(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	secret := []byte("integration-test-secret-32-bytes!!!")
	sealer := concept2.NewSealer(secret)
	svc := app.NewConcept2Service(db.New(pool), app.Concept2ServiceConfig{Sealer: sealer})
	u := newNamedTestUser(t, pool)

	grant := app.Concept2Grant{
		C2UserID:     5555,
		Username:     "sealed_rower",
		AccessToken:  "c2-access-token-plaintext-supersecret123",
		RefreshToken: "c2-refresh-token-plaintext-supersecret456",
		ExpiresAt:    time.Now().Add(7 * 24 * time.Hour).UTC().Truncate(time.Second),
		Scope:        "user:read,results:write",
		Timezone:     "America/Chicago",
	}

	if _, err := svc.SaveConnection(ctx, u.ID, grant); err != nil {
		t.Fatalf("SaveConnection: %v", err)
	}

	var accessEnc, refreshEnc []byte
	err := pool.QueryRow(ctx,
		"SELECT access_token_enc, refresh_token_enc FROM concept2_connections WHERE user_id = $1", u.ID).
		Scan(&accessEnc, &refreshEnc)
	if err != nil {
		t.Fatalf("query stored tokens: %v", err)
	}

	// Stored bytes do not contain the plaintext tokens
	if bytes.Contains(accessEnc, []byte(grant.AccessToken)) {
		t.Fatalf("access_token_enc contains plaintext token %q", grant.AccessToken)
	}
	if bytes.Contains(refreshEnc, []byte(grant.RefreshToken)) {
		t.Fatalf("refresh_token_enc contains plaintext token %q", grant.RefreshToken)
	}

	// Tokens open back to the original plaintext under the same sealer and user
	decryptedAccess, err := sealer.Open(accessEnc, u.ID)
	if err != nil {
		t.Fatalf("sealer.Open(access_token_enc): %v", err)
	}
	if string(decryptedAccess) != grant.AccessToken {
		t.Fatalf("decrypted access token = %q, want %q", decryptedAccess, grant.AccessToken)
	}

	decryptedRefresh, err := sealer.Open(refreshEnc, u.ID)
	if err != nil {
		t.Fatalf("sealer.Open(refresh_token_enc): %v", err)
	}
	if string(decryptedRefresh) != grant.RefreshToken {
		t.Fatalf("decrypted refresh token = %q, want %q", decryptedRefresh, grant.RefreshToken)
	}

	// Opening for a different user fails with ErrDecrypt
	otherUser := uuid.New()
	if _, err := sealer.Open(accessEnc, otherUser); !errors.Is(err, concept2.ErrDecrypt) {
		t.Fatalf("sealer.Open with other user: got %v, want %v", err, concept2.ErrDecrypt)
	}

	// Opening with a sealer built from another secret fails with ErrDecrypt
	otherSealer := concept2.NewSealer([]byte("different-secret-for-decryption-test!!"))
	if _, err := otherSealer.Open(accessEnc, u.ID); !errors.Is(err, concept2.ErrDecrypt) {
		t.Fatalf("otherSealer.Open: got %v, want %v", err, concept2.ErrDecrypt)
	}
}

// CLM-13: POST /profile/concept2/disconnect, served by the real handler over
// the real service and Postgres, deletes the signed-in user's row, renders the
// Connect state and sends nothing to the Concept2 Logbook (a counting fake
// the real client points at), so results already posted there stay. A second
// disconnect is a no-op without error.
func TestIntegration_Concept2_DisconnectDeletesRowWithoutCallingLogbook(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	var logbookRequests atomic.Int64
	logbook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		logbookRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(logbook.Close)
	client := concept2.New(logbook.URL, "client-id", "client-secret", "https://pixelpace.example/profile/concept2/callback")

	base, err := template.ParseFS(web.FS, "templates/base.html")
	if err != nil {
		t.Fatalf("parse base template: %v", err)
	}
	if _, err := base.ParseFS(web.FS, "templates/partials/*.html"); err != nil {
		t.Fatalf("parse partials: %v", err)
	}
	q := db.New(pool)
	svc := newConcept2Service(pool)
	h := handlers.NewConcept2Handler(svc, client, handlers.NewTemplateRenderer(base, web.FS, "templates"), false)
	const secret = "integration-test-secret-32-bytes!!!"
	endpoint := middleware.Authenticate(q, secret)(http.HandlerFunc(h.Disconnect))

	u := newNamedTestUser(t, pool)
	other := newNamedTestUser(t, pool)
	tok, err := auth.IssueAccessToken(u.ID, u.Email, secret, 15*time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if _, err := svc.SaveConnection(ctx, u.ID, grantFor("leaving_rower", 3003, "Europe/Berlin")); err != nil {
		t.Fatalf("SaveConnection: %v", err)
	}
	if _, err := svc.SaveConnection(ctx, other.ID, grantFor("staying_rower", 3004, "UTC")); err != nil {
		t.Fatalf("SaveConnection(other): %v", err)
	}
	otherRow := rawConcept2Row(t, pool, other.ID)
	before := logbookRequests.Load()

	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/profile/concept2/disconnect", nil)
		req.AddCookie(&http.Cookie{Name: "access_token", Value: tok.AccessToken})
		rr := httptest.NewRecorder()
		endpoint.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("disconnect #%d status = %d, want 200; body: %s", i, rr.Code, rr.Body.String())
		}
		if body := rr.Body.String(); !strings.Contains(body, "data-concept2-connect") || strings.Contains(body, "Connected as") {
			t.Fatalf("disconnect #%d did not render the Connect state:\n%s", i, body)
		}
		if n := concept2RowCount(t, pool, u.ID); n != 0 {
			t.Fatalf("rows after disconnect #%d = %d, want 0", i, n)
		}
	}
	if _, err := svc.Status(ctx, u.ID); !errors.Is(err, app.ErrConcept2NotConnected) {
		t.Fatalf("Status after disconnect: err = %v, want ErrConcept2NotConnected", err)
	}
	if err := svc.Disconnect(ctx, u.ID); err != nil {
		t.Fatalf("Disconnect when already disconnected: %v, want nil", err)
	}
	if after := rawConcept2Row(t, pool, other.ID); after != otherRow {
		t.Fatalf("another user's row changed:\n before %s\n after  %s", otherRow, after)
	}
	if got := logbookRequests.Load() - before; got != 0 {
		t.Fatalf("Logbook received %d requests during disconnect, want 0", got)
	}
}

// fakeLogbook is an httptest stand-in for the Concept2 Logbook's Add Result
// endpoint. It records every request and answers 201 with a new result id.
type fakeLogbook struct {
	*httptest.Server
	mu       sync.Mutex
	requests []fakeLogbookRequest
}

type fakeLogbookRequest struct {
	Method, Path, Authorization string
	Body                        []byte
}

func newFakeLogbook(t *testing.T) *fakeLogbook {
	t.Helper()
	lb := &fakeLogbook{}
	lb.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lb.mu.Lock()
		lb.requests = append(lb.requests, fakeLogbookRequest{
			Method: r.Method, Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: body,
		})
		id := 7000 + len(lb.requests)
		lb.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"data":{"id":%d,"type":"rower"}}`, id)
	}))
	t.Cleanup(lb.Close)
	return lb
}

func (lb *fakeLogbook) received() []fakeLogbookRequest {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return slices.Clone(lb.requests)
}

// newConcept2Poster builds the service that enqueues and posts results, with
// the real Logbook client pointed at lb.
func newConcept2Poster(pool *pgxpool.Pool, lb *fakeLogbook) *app.Concept2Service {
	return app.NewConcept2Service(db.New(pool), app.Concept2ServiceConfig{
		Sealer: stubSealer{},
		Client: concept2.New(lb.URL, "client-id", "client-secret", "https://pixelpace.example/profile/concept2/callback"),
	})
}

// connectConcept2 stores a Concept2 connection for userID, as the OAuth
// callback does.
func connectConcept2(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, name string) {
	t.Helper()
	if _, err := newConcept2Service(pool).SaveConnection(context.Background(), userID, grantFor(name, 4000+int64(userID[15]), "UTC")); err != nil {
		t.Fatalf("SaveConnection(%s): %v", name, err)
	}
}

type resultPostRow struct {
	UserID   uuid.UUID
	Status   string
	Attempts int
	C2ID     *int64
}

// resultPosts reads every concept2_result_posts row of raceID, by user id.
func resultPosts(t *testing.T, pool *pgxpool.Pool, raceID uuid.UUID) []resultPostRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		"SELECT user_id, status, attempts, c2_result_id FROM concept2_result_posts WHERE race_id = $1 ORDER BY user_id", raceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []resultPostRow
	for rows.Next() {
		var r resultPostRow
		if err := rows.Scan(&r.UserID, &r.Status, &r.Attempts, &r.C2ID); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

// CLM-1: finalizing a race queues exactly one pending post for a connected
// finisher, and one RunPoster pass POSTs one rower result to the Logbook with
// distance = the race target in meters and time = the recorded finish in
// tenths of a second, after which the post is posted.
func TestIntegration_Concept2_FinishedRacePostsOneResult(t *testing.T) {
	const target = 500000 // 500 m
	f := seedRaceRoom(t, 1, target, 5*time.Millisecond)
	lb := newFakeLogbook(t)
	poster := newConcept2Poster(f.pool, lb)
	f.withResultPoster(poster)
	connectConcept2(t, f.pool, f.users[0], "finisher")
	f.begin(t)
	f.ingest(t, 0, 50000, 250000, f.now)
	f.ingest(t, 0, 95437, target, f.now.Add(time.Millisecond))
	if err := f.svc.Run(context.Background(), f.race.ID); err != nil {
		t.Fatal(err)
	}

	posts := resultPosts(t, f.pool, f.race.ID)
	if len(posts) != 1 || posts[0].UserID != f.users[0] || posts[0].Status != "pending" || posts[0].Attempts != 0 {
		t.Fatalf("posts after finalize = %+v, want one pending post for the finisher", posts)
	}
	if got := lb.received(); len(got) != 0 {
		t.Fatalf("Logbook received %d requests before the poster ran", len(got))
	}

	posted, err := poster.PostDue(context.Background())
	if err != nil || posted != 1 {
		t.Fatalf("PostDue = %d, %v; want 1, nil", posted, err)
	}
	reqs := lb.received()
	if len(reqs) != 1 {
		t.Fatalf("Logbook received %d requests, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Method != http.MethodPost || req.Path != "/api/users/me/results" || req.Authorization != "Bearer access-finisher" {
		t.Fatalf("request = %s %s auth %q", req.Method, req.Path, req.Authorization)
	}
	var body struct {
		Type        string `json:"type"`
		Distance    int64  `json:"distance"`
		Time        int64  `json:"time"`
		WeightClass string `json:"weight_class"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("decode body %s: %v", req.Body, err)
	}
	if body.Type != "rower" || body.Distance != 500 || body.Time != 954 || body.WeightClass != "H" {
		t.Fatalf("posted %s, want a rower result of 500 m in 954 tenths", req.Body)
	}
	posts = resultPosts(t, f.pool, f.race.ID)
	if len(posts) != 1 || posts[0].Status != "posted" || posts[0].Attempts != 1 || posts[0].C2ID == nil || *posts[0].C2ID != 7001 {
		t.Fatalf("posts after PostDue = %+v, want one posted with Logbook id 7001", posts)
	}

	// A posted result is never sent again.
	if posted, err := poster.PostDue(context.Background()); err != nil || posted != 0 {
		t.Fatalf("second PostDue = %d, %v; want 0, nil", posted, err)
	}
	if got := lb.received(); len(got) != 1 {
		t.Fatalf("Logbook received %d requests after a second pass, want 1", len(got))
	}
}

// CLM-16: a finisher with no detail beyond the finish (no stroke, heart-rate
// or monitor data) still posts, with type, date, distance, time and
// weight_class, and every detail key is left out of the body.
func TestIntegration_Concept2_ResultWithoutDetailOmitsMissingKeys(t *testing.T) {
	const target = 500000
	f := seedRaceRoom(t, 1, target, 5*time.Millisecond)
	lb := newFakeLogbook(t)
	poster := newConcept2Poster(f.pool, lb)
	f.withResultPoster(poster)
	connectConcept2(t, f.pool, f.users[0], "bare")
	f.begin(t)
	f.ingest(t, 0, 120000, target, f.now)
	if err := f.svc.Run(context.Background(), f.race.ID); err != nil {
		t.Fatal(err)
	}
	// The finish is recorded; with its samples gone the racer has no
	// telemetry to send, and no PM5 details were ever recorded.
	if _, err := f.pool.Exec(context.Background(), "DELETE FROM telemetry_samples WHERE race_id = $1", f.race.ID); err != nil {
		t.Fatal(err)
	}
	if posted, err := poster.PostDue(context.Background()); err != nil || posted != 1 {
		t.Fatalf("PostDue = %d, %v; want 1, nil", posted, err)
	}
	reqs := lb.received()
	if len(reqs) != 1 {
		t.Fatalf("Logbook received %d requests, want 1", len(reqs))
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(reqs[0].Body, &keys); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"type", "date", "distance", "time", "weight_class"} {
		if _, ok := keys[want]; !ok {
			t.Errorf("body %s has no %q", reqs[0].Body, want)
		}
	}
	for _, absent := range []string{"stroke_data", "workout", "metadata", "serial_number", "stroke_count", "calories_total", "drag_factor", "heart_rate", "stroke_rate", "verified"} {
		if _, ok := keys[absent]; ok {
			t.Errorf("body %s carries %q, which has no value", reqs[0].Body, absent)
		}
	}
	if !bytes.Contains(reqs[0].Body, []byte(`"distance":500`)) || !bytes.Contains(reqs[0].Body, []byte(`"time":1200`)) {
		t.Errorf("body %s, want distance 500 and time 1200", reqs[0].Body)
	}
}

// CLM-15: a finisher with full telemetry and a recorded PM5 posts the full
// detail: weight_class H, the final stroke count and calories, the average
// stroke rate and drag factor, 500 m splits that add up to the finish time,
// one stroke per sample (t/d/p/spm) and the monitor metadata. verified is
// never sent.
func TestIntegration_Concept2_ResultCarriesFullDetail(t *testing.T) {
	const target = 1000000 // 1000 m
	f := seedRaceRoom(t, 1, target, 5*time.Millisecond)
	lb := newFakeLogbook(t)
	poster := newConcept2Poster(f.pool, lb)
	f.withResultPoster(poster)
	connectConcept2(t, f.pool, f.users[0], "detailed")
	f.begin(t)
	ctx := context.Background()
	if err := f.svc.RecordMonitor(ctx, f.race.ID, f.users[0], app.MonitorInfo{
		SerialNumber: "430395351", FirmwareVersion: "171", HardwareRevision: "PM5v3",
	}); err != nil {
		t.Fatal(err)
	}
	i32 := func(v int32) *int32 { return &v }
	samples := []app.TelemetrySample{
		{ElapsedMilliseconds: 50000, DistanceMillimeters: 250000, StrokeRate: 26, Power: 200,
			StrokeCount: i32(20), Calories: i32(15), HeartRate: i32(150), DragFactor: i32(120)},
		{ElapsedMilliseconds: 100000, DistanceMillimeters: 500000, StrokeRate: 28, Power: 220,
			StrokeCount: i32(45), Calories: i32(32), DragFactor: i32(124)},
		{ElapsedMilliseconds: 150000, DistanceMillimeters: 750000, StrokeRate: 30, Power: 240,
			StrokeCount: i32(70), Calories: i32(50), DragFactor: i32(126)},
		{ElapsedMilliseconds: 199950, DistanceMillimeters: target, StrokeRate: 32, Power: 240,
			StrokeCount: i32(95), Calories: i32(68), DragFactor: i32(130)},
	}
	for i, sample := range samples {
		sample.SampledAt = f.now.Add(time.Duration(i) * time.Millisecond)
		if err := f.svc.Ingest(ctx, f.race.ID, f.users[0], sample); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.Run(ctx, f.race.ID); err != nil {
		t.Fatal(err)
	}
	if posted, err := poster.PostDue(ctx); err != nil || posted != 1 {
		t.Fatalf("PostDue = %d, %v; want 1, nil", posted, err)
	}
	reqs := lb.received()
	if len(reqs) != 1 {
		t.Fatalf("Logbook received %d requests, want 1", len(reqs))
	}
	raw := reqs[0].Body
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["verified"]; ok {
		t.Errorf("body %s sets verified", raw)
	}
	type split struct {
		Distance   int64  `json:"distance"`
		Time       int64  `json:"time"`
		StrokeRate *int32 `json:"stroke_rate"`
	}
	type stroke struct {
		T   int64  `json:"t"`
		D   int64  `json:"d"`
		P   *int32 `json:"p"`
		SPM *int32 `json:"spm"`
	}
	var body struct {
		Distance      int64  `json:"distance"`
		Time          int64  `json:"time"`
		WeightClass   string `json:"weight_class"`
		StrokeCount   *int32 `json:"stroke_count"`
		CaloriesTotal *int32 `json:"calories_total"`
		StrokeRate    *int32 `json:"stroke_rate"`
		DragFactor    *int32 `json:"drag_factor"`
		Workout       struct {
			Splits []split `json:"splits"`
		} `json:"workout"`
		StrokeData []stroke          `json:"stroke_data"`
		Metadata   map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body %s: %v", raw, err)
	}
	deref := func(p *int32) int32 {
		if p == nil {
			return -1
		}
		return *p
	}
	if body.Distance != 1000 || body.Time != 1999 || body.WeightClass != "H" {
		t.Errorf("distance/time/weight_class = %d/%d/%q, want 1000/1999/H", body.Distance, body.Time, body.WeightClass)
	}
	if deref(body.StrokeCount) != 95 || deref(body.CaloriesTotal) != 68 ||
		deref(body.StrokeRate) != 29 || deref(body.DragFactor) != 125 {
		t.Errorf("stroke_count/calories_total/stroke_rate/drag_factor = %d/%d/%d/%d, want 95/68/29/125",
			deref(body.StrokeCount), deref(body.CaloriesTotal), deref(body.StrokeRate), deref(body.DragFactor))
	}
	gotSplits := make([][3]int64, len(body.Workout.Splits))
	for i, sp := range body.Workout.Splits {
		gotSplits[i] = [3]int64{sp.Distance, sp.Time, int64(deref(sp.StrokeRate))}
	}
	if want := [][3]int64{{500, 1000, 27}, {500, 999, 31}}; !slices.Equal(gotSplits, want) {
		t.Errorf("splits (distance, time, rate) = %v, want %v", gotSplits, want)
	}
	gotStrokes := make([][4]int64, len(body.StrokeData))
	for i, st := range body.StrokeData {
		gotStrokes[i] = [4]int64{st.T, st.D, int64(deref(st.P)), int64(deref(st.SPM))}
	}
	// Pace is the 500 m pace (tenths) that the sample's watts give.
	wantStrokes := [][4]int64{{500, 2500, 1205, 26}, {1000, 5000, 1167, 28}, {1500, 7500, 1134, 30}, {1999, 10000, 1134, 32}}
	if !slices.Equal(gotStrokes, wantStrokes) {
		t.Errorf("stroke_data (t, d, p, spm) = %v, want %v", gotStrokes, wantStrokes)
	}
	wantMeta := map[string]string{
		"serial_number": "430395351", "firmware_version": "171", "pm_version": "5", "other": "PixelPace BLE; hw=PM5v3",
	}
	if len(body.Metadata) != len(wantMeta) {
		t.Errorf("metadata = %v, want %v", body.Metadata, wantMeta)
	}
	for k, v := range wantMeta {
		if body.Metadata[k] != v {
			t.Errorf("metadata[%q] = %q, want %q", k, body.Metadata[k], v)
		}
	}
}

// scriptedLogbook stands in for Concept2: the token endpoint and the Add
// Result endpoint answer from per-endpoint scripts (the last answer repeats),
// and every request is recorded in arrival order.
type scriptedLogbook struct {
	*httptest.Server
	mu      sync.Mutex
	calls   []scriptedCall
	results []scriptedAnswer
	tokens  []scriptedAnswer
	delay   time.Duration
}

type scriptedAnswer struct {
	status int
	body   string
}

type scriptedCall struct {
	Path, Authorization string
	Form                url.Values
	Body                []byte
}

func newScriptedLogbook(t *testing.T) *scriptedLogbook {
	t.Helper()
	lb := &scriptedLogbook{}
	lb.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		lb.mu.Lock()
		lb.calls = append(lb.calls, scriptedCall{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Form: form, Body: body})
		n := 0
		script := &lb.results
		if r.URL.Path == "/oauth/access_token" {
			script = &lb.tokens
		}
		for _, c := range lb.calls {
			if c.Path == r.URL.Path {
				n++
			}
		}
		answer := scriptedAnswer{status: http.StatusCreated, body: fmt.Sprintf(`{"data":{"id":%d}}`, 8000+n)}
		if len(*script) > 0 {
			answer = (*script)[min(n, len(*script))-1]
		}
		delay := lb.delay
		lb.mu.Unlock()
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(answer.status)
		_, _ = io.WriteString(w, answer.body)
	}))
	t.Cleanup(lb.Close)
	return lb
}

func (lb *scriptedLogbook) received(path string) []scriptedCall {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	var got []scriptedCall
	for _, c := range lb.calls {
		if path == "" || c.Path == path {
			got = append(got, c)
		}
	}
	return got
}

const resultsPath = "/api/users/me/results"

// testClock is the poster's clock; it starts at the real time so posts the
// database stamped NOW() are due at once.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock { return &testClock{now: time.Now().Add(time.Second)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func posterFor(pool *pgxpool.Pool, baseURL string, clock *testClock, edit func(*app.Concept2ServiceConfig)) *app.Concept2Service {
	cfg := app.Concept2ServiceConfig{
		Sealer: stubSealer{},
		Client: concept2.New(baseURL, "client-id", "client-secret", "https://pixelpace.example/profile/concept2/callback"),
		Now:    clock.Now,
	}
	if edit != nil {
		edit(&cfg)
	}
	return app.NewConcept2Service(db.New(pool), cfg)
}

// finishedRace runs a one-racer race to the finish with poster as its result
// poster, after connect has stored the racer's connection.
func finishedRace(t *testing.T, poster app.RaceResultPoster, connect func(f *raceFixture)) *raceFixture {
	t.Helper()
	f := seedRaceRoom(t, 1, 500000, 5*time.Millisecond)
	f.withResultPoster(poster)
	connect(f)
	f.begin(t)
	f.ingest(t, 0, 100000, 500000, f.now)
	if err := f.svc.Run(context.Background(), f.race.ID); err != nil {
		t.Fatal(err)
	}
	return f
}

type postState struct {
	Status        string
	Attempts      int
	LastError     *string
	NextAttemptAt time.Time
}

func postStateOf(t *testing.T, pool *pgxpool.Pool, raceID uuid.UUID) postState {
	t.Helper()
	var p postState
	if err := pool.QueryRow(context.Background(),
		"SELECT status, attempts, last_error, next_attempt_at FROM concept2_result_posts WHERE race_id = $1", raceID).
		Scan(&p.Status, &p.Attempts, &p.LastError, &p.NextAttemptAt); err != nil {
		t.Fatalf("read post of race %s: %v", raceID, err)
	}
	return p
}

func needsReconnect(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) bool {
	t.Helper()
	var v bool
	if err := pool.QueryRow(context.Background(), "SELECT needs_reconnect FROM concept2_connections WHERE user_id = $1", userID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func postDue(t *testing.T, svc *app.Concept2Service) int {
	t.Helper()
	n, err := svc.PostDue(context.Background())
	if err != nil {
		t.Fatalf("PostDue: %v", err)
	}
	return n
}

// captureLogs sends the default logger to a buffer until the test ends.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// CLM-4: a 503 leaves the post pending with attempts bumped and its next try
// pushed out by a gap that doubles per attempt up to RetryMax; the post is
// not sent again before then, and ends posted when the Logbook recovers.
func TestIntegration_Concept2Poster_RetriesWithCappedExponentialBackoff(t *testing.T) {
	clock := newTestClock()
	lb := newScriptedLogbook(t)
	lb.results = []scriptedAnswer{
		{http.StatusServiceUnavailable, `{"message":"down"}`},
		{http.StatusServiceUnavailable, `{"message":"down"}`},
		{http.StatusCreated, `{"data":{"id":9001}}`},
	}
	var poster *app.Concept2Service
	f := finishedRace(t, nil, func(f *raceFixture) {
		poster = posterFor(f.pool, lb.URL, clock, func(c *app.Concept2ServiceConfig) {
			c.RetryBase = time.Minute
			c.RetryMax = 90 * time.Second
		})
		f.withResultPoster(poster)
		connectConcept2(t, f.pool, f.users[0], "retry")
	})

	wantGaps := []time.Duration{time.Minute, 90 * time.Second} // doubled 2m is capped at 90s
	for i, gap := range wantGaps {
		if n := postDue(t, poster); n != 0 {
			t.Fatalf("pass %d posted %d, want 0", i+1, n)
		}
		p := postStateOf(t, f.pool, f.race.ID)
		if p.Status != "pending" || p.Attempts != i+1 || p.LastError == nil || !strings.Contains(*p.LastError, "503") {
			t.Fatalf("after 503 #%d post = %+v, want pending, attempts %d, a 503 error", i+1, p, i+1)
		}
		if got := p.NextAttemptAt.Sub(clock.Now()); got < gap-time.Millisecond || got > gap+time.Millisecond {
			t.Fatalf("after 503 #%d next attempt in %v, want %v", i+1, got, gap)
		}
		// Not due yet: a pass just before the gap ends sends nothing.
		clock.Advance(gap - time.Second)
		postDue(t, poster)
		if got := len(lb.received(resultsPath)); got != i+1 {
			t.Fatalf("Logbook got %d posts before the gap ended, want %d", got, i+1)
		}
		clock.Advance(time.Second)
	}
	if n := postDue(t, poster); n != 1 {
		t.Fatalf("third pass posted %d, want 1", n)
	}
	if p := postStateOf(t, f.pool, f.race.ID); p.Status != "posted" || p.Attempts != 3 {
		t.Fatalf("post = %+v, want posted after 3 attempts", p)
	}
}

// CLM-4: a pending post left by a previous run is posted by a fresh
// service's RunPoster, the loop main starts at boot.
func TestIntegration_Concept2Poster_FreshRunPosterPostsPendingRows(t *testing.T) {
	clock := newTestClock()
	lb := newScriptedLogbook(t)
	f := finishedRace(t, posterFor(testDB(t), lb.URL, clock, nil), func(f *raceFixture) {
		connectConcept2(t, f.pool, f.users[0], "restart")
	})
	if p := postStateOf(t, f.pool, f.race.ID); p.Status != "pending" {
		t.Fatalf("post before restart = %+v, want pending", p)
	}

	fresh := posterFor(f.pool, lb.URL, clock, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fresh.RunPoster(ctx, 10*time.Millisecond) }()
	deadline := time.Now().Add(5 * time.Second)
	for postStateOf(t, f.pool, f.race.ID).Status != "posted" {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("fresh RunPoster did not post the pending row within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("RunPoster returned %v, want context.Canceled", err)
	}
	if got := lb.received(resultsPath); len(got) != 1 || got[0].Authorization != "Bearer access-restart" {
		t.Fatalf("Logbook posts = %+v, want one for the pending row", got)
	}
}

// CLM-5: a 409 (the Logbook already has the result) marks the post posted,
// and no later pass sends it again.
func TestIntegration_Concept2Poster_ConflictMarksPosted(t *testing.T) {
	clock := newTestClock()
	lb := newScriptedLogbook(t)
	lb.results = []scriptedAnswer{{http.StatusConflict, `{"message":"duplicate result"}`}}
	var poster *app.Concept2Service
	f := finishedRace(t, nil, func(f *raceFixture) {
		poster = posterFor(f.pool, lb.URL, clock, nil)
		f.withResultPoster(poster)
		connectConcept2(t, f.pool, f.users[0], "dup")
	})
	if n := postDue(t, poster); n != 1 {
		t.Fatalf("PostDue = %d, want 1", n)
	}
	if p := postStateOf(t, f.pool, f.race.ID); p.Status != "posted" || p.LastError != nil {
		t.Fatalf("post after 409 = %+v, want posted with no error", p)
	}
	clock.Advance(24 * time.Hour)
	postDue(t, poster)
	postDue(t, poster)
	if got := len(lb.received(resultsPath)); got != 1 {
		t.Fatalf("Logbook got %d posts, want 1", got)
	}
}

// CLM-5: two posters passing over the same due row at once send it once.
func TestIntegration_Concept2Poster_ConcurrentPassesSendOnce(t *testing.T) {
	clock := newTestClock()
	lb := newScriptedLogbook(t)
	lb.delay = 100 * time.Millisecond // keep the first claim's post in flight
	var a *app.Concept2Service
	f := finishedRace(t, nil, func(f *raceFixture) {
		a = posterFor(f.pool, lb.URL, clock, nil)
		f.withResultPoster(a)
		connectConcept2(t, f.pool, f.users[0], "race-pair")
	})
	b := posterFor(f.pool, lb.URL, clock, nil)

	start := make(chan struct{})
	var wg sync.WaitGroup
	posted := make([]int, 2)
	for i, svc := range []*app.Concept2Service{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			n, err := svc.PostDue(context.Background())
			if err != nil {
				t.Errorf("PostDue: %v", err)
			}
			posted[i] = n
		}()
	}
	close(start)
	wg.Wait()
	if got := len(lb.received(resultsPath)); got != 1 {
		t.Fatalf("Logbook got %d posts from two concurrent passes, want 1", got)
	}
	if posted[0]+posted[1] != 1 {
		t.Fatalf("passes posted %v, want 1 in total", posted)
	}
	if p := postStateOf(t, f.pool, f.race.ID); p.Status != "posted" || p.Attempts != 1 {
		t.Fatalf("post = %+v, want posted after one attempt", p)
	}
}

// CLM-6: a 422 fails the post with Concept2's message, and a failed post is
// never claimed again.
func TestIntegration_Concept2Poster_UnprocessableFailsWithMessage(t *testing.T) {
	clock := newTestClock()
	lb := newScriptedLogbook(t)
	lb.results = []scriptedAnswer{{http.StatusUnprocessableEntity, `{"message":"The time field is invalid."}`}}
	var poster *app.Concept2Service
	f := finishedRace(t, nil, func(f *raceFixture) {
		poster = posterFor(f.pool, lb.URL, clock, nil)
		f.withResultPoster(poster)
		connectConcept2(t, f.pool, f.users[0], "invalid")
	})
	if n := postDue(t, poster); n != 0 {
		t.Fatalf("PostDue = %d, want 0", n)
	}
	p := postStateOf(t, f.pool, f.race.ID)
	if p.Status != "failed" || p.Attempts != 1 || p.LastError == nil || !strings.Contains(*p.LastError, "The time field is invalid.") {
		t.Fatalf("post after 422 = %+v, want failed with Concept2's message", p)
	}
	clock.Advance(48 * time.Hour)
	postDue(t, poster)
	if got := len(lb.received(resultsPath)); got != 1 {
		t.Fatalf("Logbook got %d posts, want 1", got)
	}
	if p := postStateOf(t, f.pool, f.race.ID); p.Status != "failed" || p.Attempts != 1 {
		t.Fatalf("failed post changed to %+v", p)
	}
}

// CLM-7: an expired access token is refreshed before the post; the sealed
// new access token, rotated refresh token and expiry are saved, and the post
// bears the new token.
func TestIntegration_Concept2Poster_RefreshesExpiredToken(t *testing.T) {
	clock := newTestClock()
	lb := newScriptedLogbook(t)
	lb.tokens = []scriptedAnswer{{http.StatusOK, `{"access_token":"new-access","refresh_token":"rotated-refresh","expires_in":7200}`}}
	logs := captureLogs(t)
	var poster *app.Concept2Service
	f := finishedRace(t, nil, func(f *raceFixture) {
		poster = posterFor(f.pool, lb.URL, clock, nil)
		f.withResultPoster(poster)
		connectConcept2(t, f.pool, f.users[0], "expired")
		if _, err := f.pool.Exec(context.Background(),
			"UPDATE concept2_connections SET expires_at = $2 WHERE user_id = $1", f.users[0], clock.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	})
	before := time.Now()
	if n := postDue(t, poster); n != 1 {
		t.Fatalf("PostDue = %d, want 1", n)
	}
	calls := lb.received("")
	if len(calls) != 2 || calls[0].Path != "/oauth/access_token" || calls[1].Path != resultsPath {
		t.Fatalf("calls = %+v, want the refresh, then the post", calls)
	}
	if got := calls[0].Form.Get("refresh_token"); got != "refresh-expired" || calls[0].Form.Get("grant_type") != "refresh_token" {
		t.Fatalf("refresh form = %v, want grant_type=refresh_token with the stored refresh token", calls[0].Form)
	}
	if calls[1].Authorization != "Bearer new-access" {
		t.Fatalf("post authorization = %q, want the new token", calls[1].Authorization)
	}
	var accessEnc, refreshEnc []byte
	var expiresAt time.Time
	if err := f.pool.QueryRow(context.Background(),
		"SELECT access_token_enc, refresh_token_enc, expires_at FROM concept2_connections WHERE user_id = $1", f.users[0]).
		Scan(&accessEnc, &refreshEnc, &expiresAt); err != nil {
		t.Fatal(err)
	}
	access, err := stubSealer{}.Open(accessEnc, f.users[0])
	if err != nil || string(access) != "new-access" {
		t.Fatalf("stored access opens to %q, %v; want new-access", access, err)
	}
	refresh, err := stubSealer{}.Open(refreshEnc, f.users[0])
	if err != nil || string(refresh) != "rotated-refresh" {
		t.Fatalf("stored refresh opens to %q, %v; want rotated-refresh", refresh, err)
	}
	if exp := expiresAt.Sub(before); exp < 2*time.Hour-time.Minute || exp > 2*time.Hour+time.Minute {
		t.Fatalf("stored expires_at is %v from now, want about 2h", exp)
	}
	if needsReconnect(t, f.pool, f.users[0]) {
		t.Fatal("refresh left the connection needing reconnect")
	}
	for _, secret := range []string{"new-access", "rotated-refresh", "refresh-expired", "access-expired"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("logs carry token %q:\n%s", secret, logs)
		}
	}
}

// CLM-8: a refused refresh, or tokens that will not open, set the connection
// needs_reconnect; the post stays pending with attempts unchanged, nothing is
// POSTed (now or on later passes), no token is logged, and the profile panel
// shows the reconnect state.
func TestIntegration_Concept2Poster_UnusableTokensNeedReconnect(t *testing.T) {
	cases := []struct {
		name   string
		script func(lb *scriptedLogbook)
		poster func(pool *pgxpool.Pool, lb *scriptedLogbook, clock *testClock) *app.Concept2Service
		expire bool
	}{
		{
			name:   "refresh refused",
			expire: true,
			script: func(lb *scriptedLogbook) {
				lb.tokens = []scriptedAnswer{{http.StatusBadRequest, `{"error":"invalid_grant","error_description":"refresh token revoked"}`}}
			},
			poster: func(pool *pgxpool.Pool, lb *scriptedLogbook, clock *testClock) *app.Concept2Service {
				return posterFor(pool, lb.URL, clock, nil)
			},
		},
		{
			name:   "tokens fail to decrypt",
			script: func(*scriptedLogbook) {},
			poster: func(pool *pgxpool.Pool, lb *scriptedLogbook, clock *testClock) *app.Concept2Service {
				// Sealed with another secret than the one stored them.
				return posterFor(pool, lb.URL, clock, func(c *app.Concept2ServiceConfig) {
					c.Sealer = concept2.NewSealer([]byte("a-different-secret-than-the-stored-one!"))
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := newTestClock()
			lb := newScriptedLogbook(t)
			tc.script(lb)
			logs := captureLogs(t)
			var poster *app.Concept2Service
			f := finishedRace(t, nil, func(f *raceFixture) {
				poster = tc.poster(f.pool, lb, clock)
				f.withResultPoster(poster)
				connectConcept2(t, f.pool, f.users[0], "stale")
				if tc.expire {
					if _, err := f.pool.Exec(context.Background(),
						"UPDATE concept2_connections SET expires_at = $2 WHERE user_id = $1", f.users[0], clock.Now().Add(-time.Minute)); err != nil {
						t.Fatal(err)
					}
				}
			})
			if n := postDue(t, poster); n != 0 {
				t.Fatalf("PostDue = %d, want 0", n)
			}
			if !needsReconnect(t, f.pool, f.users[0]) {
				t.Fatal("connection not set needs_reconnect")
			}
			if p := postStateOf(t, f.pool, f.race.ID); p.Status != "pending" || p.Attempts != 0 {
				t.Fatalf("post = %+v, want pending with attempts unchanged (0)", p)
			}
			clock.Advance(time.Hour)
			postDue(t, poster)
			if got := lb.received(resultsPath); len(got) != 0 {
				t.Fatalf("Logbook got %d posts, want none", len(got))
			}
			if p := postStateOf(t, f.pool, f.race.ID); p.Status != "pending" || p.Attempts != 0 {
				t.Fatalf("post after a later pass = %+v, want pending with attempts 0", p)
			}
			for _, secret := range []string{"access-stale", "refresh-stale"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("logs carry token %q:\n%s", secret, logs)
				}
			}
			body := renderConcept2Panel(t, f.pool, f.users[0])
			for _, want := range []string{"Reconnect Concept2 to keep posting results", ">Reconnect</button>", `class="pp-btn pp-btn-cyan"`, "data-concept2-connect"} {
				if !strings.Contains(body, want) {
					t.Fatalf("panel lacks %q:\n%s", want, body)
				}
			}
		})
	}
}

// renderConcept2Panel serves GET /profile/concept2 for userID through the
// real handler, service and templates, and returns the body.
func renderConcept2Panel(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) string {
	t.Helper()
	base, err := template.ParseFS(web.FS, "templates/base.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.ParseFS(web.FS, "templates/partials/*.html"); err != nil {
		t.Fatal(err)
	}
	const secret = "integration-test-secret-32-bytes!!!"
	if _, err := pool.Exec(context.Background(), "UPDATE users SET email_verified = true WHERE id = $1", userID); err != nil {
		t.Fatal(err)
	}
	client := concept2.New("https://log.example", "client-id", "client-secret", "https://pixelpace.example/profile/concept2/callback")
	h := handlers.NewConcept2Handler(newConcept2Service(pool), client, handlers.NewTemplateRenderer(base, web.FS, "templates"), false)
	var email string
	if err := pool.QueryRow(context.Background(), "SELECT email FROM users WHERE id = $1", userID).Scan(&email); err != nil {
		t.Fatal(err)
	}
	tok, err := auth.IssueAccessToken(userID, email, secret, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/profile/concept2", nil)
	req.AddCookie(&http.Cookie{Name: "access_token", Value: tok.AccessToken})
	rr := httptest.NewRecorder()
	middleware.Authenticate(db.New(pool), secret)(http.HandlerFunc(h.Panel)).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("panel status = %d; body: %s", rr.Code, rr.Body)
	}
	return rr.Body.String()
}

// CLM-9: in DevMode finalizing a race queues no post and the Logbook gets no
// request, even for a post already pending; with DevMode off the same race
// queues one post.
func TestIntegration_Concept2Poster_DevModeQueuesAndSendsNothing(t *testing.T) {
	for _, devMode := range []bool{true, false} {
		t.Run(fmt.Sprintf("DevMode=%v", devMode), func(t *testing.T) {
			clock := newTestClock()
			lb := newScriptedLogbook(t)
			var poster *app.Concept2Service
			f := finishedRace(t, nil, func(f *raceFixture) {
				poster = posterFor(f.pool, lb.URL, clock, func(c *app.Concept2ServiceConfig) { c.DevMode = devMode })
				f.withResultPoster(poster)
				connectConcept2(t, f.pool, f.users[0], "dev")
			})
			posts := resultPosts(t, f.pool, f.race.ID)
			if want := map[bool]int{true: 0, false: 1}[devMode]; len(posts) != want {
				t.Fatalf("posts after finalize = %+v, want %d", posts, want)
			}
			if devMode {
				// A post queued outside DevMode is not sent by a DevMode poster.
				if err := posterFor(f.pool, lb.URL, clock, nil).Enqueue(context.Background(), db.New(f.pool), f.race.ID); err != nil {
					t.Fatal(err)
				}
				postDue(t, poster)
				if got := lb.received(""); len(got) != 0 {
					t.Fatalf("DevMode Logbook requests = %d, want 0", len(got))
				}
				if p := postStateOf(t, f.pool, f.race.ID); p.Status != "pending" || p.Attempts != 0 {
					t.Fatalf("DevMode touched the post: %+v", p)
				}
			}
		})
	}
}

// CLM-18: with auto_post off a finalized race queues nothing for the user;
// once it is back on only races finalized afterwards are queued.
func TestIntegration_Concept2Poster_AutoPostOffQueuesNothingAndNoBackPosting(t *testing.T) {
	clock := newTestClock()
	lb := newScriptedLogbook(t)
	ctx := context.Background()
	f := seedRaceRoom(t, 1, 500000, 5*time.Millisecond)
	poster := posterFor(f.pool, lb.URL, clock, nil)
	f.withResultPoster(poster)
	connectConcept2(t, f.pool, f.users[0], "toggler")
	if _, err := poster.SetAutoPost(ctx, f.users[0], false); err != nil {
		t.Fatal(err)
	}

	// Race A finalizes while auto_post is off.
	f.begin(t)
	f.ingest(t, 0, 100000, 500000, f.now)
	if err := f.svc.Run(ctx, f.race.ID); err != nil {
		t.Fatal(err)
	}
	raceA := f.race.ID
	if posts := resultPosts(t, f.pool, raceA); len(posts) != 0 {
		t.Fatalf("race A posts with auto_post off = %+v, want none", posts)
	}

	if _, err := poster.SetAutoPost(ctx, f.users[0], true); err != nil {
		t.Fatal(err)
	}
	// Race B, a rematch in a new room, finalizes with auto_post on.
	g := rematch(t, f)
	g.begin(t)
	g.ingest(t, 0, 110000, 500000, f.now)
	if err := g.svc.Run(ctx, g.race.ID); err != nil {
		t.Fatal(err)
	}
	if posts := resultPosts(t, f.pool, g.race.ID); len(posts) != 1 || posts[0].UserID != f.users[0] {
		t.Fatalf("race B posts = %+v, want one for the user", posts)
	}
	if posts := resultPosts(t, f.pool, raceA); len(posts) != 0 {
		t.Fatalf("race A was back-posted: %+v", posts)
	}
	if n := postDue(t, poster); n != 1 {
		t.Fatalf("PostDue = %d, want 1 (race B only)", n)
	}
	var body struct {
		Time int64 `json:"time"`
	}
	if got := lb.received(resultsPath); len(got) != 1 || json.Unmarshal(got[0].Body, &body) != nil || body.Time != 1100 {
		t.Fatalf("Logbook posts = %+v, want only race B (1100 tenths)", got)
	}
}

// rematch seeds a new counting-down room for f's users, sharing f's service
// and clock, and cleans it up before f's own cleanup removes the users.
func rematch(t *testing.T, f *raceFixture) *raceFixture {
	t.Helper()
	ctx := context.Background()
	g := *f
	g.room = uuid.Must(uuid.NewV7())
	code := strings.ToUpper(strings.NewReplacer("0", "A", "1", "B", "O", "C", "I", "D", "L", "E").Replace(strings.ReplaceAll(g.room.String(), "-", "")[26:32]))
	if _, err := f.pool.Exec(ctx,
		"INSERT INTO rooms (id, race_type_id, host_user_id, status, join_code) SELECT $1, race_type_id, host_user_id, 'counting_down', $3 FROM rooms WHERE id = $2",
		g.room, f.room, code); err != nil {
		t.Fatal(err)
	}
	for _, id := range f.users {
		if _, err := f.pool.Exec(ctx, "INSERT INTO room_participants (id, room_id, user_id, equipment_ready_at) VALUES ($1, $2, $3, NOW())", uuid.Must(uuid.NewV7()), g.room, id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, query := range []string{
			"DELETE FROM telemetry_samples WHERE race_id IN (SELECT id FROM races WHERE room_id = $1)",
			"DELETE FROM race_participants WHERE race_id IN (SELECT id FROM races WHERE room_id = $1)",
			"DELETE FROM concept2_result_posts WHERE race_id IN (SELECT id FROM races WHERE room_id = $1)",
			"DELETE FROM races WHERE room_id = $1",
			"DELETE FROM room_participants WHERE room_id = $1",
			"DELETE FROM rooms WHERE id = $1",
		} {
			if _, err := f.pool.Exec(ctx, query, g.room); err != nil {
				t.Error(err)
			}
		}
	})
	return &g
}

// CLM-2: the posted date is the finish in the connection's IANA zone, with
// that zone as timezone; a zone that does not load posts in UTC.
func TestIntegration_Concept2Poster_DateInConnectionTimezone(t *testing.T) {
	cases := []struct{ zone, wantDate, wantTZ string }{
		{"Australia/Sydney", "2026-07-01 06:00:00", "Australia/Sydney"},
		{"Not/A_Zone", "2026-06-30 20:00:00", "UTC"},
	}
	for _, tc := range cases {
		t.Run(tc.zone, func(t *testing.T) {
			clock := newTestClock()
			lb := newScriptedLogbook(t)
			var poster *app.Concept2Service
			f := finishedRace(t, nil, func(f *raceFixture) {
				poster = posterFor(f.pool, lb.URL, clock, nil)
				f.withResultPoster(poster)
				if _, err := newConcept2Service(f.pool).SaveConnection(context.Background(), f.users[0], grantFor("tz", 77, tc.zone)); err != nil {
					t.Fatal(err)
				}
			})
			if _, err := f.pool.Exec(context.Background(),
				"UPDATE race_participants SET finished_at = '2026-06-30T20:00:00Z' WHERE race_id = $1", f.race.ID); err != nil {
				t.Fatal(err)
			}
			if n := postDue(t, poster); n != 1 {
				t.Fatalf("PostDue = %d, want 1", n)
			}
			got := lb.received(resultsPath)
			var body struct {
				Date     string `json:"date"`
				Timezone string `json:"timezone"`
			}
			if len(got) != 1 || json.Unmarshal(got[0].Body, &body) != nil {
				t.Fatalf("Logbook posts = %+v", got)
			}
			if body.Date != tc.wantDate || body.Timezone != tc.wantTZ {
				t.Fatalf("posted date %q tz %q, want %q %q", body.Date, body.Timezone, tc.wantDate, tc.wantTZ)
			}
		})
	}
}

// A post queued while auto_post was on is not sent once the user switches it
// off: the claim re-reads the toggle. Switching it back on sends it.
func TestIntegration_Concept2Poster_AutoPostOffAfterQueueingSendsNothing(t *testing.T) {
	clock := newTestClock()
	lb := newScriptedLogbook(t)
	ctx := context.Background()
	var poster *app.Concept2Service
	f := finishedRace(t, nil, func(f *raceFixture) {
		poster = posterFor(f.pool, lb.URL, clock, nil)
		f.withResultPoster(poster)
		connectConcept2(t, f.pool, f.users[0], "late-toggle")
	})
	if p := postStateOf(t, f.pool, f.race.ID); p.Status != "pending" {
		t.Fatalf("post after finalize = %+v, want pending", p)
	}
	if _, err := poster.SetAutoPost(ctx, f.users[0], false); err != nil {
		t.Fatal(err)
	}
	if n := postDue(t, poster); n != 0 {
		t.Fatalf("PostDue with auto_post off = %d, want 0", n)
	}
	if got := lb.received(""); len(got) != 0 {
		t.Fatalf("Logbook got %d requests with auto_post off, want 0", len(got))
	}
	if p := postStateOf(t, f.pool, f.race.ID); p.Status != "pending" || p.Attempts != 0 {
		t.Fatalf("post with auto_post off = %+v, want pending and unclaimed", p)
	}
	if _, err := poster.SetAutoPost(ctx, f.users[0], true); err != nil {
		t.Fatal(err)
	}
	if n := postDue(t, poster); n != 1 {
		t.Fatalf("PostDue after auto_post back on = %d, want 1", n)
	}
}

// A race that finishes while the connection needs reconnecting is still
// queued; the post waits unclaimed, and is sent once the user reconnects.
func TestIntegration_Concept2Poster_FinishWhileNeedsReconnectIsQueuedAndHeld(t *testing.T) {
	clock := newTestClock()
	lb := newScriptedLogbook(t)
	var poster *app.Concept2Service
	f := finishedRace(t, nil, func(f *raceFixture) {
		poster = posterFor(f.pool, lb.URL, clock, nil)
		f.withResultPoster(poster)
		connectConcept2(t, f.pool, f.users[0], "flagged")
		if _, err := f.pool.Exec(context.Background(),
			"UPDATE concept2_connections SET needs_reconnect = true WHERE user_id = $1", f.users[0]); err != nil {
			t.Fatal(err)
		}
	})
	posts := resultPosts(t, f.pool, f.race.ID)
	if len(posts) != 1 || posts[0].Status != "pending" {
		t.Fatalf("posts after a finish while flagged = %+v, want one pending", posts)
	}
	if n := postDue(t, poster); n != 0 {
		t.Fatalf("PostDue while flagged = %d, want 0", n)
	}
	if got := lb.received(""); len(got) != 0 {
		t.Fatalf("Logbook got %d requests while flagged, want 0", len(got))
	}
	if p := postStateOf(t, f.pool, f.race.ID); p.Attempts != 0 {
		t.Fatalf("post while flagged = %+v, want unclaimed", p)
	}
	connectConcept2(t, f.pool, f.users[0], "flagged") // reconnect clears the flag
	if n := postDue(t, poster); n != 1 {
		t.Fatalf("PostDue after reconnect = %d, want 1", n)
	}
}

// Transient Logbook failures never fail a post: well past the old limit of 5
// attempts it is still pending, and it posts once the Logbook recovers.
func TestIntegration_Concept2Poster_TransientFailuresNeverFailThePost(t *testing.T) {
	const failures = 12
	clock := newTestClock()
	lb := newScriptedLogbook(t)
	for i := 0; i < failures; i++ {
		status := []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusBadGateway}[i%3]
		lb.results = append(lb.results, scriptedAnswer{status, `{"message":"try later"}`})
	}
	lb.results = append(lb.results, scriptedAnswer{http.StatusCreated, `{"data":{"id":9100}}`})
	var poster *app.Concept2Service
	f := finishedRace(t, nil, func(f *raceFixture) {
		poster = posterFor(f.pool, lb.URL, clock, nil)
		f.withResultPoster(poster)
		connectConcept2(t, f.pool, f.users[0], "patient")
	})
	for i := 1; i <= failures; i++ {
		if n := postDue(t, poster); n != 0 {
			t.Fatalf("pass %d posted %d, want 0", i, n)
		}
		p := postStateOf(t, f.pool, f.race.ID)
		if p.Status != "pending" || p.Attempts != i {
			t.Fatalf("after failure %d post = %+v, want pending with %d attempts", i, p, i)
		}
		if gap := p.NextAttemptAt.Sub(clock.Now()); gap > time.Hour+time.Millisecond {
			t.Fatalf("after failure %d retry gap %v exceeds the 1h cap", i, gap)
		}
		clock.Advance(time.Hour)
	}
	if n := postDue(t, poster); n != 1 {
		t.Fatalf("pass after recovery posted %d, want 1", n)
	}
	if p := postStateOf(t, f.pool, f.race.ID); p.Status != "posted" {
		t.Fatalf("post = %+v, want posted", p)
	}
}

// A 2xx token reply that issues no access token is a refused renewal: the
// connection needs reconnecting, the post stays pending with attempts
// unchanged and nothing is POSTed.
func TestIntegration_Concept2Poster_RefreshWithoutAccessTokenNeedsReconnect(t *testing.T) {
	clock := newTestClock()
	lb := newScriptedLogbook(t)
	lb.tokens = []scriptedAnswer{{http.StatusOK, `{"error":"invalid_grant"}`}}
	var poster *app.Concept2Service
	f := finishedRace(t, nil, func(f *raceFixture) {
		poster = posterFor(f.pool, lb.URL, clock, nil)
		f.withResultPoster(poster)
		connectConcept2(t, f.pool, f.users[0], "no-token")
		if _, err := f.pool.Exec(context.Background(),
			"UPDATE concept2_connections SET expires_at = $2 WHERE user_id = $1", f.users[0], clock.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	})
	if n := postDue(t, poster); n != 0 {
		t.Fatalf("PostDue = %d, want 0", n)
	}
	if !needsReconnect(t, f.pool, f.users[0]) {
		t.Fatal("connection not set needs_reconnect")
	}
	if p := postStateOf(t, f.pool, f.race.ID); p.Status != "pending" || p.Attempts != 0 {
		t.Fatalf("post = %+v, want pending with attempts unchanged (0)", p)
	}
	if got := lb.received(resultsPath); len(got) != 0 {
		t.Fatalf("Logbook got %d posts, want none", len(got))
	}
}

// A 2xx Add Result reply with no readable result id is not counted as posted:
// the post stays pending, and a repeat the Logbook answers 409 settles it.
func TestIntegration_Concept2Poster_UnreadableCreatedReplyIsRetried(t *testing.T) {
	clock := newTestClock()
	lb := newScriptedLogbook(t)
	lb.results = []scriptedAnswer{
		{http.StatusCreated, `<html>ok</html>`},
		{http.StatusConflict, `{"message":"duplicate result"}`},
	}
	var poster *app.Concept2Service
	f := finishedRace(t, nil, func(f *raceFixture) {
		poster = posterFor(f.pool, lb.URL, clock, nil)
		f.withResultPoster(poster)
		connectConcept2(t, f.pool, f.users[0], "garbled")
	})
	if n := postDue(t, poster); n != 0 {
		t.Fatalf("PostDue on an unreadable 201 = %d, want 0", n)
	}
	if p := postStateOf(t, f.pool, f.race.ID); p.Status != "pending" || p.LastError == nil {
		t.Fatalf("post after an unreadable 201 = %+v, want pending with an error", p)
	}
	clock.Advance(time.Hour)
	if n := postDue(t, poster); n != 1 {
		t.Fatalf("PostDue on the 409 repeat = %d, want 1", n)
	}
	if p := postStateOf(t, f.pool, f.race.ID); p.Status != "posted" {
		t.Fatalf("post = %+v, want posted", p)
	}
}

// When the refreshed tokens cannot be saved, the rotation is not dropped
// silently: the connection is flagged needs_reconnect, the result is still
// posted with the new access token, and the pass is not aborted.
func TestIntegration_Concept2Poster_TokenSaveFailureFlagsReconnect(t *testing.T) {
	clock := newTestClock()
	lb := newScriptedLogbook(t)
	lb.tokens = []scriptedAnswer{{http.StatusOK, `{"access_token":"fresh-access","refresh_token":"fresh-refresh","expires_in":7200}`}}
	logs := captureLogs(t)
	ctx := context.Background()
	var poster *app.Concept2Service
	f := finishedRace(t, nil, func(f *raceFixture) {
		poster = posterFor(f.pool, lb.URL, clock, nil)
		f.withResultPoster(poster)
		connectConcept2(t, f.pool, f.users[0], "unsaved")
		if _, err := f.pool.Exec(ctx,
			"UPDATE concept2_connections SET expires_at = $2 WHERE user_id = $1", f.users[0], clock.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	})
	// Make every token write for this user fail, as a database fault would.
	fn := "test_block_c2_tokens_" + strings.ReplaceAll(f.users[0].String(), "-", "")
	for _, stmt := range []string{
		fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.user_id = '%s' AND NEW.access_token_enc IS DISTINCT FROM OLD.access_token_enc THEN
    RAISE EXCEPTION 'token write blocked by test';
  END IF;
  RETURN NEW;
END $$`, fn, f.users[0]),
		fmt.Sprintf("CREATE TRIGGER %s BEFORE UPDATE ON concept2_connections FOR EACH ROW EXECUTE FUNCTION %s()", fn, fn),
	} {
		if _, err := f.pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON concept2_connections", fn))
		_, _ = f.pool.Exec(ctx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", fn))
	})

	n, err := poster.PostDue(ctx)
	if err != nil {
		t.Fatalf("PostDue aborted the pass: %v", err)
	}
	if n != 1 {
		t.Fatalf("PostDue = %d, want 1 (posted with the new token)", n)
	}
	if got := lb.received(resultsPath); len(got) != 1 || got[0].Authorization != "Bearer fresh-access" {
		t.Fatalf("Logbook posts = %+v, want one bearing the new access token", got)
	}
	if !needsReconnect(t, f.pool, f.users[0]) {
		t.Fatal("connection not flagged needs_reconnect after the token save failed")
	}
	for _, secret := range []string{"fresh-access", "fresh-refresh", "refresh-unsaved", "access-unsaved"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("logs carry token %q:\n%s", secret, logs)
		}
	}
}
