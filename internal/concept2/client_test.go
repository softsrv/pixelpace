package concept2_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/softsrv/starter/internal/concept2"
)

func TestAuthorizeURL(t *testing.T) {
	client := concept2.New(
		"https://log-dev.concept2.com",
		"test-client-id",
		"test-client-secret",
		"http://localhost:8080/profile/concept2/callback",
	)

	state := "random-state-12345"
	rawURL := client.AuthorizeURL(state)

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("failed to parse authorize url: %v", err)
	}

	if parsed.Scheme != "https" || parsed.Host != "log-dev.concept2.com" {
		t.Errorf("unexpected scheme/host: %s://%s", parsed.Scheme, parsed.Host)
	}
	if parsed.Path != "/oauth/authorize" {
		t.Errorf("unexpected path: %s, want /oauth/authorize", parsed.Path)
	}

	q := parsed.Query()
	if got := q.Get("client_id"); got != "test-client-id" {
		t.Errorf("client_id = %q, want %q", got, "test-client-id")
	}
	if got := q.Get("response_type"); got != "code" {
		t.Errorf("response_type = %q, want %q", got, "code")
	}
	if got := q.Get("scope"); got != "user:read,results:write" {
		t.Errorf("scope = %q, want %q", got, "user:read,results:write")
	}
	if got := q.Get("redirect_uri"); got != "http://localhost:8080/profile/concept2/callback" {
		t.Errorf("redirect_uri = %q, want %q", got, "http://localhost:8080/profile/concept2/callback")
	}
	if got := q.Get("state"); got != state {
		t.Errorf("state = %q, want %q", got, state)
	}
}

func TestExchangeCode_Success(t *testing.T) {
	const (
		expectedCode   = "auth-code-xyz-123"
		clientID       = "client-id-abc"
		clientSecret   = "client-secret-xyz"
		redirectURI    = "http://localhost:8080/callback"
		expectedAccess = "access-token-foo"
		expectedRef    = "refresh-token-bar"
		expectedScope  = "user:read,results:write"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/access_token" {
			t.Errorf("request path = %q, want /oauth/access_token", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("request method = %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
			t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}
		vals, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("failed to parse form values: %v", err)
		}

		if got := vals.Get("grant_type"); got != "authorization_code" {
			t.Errorf("grant_type = %q, want authorization_code", got)
		}
		if got := vals.Get("client_id"); got != clientID {
			t.Errorf("client_id = %q, want %q", got, clientID)
		}
		if got := vals.Get("client_secret"); got != clientSecret {
			t.Errorf("client_secret = %q, want %q", got, clientSecret)
		}
		if got := vals.Get("code"); got != expectedCode {
			t.Errorf("code = %q, want %q", got, expectedCode)
		}
		if got := vals.Get("redirect_uri"); got != redirectURI {
			t.Errorf("redirect_uri = %q, want %q", got, redirectURI)
		}
		if got := vals.Get("scope"); got != expectedScope {
			t.Errorf("scope = %q, want %q", got, expectedScope)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"access_token": "` + expectedAccess + `",
			"refresh_token": "` + expectedRef + `",
			"expires_in": 3600,
			"scope": "` + expectedScope + `"
		}`))
	}))
	defer server.Close()

	client := concept2.NewClient(server.URL, clientID, clientSecret, redirectURI)
	before := time.Now()
	token, err := client.ExchangeCode(expectedCode)
	if err != nil {
		t.Fatalf("ExchangeCode failed: %v", err)
	}

	if token.AccessToken != expectedAccess {
		t.Errorf("AccessToken = %q, want %q", token.AccessToken, expectedAccess)
	}
	if token.RefreshToken != expectedRef {
		t.Errorf("RefreshToken = %q, want %q", token.RefreshToken, expectedRef)
	}
	if token.Scope != expectedScope {
		t.Errorf("Scope = %q, want %q", token.Scope, expectedScope)
	}
	if token.ExpiresAt.Before(before.Add(3500*time.Second)) || token.ExpiresAt.After(time.Now().Add(3700*time.Second)) {
		t.Errorf("ExpiresAt = %v, want approx 1h from now", token.ExpiresAt)
	}
}

func TestRefresh_Success(t *testing.T) {
	const (
		expectedRefInput = "old-refresh-token-456"
		clientID         = "client-id-abc"
		clientSecret     = "client-secret-xyz"
		redirectURI      = "http://localhost:8080/callback"
		expectedAccess   = "new-access-token-789"
		expectedRef      = "new-refresh-token-012"
		expectedScope    = "user:read,results:write"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/access_token" {
			t.Errorf("request path = %q, want /oauth/access_token", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("request method = %q, want POST", r.Method)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}
		vals, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("failed to parse form values: %v", err)
		}

		if got := vals.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		if got := vals.Get("client_id"); got != clientID {
			t.Errorf("client_id = %q, want %q", got, clientID)
		}
		if got := vals.Get("client_secret"); got != clientSecret {
			t.Errorf("client_secret = %q, want %q", got, clientSecret)
		}
		if got := vals.Get("refresh_token"); got != expectedRefInput {
			t.Errorf("refresh_token = %q, want %q", got, expectedRefInput)
		}
		if got := vals.Get("scope"); got != expectedScope {
			t.Errorf("scope = %q, want %q", got, expectedScope)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"access_token": "` + expectedAccess + `",
			"refresh_token": "` + expectedRef + `",
			"expires_in": 7200,
			"scope": "` + expectedScope + `"
		}`))
	}))
	defer server.Close()

	client := concept2.NewClient(server.URL, clientID, clientSecret, redirectURI)
	token, err := client.Refresh(expectedRefInput)
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	if token.AccessToken != expectedAccess {
		t.Errorf("AccessToken = %q, want %q", token.AccessToken, expectedAccess)
	}
	if token.RefreshToken != expectedRef {
		t.Errorf("RefreshToken = %q, want %q", token.RefreshToken, expectedRef)
	}
	if token.Scope != expectedScope {
		t.Errorf("Scope = %q, want %q", token.Scope, expectedScope)
	}
}

