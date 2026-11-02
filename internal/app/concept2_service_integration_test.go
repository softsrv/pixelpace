//go:build integration

package app_test

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
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
