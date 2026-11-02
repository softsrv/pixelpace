package concept2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is Concept2's production Logbook base URL.
	DefaultBaseURL = "https://log.concept2.com"

	// DefaultScope is the OAuth scope required for reading user info and posting workout results.
	DefaultScope = "user:read,results:write"

	// DefaultTimeout is the bounded timeout for all Concept2 HTTP operations.
	DefaultTimeout = 10 * time.Second

	maxErrorBodyBytes = 65536
)

var (
	// ErrTimeout indicates an HTTP request to the Concept2 API timed out.
	ErrTimeout = errors.New("concept2: request timed out")

	// ErrMalformed indicates a response body from the Concept2 API could not be parsed.
	ErrMalformed = errors.New("concept2: malformed response")

	// ErrUnauthorized indicates Concept2 rejected credentials or token (HTTP 401).
	ErrUnauthorized = errors.New("concept2: unauthorized")
)

// APIError represents an HTTP error response from the Concept2 API.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("concept2: api error (status %d): %s", e.StatusCode, msg)
}

// Is supports errors.Is for matching APIError status codes or ErrUnauthorized.
func (e *APIError) Is(target error) bool {
	if target == nil {
		return false
	}
	if t, ok := target.(*APIError); ok {
		return e.StatusCode == t.StatusCode
	}
	if target == ErrUnauthorized && e.StatusCode == http.StatusUnauthorized {
		return true
	}
	return false
}

// Token holds OAuth access and refresh credentials returned by Concept2.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope"`
}

// User represents basic Logbook user identity returned by /api/users/me.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// Option configures a Client instance.
type Option func(*Client)

// WithHTTPClient sets a custom *http.Client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithTimeout overrides the HTTP client request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.httpClient.Timeout = d
	}
}

// Client is an HTTP client for Concept2 Logbook OAuth operations.
type Client struct {
	baseURL      string
	clientID     string
	clientSecret string
	redirectURL  string
	httpClient   *http.Client
}

// New creates a new Concept2 Logbook API client.
func New(baseURL, clientID, clientSecret, redirectURL string, opts ...Option) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	c := &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURL:  redirectURL,
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// NewClient is an alias for New.
func NewClient(baseURL, clientID, clientSecret, redirectURL string, opts ...Option) *Client {
	return New(baseURL, clientID, clientSecret, redirectURL, opts...)
}

// AuthorizeURL constructs the user authorization URL redirecting to /oauth/authorize.
func (c *Client) AuthorizeURL(state string) string {
	vals := url.Values{}
	vals.Set("client_id", c.clientID)
	vals.Set("response_type", "code")
	vals.Set("scope", DefaultScope)
	vals.Set("redirect_uri", c.redirectURL)
	vals.Set("state", state)

	base := strings.TrimRight(c.baseURL, "/")
	return base + "/oauth/authorize?" + vals.Encode()
}

// ExchangeCode trades an authorization code for access and refresh tokens.
func (c *Client) ExchangeCode(code string) (*Token, error) {
	return c.ExchangeCodeContext(context.Background(), code)
}

// ExchangeCodeContext trades an authorization code for tokens with context.
func (c *Client) ExchangeCodeContext(ctx context.Context, code string) (*Token, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", c.redirectURL)
	form.Set("scope", DefaultScope)

	return c.postToken(ctx, form, code)
}

// Refresh trades a refresh token for new access and refresh tokens.
func (c *Client) Refresh(refreshToken string) (*Token, error) {
	return c.RefreshContext(context.Background(), refreshToken)
}

// RefreshContext trades a refresh token for new access and refresh tokens with context.
func (c *Client) RefreshContext(ctx context.Context, refreshToken string) (*Token, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("scope", DefaultScope)

	return c.postToken(ctx, form, refreshToken)
}

type tokenResponse struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	ExpiresIn    json.Number `json:"expires_in"`
	ExpiresAt    json.Number `json:"expires_at"`
	Scope        string      `json:"scope"`
}