func TestGetMe_Success(t *testing.T) {
	const (
		accessToken  = "bearer-token-val"
		expectedID   = int64(987654)
		expectedUser = "c2_rower_champion"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users/me" {
			t.Errorf("request path = %q, want /api/users/me", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("request method = %q, want GET", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+accessToken {
			t.Errorf("Authorization = %q, want %q", auth, "Bearer "+accessToken)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"id": 987654,
				"username": "c2_rower_champion"
			}
		}`))
	}))
	defer server.Close()

	client := concept2.New(server.URL, "id", "sec", "http://localhost/cb")
	user, err := client.GetMe(accessToken)
	if err != nil {
		t.Fatalf("GetMe failed: %v", err)
	}

	if user.ID != expectedID {
		t.Errorf("user.ID = %d, want %d", user.ID, expectedID)
	}
	if user.Username != expectedUser {
		t.Errorf("user.Username = %q, want %q", user.Username, expectedUser)
	}
}

func TestTypedErrors_Non2xx(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		isAuthErr  bool
	}{
		{
			name:       "400 Bad Request",
			statusCode: http.StatusBadRequest,
			body:       `{"error":"invalid_grant","error_description":"authorization code expired"}`,
		},
		{
			name:       "401 Unauthorized",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":"unauthorized","message":"invalid credentials"}`,
			isAuthErr:  true,
		},
		{
			name:       "500 Internal Server Error",
			statusCode: http.StatusInternalServerError,
			body:       `{"error":"server_error","message":"internal server error"}`,
		},
		{
			name:       "503 Service Unavailable",
			statusCode: http.StatusServiceUnavailable,
			body:       `upstream service down`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := concept2.New(server.URL, "id", "sec", "http://localhost/cb")

			// Check ExchangeCode
			_, err := client.ExchangeCode("code")
			assertAPIError(t, err, tt.statusCode, tt.isAuthErr)

			// Check Refresh
			_, err = client.Refresh("refresh-tok")
			assertAPIError(t, err, tt.statusCode, tt.isAuthErr)

			// Check GetMe
			_, err = client.GetMe("bearer-tok")
			assertAPIError(t, err, tt.statusCode, tt.isAuthErr)
		})
	}
}

