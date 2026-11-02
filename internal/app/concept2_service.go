package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/concept2"
	"github.com/softsrv/starter/internal/db"
)

var (
	// ErrConcept2NotConnected is returned when the user has no Concept2 connection.
	ErrConcept2NotConnected = errors.New("concept2 logbook is not connected")
	// ErrConcept2NoSealer is returned when tokens would be stored without a sealer.
	// Tokens are never written in plaintext.
	ErrConcept2NoSealer = errors.New("concept2 token sealer is not configured")
)

// TokenSealer seals and opens Concept2 OAuth tokens for storage. The user id
// is bound to every sealed blob, so a blob sealed for one user does not open
// for another.
type TokenSealer interface {
	Seal(plaintext []byte, userID uuid.UUID) ([]byte, error)
	Open(sealed []byte, userID uuid.UUID) ([]byte, error)
}

// Concept2ResultClient posts one result to a user's Concept2 Logbook and
// refreshes an expired access token. *concept2.Client implements it.
type Concept2ResultClient interface {
	PostResult(ctx context.Context, accessToken string, r concept2.Result) (int64, error)
	RefreshContext(ctx context.Context, refreshToken string) (*concept2.Token, error)
}

// Concept2ServiceConfig configures a Concept2Service. Zero values take the
// defaults: a 5 minute lease, retries 1 minute after the first failure
// doubling up to 1 hour (with no limit on how many), 20 posts per pass and
// real time.
type Concept2ServiceConfig struct {
	// Sealer encrypts tokens before every write. Required to save a connection.
	Sealer TokenSealer
	// Client posts results to the Logbook. Required by PostDue.
	Client Concept2ResultClient
	// DevMode turns posting off: Enqueue queues nothing and PostDue sends
	// nothing. main sets it from devModeForAppEnv, its only owner.
	DevMode bool
	// Lease is how long a claimed post stays claimed before another pass may
	// take it again, so a poster that stops mid-post does not strand it.
	Lease time.Duration
	// RetryBase is the gap before a post that failed once (5xx, network) is
	// tried again; each further failure doubles it, up to RetryMax.
	RetryBase time.Duration
	RetryMax  time.Duration
	// BatchSize caps the posts one PostDue pass claims.
	BatchSize int
	Now       func() time.Time
}

// Concept2Service stores each PixelPace user's Concept2 Logbook connection
// and posts their finished race results to it.
// Every method takes the signed-in user's id and every query is keyed by it,
// so acting for one user never touches another user's connection.
type Concept2Service struct {
	q   *db.Queries
	cfg Concept2ServiceConfig
}