func (c *Client) postToken(ctx context.Context, form url.Values, secretPayload string) (*Token, error) {
	endpoint := c.baseURL + "/oauth/access_token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, c.sanitizeError(err, secretPayload)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.handleNetworkError(err, secretPayload)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.parseAPIError(resp, secretPayload)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		return nil, c.handleNetworkError(err, secretPayload)
	}

	var tr tokenResponse
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&tr); err != nil {
		return nil, c.wrapMalformed(err, secretPayload)
	}

	if tr.AccessToken == "" {
		return nil, fmt.Errorf("%w: missing access_token", ErrMalformed)
	}

	now := time.Now()
	var expiresAt time.Time
	if sec, err := tr.ExpiresIn.Int64(); err == nil && sec > 0 {
		expiresAt = now.Add(time.Duration(sec) * time.Second)
	} else if unixSec, err := tr.ExpiresAt.Int64(); err == nil && unixSec > 0 {
		expiresAt = time.Unix(unixSec, 0)
	}

	scope := tr.Scope
	if scope == "" {
		scope = DefaultScope
	}

	return &Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    expiresAt,
		Scope:        scope,
	}, nil
}

// GetMe fetches the Logbook user id and username for the given bearer access token.
func (c *Client) GetMe(tokens ...string) (*User, error) {
	return c.GetMeContext(context.Background(), tokens...)
}

type getMeResponse struct {
	Data struct {
		ID       json.Number `json:"id"`
		Username string      `json:"username"`
	} `json:"data"`
}

// GetMeContext fetches the Logbook user id and username with context.
func (c *Client) GetMeContext(ctx context.Context, tokens ...string) (*User, error) {
	var accessToken string
	if len(tokens) > 0 {
		accessToken = tokens[0]
	}
	if accessToken == "" {
		return nil, &APIError{
			StatusCode: http.StatusUnauthorized,
			Message:    "missing access token",
		}
	}

	endpoint := c.baseURL + "/api/users/me"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, c.sanitizeError(err, accessToken)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.handleNetworkError(err, accessToken)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.parseAPIError(resp, accessToken)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		return nil, c.handleNetworkError(err, accessToken)
	}

	var meResp getMeResponse
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&meResp); err != nil {
		return nil, c.wrapMalformed(err, accessToken)
	}

	userID, err := meResp.Data.ID.Int64()
	if err != nil || userID == 0 {
		return nil, fmt.Errorf("%w: missing or invalid user id", ErrMalformed)
	}

	return &User{
		ID:       userID,
		Username: meResp.Data.Username,
	}, nil
}

// WeightClassHeavy is the Logbook's heavyweight class, its default for a
// rower result; "L" (lightweight) is the other value.
const WeightClassHeavy = "H"

// Result is one workout to add to a user's Logbook (POST
// /api/users/me/results). Type, Date, Distance, Time and WeightClass are
// always sent; every other key is omitted when its value is unknown, so a
// result with no detail still posts.
type Result struct {
	Type          string     `json:"type"`
	Date          string     `json:"date"` // end of the workout, "2006-01-02 15:04:05" local wall time
	Timezone      string     `json:"timezone,omitempty"`
	Distance      int64      `json:"distance"` // meters
	Time          int64      `json:"time"`     // tenths of a second
	WeightClass   string     `json:"weight_class"`
	WorkoutType   string     `json:"workout_type,omitempty"`
	StrokeRate    *int32     `json:"stroke_rate,omitempty"`
	StrokeCount   *int32     `json:"stroke_count,omitempty"`
	CaloriesTotal *int32     `json:"calories_total,omitempty"`
	DragFactor    *int32     `json:"drag_factor,omitempty"`
	HeartRate     *HeartRate `json:"heart_rate,omitempty"`
	Workout       *Workout   `json:"workout,omitempty"`
	StrokeData    []Stroke   `json:"stroke_data,omitempty"`
	Metadata      *Metadata  `json:"metadata,omitempty"`
}

// Workout is a result's split data.
type Workout struct {
	Splits []Split `json:"splits,omitempty"`
}

// Split is one split of a result: its own distance and time, not cumulative.
type Split struct {
	Distance   int64  `json:"distance"` // meters
	Time       int64  `json:"time"`     // tenths of a second
	StrokeRate *int32 `json:"stroke_rate,omitempty"`
}

// HeartRate is a result's heart-rate summary in beats per minute.
type HeartRate struct {
	Average *int32 `json:"average,omitempty"`
	Min     *int32 `json:"min,omitempty"`
	Max     *int32 `json:"max,omitempty"`
	Ending  *int32 `json:"ending,omitempty"`
}