func assertAPIError(t *testing.T, err error, wantStatus int, isAuthErr bool) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var apiErr *concept2.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *concept2.APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != wantStatus {
		t.Errorf("apiErr.StatusCode = %d, want %d", apiErr.StatusCode, wantStatus)
	}
	if isAuthErr && !errors.Is(err, concept2.ErrUnauthorized) {
		t.Errorf("expected errors.Is(err, ErrUnauthorized) for 401, got false")
	}
}

func TestMalformedBodies(t *testing.T) {
	malformedResponses := []struct {
		name string
		body string
	}{
		{name: "not json", body: "invalid json string"},
		{name: "truncated json", body: `{"access_token": "foo",`},
		{name: "missing required fields", body: `{}`},
	}

	for _, tt := range malformedResponses {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := concept2.New(server.URL, "id", "sec", "http://localhost/cb")

			_, err := client.ExchangeCode("code")
			if !errors.Is(err, concept2.ErrMalformed) {
				t.Errorf("ExchangeCode: expected ErrMalformed, got: %v", err)
			}

			_, err = client.Refresh("refresh-tok")
			if !errors.Is(err, concept2.ErrMalformed) {
				t.Errorf("Refresh: expected ErrMalformed, got: %v", err)
			}

			_, err = client.GetMe("access-tok")
			if !errors.Is(err, concept2.ErrMalformed) {
				t.Errorf("GetMe: expected ErrMalformed, got: %v", err)
			}
		})
	}
}

func TestSecretsNotLeakedInErrors(t *testing.T) {
	const (
		sensitiveSecret  = "super-confidential-secret-key-999"
		sensitiveCode    = "confidential-auth-code-888"
		sensitiveRefresh = "confidential-refresh-tok-777"
		sensitiveAccess  = "confidential-bearer-tok-666"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		// Server echoes back whatever parameters were received in the request
		body, _ := io.ReadAll(r.Body)
		authHeader := r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{
			"error": "invalid_request",
			"error_description": "Failed request: body ` + string(body) + ` auth ` + authHeader + `"
		}`))
	}))
	defer server.Close()

	client := concept2.New(server.URL, "test-client", sensitiveSecret, "http://localhost/cb")

	_, errExchange := client.ExchangeCode(sensitiveCode)
	checkNoSecrets(t, "ExchangeCode", errExchange, sensitiveSecret, sensitiveCode)

	_, errRefresh := client.Refresh(sensitiveRefresh)
	checkNoSecrets(t, "Refresh", errRefresh, sensitiveSecret, sensitiveRefresh)

	_, errGetMe := client.GetMe(sensitiveAccess)
	checkNoSecrets(t, "GetMe", errGetMe, sensitiveSecret, sensitiveAccess)
}

func checkNoSecrets(t *testing.T, op string, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error, got nil", op)
	}
	errStr := err.Error()
	for _, sec := range secrets {
		if strings.Contains(errStr, sec) {
			t.Errorf("%s: error string leaks secret %q: %s", op, sec, errStr)
		}
	}
	if !strings.Contains(errStr, "[REDACTED]") {
		t.Errorf("%s: expected redacted placeholder in error string: %s", op, errStr)
	}
}

func TestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"foo"}`))
	}))
	defer server.Close()

	// Client has a bounded timeout of 30ms, which is overridable for tests
	timeout := 30 * time.Millisecond
	client := concept2.New(
		server.URL,
		"client-id",
		"client-secret",
		"http://localhost/cb",
		concept2.WithTimeout(timeout),
	)

	start := time.Now()
	_, err := client.ExchangeCode("code")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !errors.Is(err, concept2.ErrTimeout) {
		t.Errorf("expected errors.Is(err, ErrTimeout), got: %v", err)
	}
	if elapsed > 120*time.Millisecond {
		t.Errorf("call took %v, want under 120ms (bounded timeout)", elapsed)
	}
}