// NewConcept2Service constructs a Concept2Service.
func NewConcept2Service(q *db.Queries, cfg Concept2ServiceConfig) *Concept2Service {
	if cfg.Lease <= 0 {
		cfg.Lease = 5 * time.Minute
	}
	if cfg.RetryBase <= 0 {
		cfg.RetryBase = time.Minute
	}
	if cfg.RetryMax <= 0 {
		cfg.RetryMax = time.Hour
	}
	if cfg.RetryMax < cfg.RetryBase {
		cfg.RetryMax = cfg.RetryBase
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 20
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Concept2Service{q: q, cfg: cfg}
}

var _ RaceResultPoster = (*Concept2Service)(nil)

// Enqueue queues one pending result post for each of the race's finished
// participants whose connection has auto_post on. A connection that needs
// reconnecting is queued too: PostDue holds its posts until the user
// reconnects, so a finish is never lost. RaceService calls it with its
// finalize transaction's queries,
// so the posts commit with the finish or not at all. It is idempotent: a
// participant already queued for this race is not queued again. In DevMode
// it queues nothing.
func (s *Concept2Service) Enqueue(ctx context.Context, q *db.Queries, raceID uuid.UUID) error {
	if s.cfg.DevMode {
		return nil
	}
	userIDs, err := q.ListConcept2PostCandidates(ctx, raceID)
	if err != nil {
		return fmt.Errorf("list concept2 post candidates: %w", err)
	}
	for _, userID := range userIDs {
		if _, err := q.EnqueueConcept2ResultPost(ctx, db.EnqueueConcept2ResultPostParams{
			ID: uuid.Must(uuid.NewV7()), UserID: userID, RaceID: raceID,
		}); err != nil {
			return fmt.Errorf("enqueue concept2 result post: %w", err)
		}
	}
	return nil
}

// RunPoster runs PostDue every interval until ctx is cancelled, logging a
// failed pass and trying again at the next interval. It returns ctx's error.
func (s *Concept2Service) RunPoster(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("concept2 poster interval must be positive, got %v", interval)
	}
	for {
		if _, err := s.PostDue(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "concept2 post pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// PostDue makes one pass over the posts that are due: it claims up to
// BatchSize of them (only those whose connection has auto_post on now and
// does not need reconnecting) and posts each to its user's Logbook. A post
// the Logbook accepts, or already has (409), becomes posted; one it rejects
// (422 and other permanent 4xx) is failed with Concept2's message; a 5xx,
// rate limit, network failure or unreadable reply keeps it pending, retried
// after a capped exponential gap for as long as it takes. Tokens that will
// not open or refresh set the connection needs_reconnect and leave its posts
// pending, attempts unchanged, with nothing sent. It returns how many posts
// it posted, and an error only when the database fails. In DevMode it sends
// nothing.
func (s *Concept2Service) PostDue(ctx context.Context) (int, error) {
	if s.cfg.DevMode {
		return 0, nil
	}
	if s.cfg.Client == nil {
		return 0, errors.New("concept2 result client is not configured")
	}
	if s.cfg.Sealer == nil {
		return 0, ErrConcept2NoSealer
	}
	now := s.cfg.Now()
	claimed, err := s.q.ClaimDueConcept2Posts(ctx, db.ClaimDueConcept2PostsParams{
		Now:        pgtype.Timestamptz{Time: now, Valid: true},
		LeaseUntil: pgtype.Timestamptz{Time: now.Add(s.cfg.Lease), Valid: true},
		MaxPosts:   int32(s.cfg.BatchSize),
	})
	if err != nil {
		return 0, fmt.Errorf("claim due concept2 posts: %w", err)
	}
	posted := 0
	for _, post := range claimed {
		ok, err := s.postOne(ctx, post)
		if err != nil {
			return posted, err
		}
		if ok {
			posted++
		}
	}
	return posted, nil
}

// postOne posts one claimed post. It reports whether the post ended posted,
// and returns an error only when recording the outcome fails.
func (s *Concept2Service) postOne(ctx context.Context, post db.ClaimDueConcept2PostsRow) (bool, error) {
	payload, err := s.q.GetConcept2PostPayload(ctx, db.GetConcept2PostPayloadParams{ID: post.ID, UserID: post.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		// The user disconnected, or the finish is gone: nothing can be posted.
		return false, s.failPost(ctx, post, "concept2 connection or finished result no longer exists")
	}
	if err != nil {
		return false, fmt.Errorf("get concept2 post payload: %w", err)
	}
	strokes, err := s.q.ListConcept2PostStrokes(ctx, db.ListConcept2PostStrokesParams{ID: post.ID, UserID: post.UserID})
	if err != nil {
		return false, fmt.Errorf("list concept2 post strokes: %w", err)
	}
	result, err := concept2ResultFor(payload, strokes)
	if err != nil {
		return false, s.failPost(ctx, post, err.Error())
	}
	conn, err := s.q.GetConcept2Connection(ctx, post.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, s.failPost(ctx, post, "concept2 connection no longer exists")
	}
	if err != nil {
		return false, fmt.Errorf("get concept2 connection: %w", err)
	}
	if conn.NeedsReconnect {
		// Another post of this pass already found the tokens unusable.
		return false, s.awaitReconnect(ctx, post, "concept2 connection needs reconnecting", false)
	}
	access, err := s.cfg.Sealer.Open(conn.AccessTokenEnc, post.UserID)
	if err != nil {
		return false, s.awaitReconnect(ctx, post, "concept2 tokens could not be opened", true)
	}
	refreshed := false
	if !conn.ExpiresAt.Valid || !conn.ExpiresAt.Time.After(s.cfg.Now()) {
		token, done, err := s.refreshTokens(ctx, post, conn)
		if done || err != nil {
			return false, err
		}
		access, refreshed = token, true
	}
	c2ResultID, err := s.cfg.Client.PostResult(ctx, string(access), result)
	if errors.Is(err, concept2.ErrUnauthorized) && !refreshed {
		// The Logbook refused a token that had not expired: refresh it once.
		token, done, rerr := s.refreshTokens(ctx, post, conn)
		if done || rerr != nil {
			return false, rerr
		}
		c2ResultID, err = s.cfg.Client.PostResult(ctx, string(token), result)
	}
	var apiErr *concept2.APIError
	switch {
	case err == nil:
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict:
		// The Logbook already has this result: it is posted, never sent again.
		c2ResultID = 0
	case errors.Is(err, concept2.ErrUnauthorized):
		return false, s.awaitReconnect(ctx, post, "concept2 refused the refreshed access token", true)
	case errors.As(err, &apiErr) && permanentLogbookError(apiErr.StatusCode):
		return false, s.failPost(ctx, post, apiErr.Error())
	default:
		return false, s.retryPost(ctx, post, err)
	}
	if _, err := s.q.MarkConcept2PostPosted(ctx, db.MarkConcept2PostPostedParams{
		ID: post.ID, UserID: post.UserID,
		C2ResultID: pgtype.Int8{Int64: c2ResultID, Valid: c2ResultID != 0},
	}); err != nil {
		return true, fmt.Errorf("mark concept2 post posted: %w", err)
	}
	return true, nil
}

// permanentLogbookError reports whether a Logbook status means the same post
// will never be accepted: a 4xx other than a timeout or rate limit.
func permanentLogbookError(status int) bool {
	return status >= 400 && status < 500 &&
		status != http.StatusRequestTimeout && status != http.StatusTooManyRequests
}

// refreshTokens trades the connection's refresh token for new tokens and
// saves the sealed access token, rotated refresh token and expiry in one
// write. It returns the new access token. done reports that the post's
// outcome is already recorded (refused refresh: needs_reconnect; Logbook
// unavailable: retry), so the caller must not post.
func (s *Concept2Service) refreshTokens(ctx context.Context, post db.ClaimDueConcept2PostsRow, conn db.Concept2Connection) (access []byte, done bool, err error) {
	refresh, err := s.cfg.Sealer.Open(conn.RefreshTokenEnc, post.UserID)
	if err != nil {
		return nil, true, s.awaitReconnect(ctx, post, "concept2 tokens could not be opened", true)
	}
	tok, err := s.cfg.Client.RefreshContext(ctx, string(refresh))
	var apiErr *concept2.APIError
	switch {
	case err == nil:
	case errors.As(err, &apiErr) && permanentLogbookError(apiErr.StatusCode):
		return nil, true, s.awaitReconnect(ctx, post, "concept2 refused the token refresh", true)
	case errors.Is(err, concept2.ErrNoAccessToken):
		// Concept2 answered but issued no access token: the renewal was refused.
		return nil, true, s.awaitReconnect(ctx, post, "concept2 refused the token refresh", true)
	default:
		return nil, true, s.retryPost(ctx, post, err)
	}
	newRefresh := tok.RefreshToken
	if newRefresh == "" {
		newRefresh = string(refresh) // not rotated: keep the current one
	}
	expiresAt := tok.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = s.cfg.Now().Add(time.Hour)
	}
	accessEnc, err := s.cfg.Sealer.Seal([]byte(tok.AccessToken), post.UserID)
	if err != nil {
		return nil, true, fmt.Errorf("seal concept2 access token: %w", err)
	}
	refreshEnc, err := s.cfg.Sealer.Seal([]byte(newRefresh), post.UserID)
	if err != nil {
		return nil, true, fmt.Errorf("seal concept2 refresh token: %w", err)
	}
	if err := s.saveRefreshedTokens(ctx, db.UpdateConcept2TokensParams{
		UserID: post.UserID, AccessTokenEnc: accessEnc, RefreshTokenEnc: refreshEnc,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		// Concept2 has rotated the refresh token, so the stored one is dead.
		// Ask the user to reconnect rather than leave the connection silently
		// broken, and still post this result with the new access token.
		slog.ErrorContext(ctx, "concept2 refreshed tokens could not be saved; connection needs reconnecting",
			"user_id", post.UserID, "post_id", post.ID, "error", err)
		if _, merr := s.q.MarkConcept2NeedsReconnect(ctx, post.UserID); merr != nil {
			return nil, true, fmt.Errorf("mark concept2 needs reconnect after token save failed: %w", errors.Join(err, merr))
		}
	}
	return []byte(tok.AccessToken), false, nil
}

// tokenSaveAttempts bounds how many times refreshed tokens are written before
// the write is given up; tokenSaveGap is the wait before the first retry,
// doubling after each.
const (
	tokenSaveAttempts = 3
	tokenSaveGap      = 50 * time.Millisecond
)

// saveRefreshedTokens writes refreshed tokens, retrying a failed write a
// bounded number of times, since the old refresh token is no longer valid.
func (s *Concept2Service) saveRefreshedTokens(ctx context.Context, p db.UpdateConcept2TokensParams) error {
	gap := tokenSaveGap
	var err error
	for attempt := 1; attempt <= tokenSaveAttempts; attempt++ {
		if _, err = s.q.UpdateConcept2Tokens(ctx, p); err == nil {
			return nil
		}
		if attempt == tokenSaveAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(gap):
		}
		gap *= 2
	}
	return fmt.Errorf("update concept2 tokens: %w", err)
}

// maxConcept2PostErrorLen caps the error text kept on a post.
const maxConcept2PostErrorLen = 500

func postErrorText(msg string) pgtype.Text {
	if len(msg) > maxConcept2PostErrorLen {
		msg = msg[:maxConcept2PostErrorLen]
	}
	return pgtype.Text{String: msg, Valid: true}
}

// retryGap is the wait after a post's attempts-th failed attempt: RetryBase,
// doubling with each attempt, capped at RetryMax.
func (s *Concept2Service) retryGap(attempts int32) time.Duration {
	gap := s.cfg.RetryBase
	for i := int32(1); i < attempts && gap < s.cfg.RetryMax; i++ {
		gap *= 2
	}
	return min(gap, s.cfg.RetryMax)
}

// retryPost keeps cause on the post and leaves it pending until the retry
// gap has passed. A transient failure never fails a post, however many times
// it repeats; the gap stops growing at RetryMax.
func (s *Concept2Service) retryPost(ctx context.Context, post db.ClaimDueConcept2PostsRow, cause error) error {
	gap := s.retryGap(post.Attempts)
	msg := postErrorText(cause.Error())
	slog.WarnContext(ctx, "concept2 result post failed", "post_id", post.ID, "user_id", post.UserID,
		"race_id", post.RaceID, "attempt", post.Attempts, "retry_in", gap, "error", msg.String)
	if _, err := s.q.RecordConcept2PostError(ctx, db.RecordConcept2PostErrorParams{
		ID: post.ID, UserID: post.UserID, LastError: msg,
		RetryAt: pgtype.Timestamptz{Time: s.cfg.Now().Add(gap), Valid: true},
	}); err != nil {
		return fmt.Errorf("record concept2 post error: %w", err)
	}
	return nil
}

// failPost fails the post for good with msg; it is never claimed again.
func (s *Concept2Service) failPost(ctx context.Context, post db.ClaimDueConcept2PostsRow, msg string) error {
	text := postErrorText(msg)
	slog.WarnContext(ctx, "concept2 result post rejected", "post_id", post.ID, "user_id", post.UserID,
		"race_id", post.RaceID, "error", text.String)
	if _, err := s.q.MarkConcept2PostFailed(ctx, db.MarkConcept2PostFailedParams{
		ID: post.ID, UserID: post.UserID, LastError: text,
	}); err != nil {
		return fmt.Errorf("mark concept2 post failed: %w", err)
	}
	return nil
}

// awaitReconnect gives the post back its claimed attempt, since nothing was
// sent, and (when mark is set) flags the connection needs_reconnect: its posts
// wait, pending, until the user reconnects. reason never carries a token.
func (s *Concept2Service) awaitReconnect(ctx context.Context, post db.ClaimDueConcept2PostsRow, reason string, mark bool) error {
	if _, err := s.q.RequeueConcept2PostForReconnect(ctx, db.RequeueConcept2PostForReconnectParams{
		ID: post.ID, UserID: post.UserID, LastError: postErrorText(reason),
		Now: pgtype.Timestamptz{Time: s.cfg.Now(), Valid: true},
	}); err != nil {
		return fmt.Errorf("requeue concept2 post: %w", err)
	}
	if !mark {
		return nil
	}
	slog.WarnContext(ctx, "concept2 connection needs reconnecting", "user_id", post.UserID, "reason", reason)
	if _, err := s.q.MarkConcept2NeedsReconnect(ctx, post.UserID); err != nil {
		return fmt.Errorf("mark concept2 needs reconnect: %w", err)
	}
	return nil
}

// concept2ResultFor builds the rower result for a finished distance race:
// the race target in meters, the recorded finish time in tenths of a second
// (truncated, as the monitor shows it) and the finish as the date, in the
// connection's timezone, with the detail the racer's samples and PM5 give.
// Detail that is not known is left out. verified is never set: only clients
// Concept2 trusts may verify a result.
func concept2ResultFor(p db.GetConcept2PostPayloadRow, strokes []db.ListConcept2PostStrokesRow) (concept2.Result, error) {
	if p.Kind != "distance" {
		return concept2.Result{}, fmt.Errorf("concept2 posts need a distance race, got %q", p.Kind)
	}
	if !p.ElapsedMilliseconds.Valid || !p.FinishedAt.Valid {
		return concept2.Result{}, errors.New("finished participant has no recorded finish time")
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		loc = time.UTC
	}
	date, tz := concept2.DateIn(p.FinishedAt.Time, loc)
	finishMs := int64(p.ElapsedMilliseconds.Int32)
	targetMm := p.TargetValue // race targets are in millimeters
	r := concept2.Result{
		Type:        "rower",
		Date:        date,
		Timezone:    tz,
		Distance:    targetMm / 1000,
		Time:        finishMs / 100,
		WeightClass: concept2.WeightClassHeavy,
		WorkoutType: "FixedDistanceSplits",
		StrokeRate:  averageRate(strokes),
		StrokeCount: maxOf(strokes, func(s db.ListConcept2PostStrokesRow) pgtype.Int4 { return s.StrokeCount }),
		CaloriesTotal: maxOf(strokes, func(s db.ListConcept2PostStrokesRow) pgtype.Int4 {
			return s.Calories
		}),
		DragFactor: averageDrag(strokes),
		StrokeData: strokeData(strokes),
		Metadata:   monitorMetadata(p),
	}
	if splits := splits500m(strokes, targetMm, finishMs); len(splits) > 0 {
		r.Workout = &concept2.Workout{Splits: splits}
	}
	return r, nil
}

// concept2SplitMm is the length of one posted split: 500 m.
const concept2SplitMm = 500_000

// maxOf returns the largest known value of field (the PM5's stroke count
// and calories only grow), or nil when no sample knows it.
func maxOf(strokes []db.ListConcept2PostStrokesRow, field func(db.ListConcept2PostStrokesRow) pgtype.Int4) *int32 {
	var best *int32
	for _, s := range strokes {
		if v := field(s); v.Valid && (best == nil || v.Int32 > *best) {
			n := v.Int32
			best = &n
		}
	}
	return best
}

// meanOf rounds the mean of vs, or returns nil for none.
func meanOf(vs []int32) *int32 {
	if len(vs) == 0 {
		return nil
	}
	var sum int64
	for _, v := range vs {
		sum += int64(v)
	}
	n := int32(math.Round(float64(sum) / float64(len(vs))))
	return &n
}

// averageRate is the mean stroke rate of the samples taken while rowing
// (a rate of 0 is the monitor before the first stroke).
func averageRate(strokes []db.ListConcept2PostStrokesRow) *int32 {
	var vs []int32
	for _, s := range strokes {
		if s.StrokeRate.Valid && s.StrokeRate.Int32 > 0 {
			vs = append(vs, s.StrokeRate.Int32)
		}
	}
	return meanOf(vs)
}

// averageDrag is the mean known drag factor, to the nearest whole number.
func averageDrag(strokes []db.ListConcept2PostStrokesRow) *int32 {
	var vs []int32
	for _, s := range strokes {
		if s.DragFactor.Valid && s.DragFactor.Int32 > 0 {
			vs = append(vs, s.DragFactor.Int32)
		}
	}
	return meanOf(vs)
}

// paceFromWatts is the 500 m pace, in tenths of a second, that a rower's
// power gives by the Concept2 formula watts = 2.80 / (seconds per meter)^3.
func paceFromWatts(watts int32) int32 {
	return int32(math.Round(5000 * math.Cbrt(2.8/float64(watts))))
}

// strokeData is one Logbook stroke per sample: cumulative time in tenths and
// distance in decimeters, with pace (from power) and rate when known.
func strokeData(strokes []db.ListConcept2PostStrokesRow) []concept2.Stroke {
	out := make([]concept2.Stroke, 0, len(strokes))
	for _, s := range strokes {
		st := concept2.Stroke{T: s.ElapsedMilliseconds.Int64 / 100, D: s.DistanceMillimeters.Int64 / 100}
		if s.Power.Valid && s.Power.Int32 > 0 {
			p := paceFromWatts(s.Power.Int32)
			st.P = &p
		}
		if s.StrokeRate.Valid {
			spm := s.StrokeRate.Int32
			st.SPM = &spm
		}
		out = append(out, st)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// splits500m cuts the race into 500 m splits (the last one shorter when the
// target is not a multiple of 500 m). The time a racer crossed each split
// line is interpolated between the samples either side of it; the last line
// is the recorded finish, so the split times add up to the posted time. No
// samples, no splits.
func splits500m(strokes []db.ListConcept2PostStrokesRow, targetMm, finishMs int64) []concept2.Split {
	if len(strokes) == 0 || targetMm <= 0 {
		return nil
	}
	type point struct{ ms, mm int64 }
	pts := make([]point, 0, len(strokes)+1)
	pts = append(pts, point{})
	for _, s := range strokes {
		pts = append(pts, point{s.ElapsedMilliseconds.Int64, s.DistanceMillimeters.Int64})
	}
	crossedAt := func(lineMm int64) int64 {
		for i := 1; i < len(pts); i++ {
			a, b := pts[i-1], pts[i]
			if b.mm < lineMm {
				continue
			}
			if b.mm == a.mm {
				return b.ms
			}
			return a.ms + (b.ms-a.ms)*(lineMm-a.mm)/(b.mm-a.mm)
		}
		return finishMs
	}
	var out []concept2.Split
	prevMm, prevTenths := int64(0), int64(0)
	for prevMm < targetMm {
		lineMm := min(prevMm+concept2SplitMm, targetMm)
		tenths := finishMs / 100
		if lineMm < targetMm {
			tenths = min(crossedAt(lineMm)/100, tenths)
		}
		split := concept2.Split{Distance: (lineMm - prevMm) / 1000, Time: tenths - prevTenths}
		var rates []int32
		for _, s := range strokes {
			if s.DistanceMillimeters.Int64 > prevMm && s.DistanceMillimeters.Int64 <= lineMm &&
				s.StrokeRate.Valid && s.StrokeRate.Int32 > 0 {
				rates = append(rates, s.StrokeRate.Int32)
			}
		}
		split.StrokeRate = meanOf(rates)
		out = append(out, split)
		prevMm, prevTenths = lineMm, tenths
	}
	return out
}

// monitorMetadata describes the PM5 the racer rowed on, as the browser read
// it; nil when nothing about it is known.
func monitorMetadata(p db.GetConcept2PostPayloadRow) *concept2.Metadata {
	if !p.Pm5SerialNumber.Valid && !p.Pm5FirmwareVersion.Valid && !p.Pm5HardwareRevision.Valid {
		return nil
	}
	other := "PixelPace BLE"
	if p.Pm5HardwareRevision.Valid {
		other += "; hw=" + p.Pm5HardwareRevision.String
	}
	return &concept2.Metadata{
		PMVersion:       "5",
		SerialNumber:    p.Pm5SerialNumber.String,
		FirmwareVersion: p.Pm5FirmwareVersion.String,
		Other:           other,
	}
}

// Concept2Connection is what a user may see of their own connection.
// It never carries the tokens.
type Concept2Connection struct {
	C2UserID       int64
	Username       string
	ConnectedAt    time.Time
	AutoPost       bool
	NeedsReconnect bool
	Timezone       string
}

// Concept2Grant is a newly obtained Concept2 connection to store for a user.
type Concept2Grant struct {
	C2UserID     int64
	Username     string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scope        string
	Timezone     string // IANA name, already validated by the caller; empty means UTC
}

// Status returns userID's connection, or ErrConcept2NotConnected.
func (s *Concept2Service) Status(ctx context.Context, userID uuid.UUID) (Concept2Connection, error) {
	row, err := s.q.GetConcept2Connection(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Concept2Connection{}, ErrConcept2NotConnected
		}
		return Concept2Connection{}, fmt.Errorf("get concept2 connection: %w", err)
	}
	return toConcept2Connection(row), nil
}

// SaveConnection connects (or reconnects) userID's Concept2 account. Both
// tokens are sealed with the user id before the write; a reconnect overwrites
// the previous tokens, account and timezone and clears needs_reconnect.
func (s *Concept2Service) SaveConnection(ctx context.Context, userID uuid.UUID, g Concept2Grant) (Concept2Connection, error) {
	if s.cfg.Sealer == nil {
		return Concept2Connection{}, ErrConcept2NoSealer
	}
	access, err := s.cfg.Sealer.Seal([]byte(g.AccessToken), userID)
	if err != nil {
		return Concept2Connection{}, fmt.Errorf("seal concept2 access token: %w", err)
	}
	refresh, err := s.cfg.Sealer.Seal([]byte(g.RefreshToken), userID)
	if err != nil {
		return Concept2Connection{}, fmt.Errorf("seal concept2 refresh token: %w", err)
	}
	tz := strings.TrimSpace(g.Timezone)
	if tz == "" {
		tz = "UTC"
	}
	row, err := s.q.UpsertConcept2Connection(ctx, db.UpsertConcept2ConnectionParams{
		UserID:          userID,
		C2UserID:        g.C2UserID,
		C2Username:      g.Username,
		AccessTokenEnc:  access,
		RefreshTokenEnc: refresh,
		ExpiresAt:       pgtype.Timestamptz{Time: g.ExpiresAt, Valid: true},
		Scope:           g.Scope,
		Timezone:        tz,
	})
	if err != nil {
		return Concept2Connection{}, fmt.Errorf("upsert concept2 connection: %w", err)
	}
	return toConcept2Connection(row), nil
}

// Disconnect deletes userID's connection. It is idempotent: disconnecting a
// user who is not connected succeeds.
func (s *Concept2Service) Disconnect(ctx context.Context, userID uuid.UUID) error {
	if _, err := s.q.DeleteConcept2Connection(ctx, userID); err != nil {
		return fmt.Errorf("delete concept2 connection: %w", err)
	}
	return nil
}

// SetAutoPost updates the auto_post preference for userID.
func (s *Concept2Service) SetAutoPost(ctx context.Context, userID uuid.UUID, autoPost bool) (Concept2Connection, error) {
	row, err := s.q.SetConcept2AutoPost(ctx, db.SetConcept2AutoPostParams{
		UserID:   userID,
		AutoPost: autoPost,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Concept2Connection{}, ErrConcept2NotConnected
		}
		return Concept2Connection{}, fmt.Errorf("set concept2 auto post: %w", err)
	}
	return toConcept2Connection(row), nil
}

func toConcept2Connection(row db.Concept2Connection) Concept2Connection {
	return Concept2Connection{
		C2UserID:       row.C2UserID,
		Username:       row.C2Username,
		ConnectedAt:    row.ConnectedAt.Time,
		AutoPost:       row.AutoPost,
		NeedsReconnect: row.NeedsReconnect,
		Timezone:       row.Timezone,
	}
}