// Stroke is one stroke of a result's stroke data. Time and distance are
// cumulative from the start of the workout.
type Stroke struct {
	T   int64  `json:"t"`             // tenths of a second
	D   int64  `json:"d"`             // decimeters
	P   *int32 `json:"p,omitempty"`   // pace, tenths of a second per 500m
	SPM *int32 `json:"spm,omitempty"` // strokes per minute
	HR  *int32 `json:"hr,omitempty"`  // heart rate
}

// Metadata describes the client and monitor that recorded a result.
type Metadata struct {
	ClientVersion   string `json:"client_version,omitempty"`
	PMVersion       string `json:"pm_version,omitempty"`
	FirmwareVersion string `json:"firmware_version,omitempty"`
	SerialNumber    string `json:"serial_number,omitempty"`
	Other           string `json:"other,omitempty"`
}

type postResultResponse struct {
	Data struct {
		ID json.Number `json:"id"`
	} `json:"data"`
}

// PostResult adds r to the Logbook of the user the access token belongs to and
// returns the new result's Logbook id. A non-2xx response is an *APIError
// (409 is a duplicate result, 401 matches ErrUnauthorized); the token never
// appears in an error. A 2xx response whose body has no readable id still
// means the result was created, so it returns id 0 and no error.
func (c *Client) PostResult(ctx context.Context, accessToken string, r Result) (int64, error) {
	if accessToken == "" {
		return 0, &APIError{StatusCode: http.StatusUnauthorized, Message: "missing access token"}
	}
	body, err := json.Marshal(r)
	if err != nil {
		return 0, fmt.Errorf("concept2: encode result: %w", err)
	}
	endpoint := c.baseURL + "/api/users/me/results"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return 0, c.sanitizeError(err, accessToken)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.c2logbook.v1+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, c.handleNetworkError(err, accessToken)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, c.parseAPIError(resp, accessToken)
	}
	var created postResultResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxErrorBodyBytes))
	dec.UseNumber()
	if err := dec.Decode(&created); err != nil {
		return 0, nil
	}
	id, err := created.Data.ID.Int64()
	if err != nil {
		return 0, nil
	}
	return id, nil
}

// DateIn returns the wall-clock time formatted as "2006-01-02 15:04:05" in loc,
// along with the location's IANA timezone name. If loc is nil, UTC is used.
func DateIn(t time.Time, loc *time.Location) (date, tz string) {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04:05"), loc.String()
}

type apiErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Message          string `json:"message"`
}

func (c *Client) parseAPIError(resp *http.Response, secrets ...string) *APIError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	msg := ""
	if len(body) > 0 {
		var aer apiErrorResponse
		if err := json.Unmarshal(body, &aer); err == nil {
			if aer.ErrorDescription != "" {
				msg = aer.ErrorDescription
			} else if aer.Message != "" {
				msg = aer.Message
			} else if aer.Error != "" {
				msg = aer.Error
			}
		}
		if msg == "" {
			msg = string(body)
		}
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &APIError{
		StatusCode: resp.StatusCode,
		Message:    c.sanitizeString(msg, secrets...),
	}
}

func (c *Client) handleNetworkError(err error, secrets ...string) error {
	if isTimeout(err) {
		return fmt.Errorf("%w: %s", ErrTimeout, c.sanitizeString(err.Error(), secrets...))
	}
	return c.sanitizeError(err, secrets...)
}

func (c *Client) wrapMalformed(err error, secrets ...string) error {
	return fmt.Errorf("%w: %s", ErrMalformed, c.sanitizeString(err.Error(), secrets...))
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "Client.Timeout") || strings.Contains(s, "deadline exceeded") || strings.Contains(s, "timeout")
}

func (c *Client) sanitizeString(s string, secrets ...string) string {
	allSecrets := append([]string{c.clientSecret}, secrets...)
	for _, sec := range allSecrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[REDACTED]")
		}
	}
	return s
}

type sanitizedError struct {
	err error
	msg string
}

func (e *sanitizedError) Error() string {
	return e.msg
}

func (e *sanitizedError) Unwrap() error {
	return e.err
}

func (c *Client) sanitizeError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	sanitizedMsg := c.sanitizeString(err.Error(), secrets...)
	if sanitizedMsg == err.Error() {
		return err
	}
	return &sanitizedError{
		err: err,
		msg: sanitizedMsg,
	}
}