func TestDateIn(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation America/New_York: %v", err)
	}

	utcTime := time.Date(2026, 3, 1, 3, 30, 0, 0, time.UTC)

	dateNY, tzNY := concept2.DateIn(utcTime, ny)
	if dateNY != "2026-02-28 22:30:00" {
		t.Errorf("dateNY = %q, want %q", dateNY, "2026-02-28 22:30:00")
	}
	if tzNY != "America/New_York" {
		t.Errorf("tzNY = %q, want %q", tzNY, "America/New_York")
	}

	dateUTC, tzUTC := concept2.DateIn(utcTime, time.UTC)
	if dateUTC != "2026-03-01 03:30:00" {
		t.Errorf("dateUTC = %q, want %q", dateUTC, "2026-03-01 03:30:00")
	}
	if tzUTC != "UTC" {
		t.Errorf("tzUTC = %q, want %q", tzUTC, "UTC")
	}

	// nil location defaults to UTC
	dateNil, tzNil := concept2.DateIn(utcTime, nil)
	if dateNil != "2026-03-01 03:30:00" {
		t.Errorf("dateNil = %q, want %q", dateNil, "2026-03-01 03:30:00")
	}
	if tzNil != "UTC" {
		t.Errorf("tzNil = %q, want %q", tzNil, "UTC")
	}
}

func TestGetMe_MissingToken(t *testing.T) {
	client := concept2.New("https://log.concept2.com", "id", "sec", "http://localhost/cb")
	_, err := client.GetMe()
	if err == nil {
		t.Fatalf("expected error for empty token, got nil")
	}
	var apiErr *concept2.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *concept2.APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, http.StatusUnauthorized)
	}
}

func TestContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	client := concept2.New(server.URL, "id", "sec", "http://localhost/cb")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := client.ExchangeCodeContext(ctx, "code")
	if err == nil {
		t.Fatalf("expected error on cancelled context, got nil")
	}
}

// TestResultJSONShape pins the keys a full Result marshals to, as the Logbook's
// Add Result endpoint names them, and that verified is never sent.
func TestResultJSONShape(t *testing.T) {
	i32 := func(v int32) *int32 { return &v }
	r := concept2.Result{
		Type: "rower", Date: "2026-01-02 03:07:25", Timezone: "UTC", Distance: 1000, Time: 1999,
		WeightClass: concept2.WeightClassHeavy, WorkoutType: "FixedDistanceSplits",
		StrokeRate: i32(29), StrokeCount: i32(95), CaloriesTotal: i32(68), DragFactor: i32(125),
		Workout: &concept2.Workout{Splits: []concept2.Split{
			{Distance: 500, Time: 1000, StrokeRate: i32(27)},
			{Distance: 500, Time: 999},
		}},
		StrokeData: []concept2.Stroke{{T: 500, D: 2500, P: i32(1205), SPM: i32(26)}},
		Metadata: &concept2.Metadata{
			PMVersion: "5", FirmwareVersion: "171", SerialNumber: "430395351", Other: "PixelPace BLE; hw=PM5v3",
		},
	}
	got, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"rower","date":"2026-01-02 03:07:25","timezone":"UTC","distance":1000,"time":1999,` +
		`"weight_class":"H","workout_type":"FixedDistanceSplits","stroke_rate":29,"stroke_count":95,` +
		`"calories_total":68,"drag_factor":125,` +
		`"workout":{"splits":[{"distance":500,"time":1000,"stroke_rate":27},{"distance":500,"time":999}]},` +
		`"stroke_data":[{"t":500,"d":2500,"p":1205,"spm":26}],` +
		`"metadata":{"pm_version":"5","firmware_version":"171","serial_number":"430395351","other":"PixelPace BLE; hw=PM5v3"}}`
	if string(got) != want {
		t.Errorf("Result JSON =\n %s\nwant\n %s", got, want)
	}
}
