package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/bcrypt"

	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/email"
	"github.com/softsrv/starter/internal/users"
)

const (
	maxFailedLoginAttempts = 10
	lockDuration           = time.Hour
)

// DeviceMeta holds metadata captured from the HTTP request.
type DeviceMeta struct {
	DeviceName string
	IPAddress  *netip.Addr
	UserAgent  string
}

// TokenResult is returned after a successful login or token refresh.
type TokenResult struct {
	AccessToken        string
	AccessTokenExpiry  time.Time
	RefreshToken       string
	RefreshTokenExpiry time.Time
	EmailVerified      bool
}

// pgxBeginner is satisfied by *pgxpool.Pool.
type pgxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// AuthServiceConfig holds the configuration for AuthService.
// Using a config struct instead of positional parameters makes call sites
// self-documenting and prevents accidental argument transposition.
type AuthServiceConfig struct {
	JWTSecret      string
	AccessExpiry   time.Duration
	RefreshExpiry  time.Duration
	BCryptCost     int
	PasswordMinLen int
	AppBaseURL     string
	AppName        string
}

// AuthService handles all authentication business logic.
type AuthService struct {
	q      *db.Queries
	pool   pgxBeginner
	mailer email.Mailer
	cfg    AuthServiceConfig

	// dummyHash is compared against the supplied password when the email does
	// not exist, so the login path spends comparable time whether or not the
	// account is real. This closes the timing oracle that would otherwise leak
	// which emails are registered.
	dummyHash []byte

	// wg tracks background email goroutines so Shutdown can drain them cleanly.
	wg sync.WaitGroup
}

// NewAuthService constructs an AuthService with all dependencies injected.
func NewAuthService(q *db.Queries, pool pgxBeginner, mailer email.Mailer, cfg AuthServiceConfig) *AuthService {
	// Precompute a throwaway hash at the configured cost so the no-user login
	// path can perform an equivalent bcrypt comparison. If generation fails we
	// leave it nil; Login handles that by skipping the comparison.
	dummyHash, err := bcrypt.GenerateFromPassword([]byte("timing-equalizer-not-a-real-password"), cfg.BCryptCost)
	if err != nil {
		slog.Error("generate dummy password hash", "error", err)
	}

	return &AuthService{
		q:         q,
		pool:      pool,
		mailer:    mailer,
		cfg:       cfg,
		dummyHash: dummyHash,
	}
}

// Shutdown blocks until all in-flight background email goroutines have finished.
// Call this during application shutdown, after the HTTP server has stopped
// accepting new requests, to avoid cutting off in-progress email deliveries.
func (s *AuthService) Shutdown() {
	s.wg.Wait()
}

// goSend runs fn in a tracked background goroutine. The WaitGroup is incremented
// before launching so that Shutdown() can drain all outstanding sends.
func (s *AuthService) goSend(fn func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn()
	}()
}

// Register creates a new user account and sends an email verification code.
func (s *AuthService) Register(ctx context.Context, rawEmail, password string) (db.User, error) {
	normalizedEmail := users.NormalizeEmail(rawEmail)
	if err := users.ValidateEmail(normalizedEmail); err != nil {
		return db.User{}, err
	}
	if err := users.ValidatePassword(password, s.cfg.PasswordMinLen); err != nil {
		return db.User{}, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.cfg.BCryptCost)
	if err != nil {
		return db.User{}, fmt.Errorf("hash password: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return db.User{}, fmt.Errorf("generate user id: %w", err)
	}

	user, err := s.q.CreateUser(ctx, db.CreateUserParams{
		ID:           id,
		Email:        normalizedEmail,
		PasswordHash: string(hash),
	})
	if err != nil {
		return db.User{}, fmt.Errorf("create user: %w", err)
	}

	s.goSend(func() {
		if sendErr := s.sendVerificationEmail(context.Background(), user); sendErr != nil {
			slog.Error("send verification code", "error", sendErr, "user_id", user.ID)
		}
	})

	return user, nil
}

// Login authenticates a user and returns a token pair.
func (s *AuthService) Login(ctx context.Context, rawEmail, password string, meta DeviceMeta) (TokenResult, error) {
	normalizedEmail := users.NormalizeEmail(rawEmail)

	user, err := s.q.GetUserByEmail(ctx, normalizedEmail)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Spend comparable time to the real-password path so response
			// latency doesn't reveal whether the email is registered.
			if s.dummyHash != nil {
				_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
			}
			return TokenResult{}, ErrInvalidCredentials
		}
		return TokenResult{}, fmt.Errorf("get user: %w", err)
	}

	// Check account lock before anything else.
	if user.LockedUntil.Valid && user.LockedUntil.Time.After(time.Now()) {
		return TokenResult{}, ErrAccountLocked
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		if incrErr := s.q.IncrementFailedLoginAttempts(ctx, user.ID); incrErr != nil {
			slog.Error("increment failed login attempts", "user_id", user.ID, "error", incrErr)
		}
		if user.FailedLoginAttempts+1 >= maxFailedLoginAttempts {
			lockedUntil := time.Now().Add(lockDuration)
			if lockErr := s.q.LockAccount(ctx, db.LockAccountParams{
				ID:          user.ID,
				LockedUntil: pgtype.Timestamptz{Time: lockedUntil, Valid: true},
			}); lockErr != nil {
				slog.Error("lock account", "user_id", user.ID, "error", lockErr)
			}
		}
		return TokenResult{}, ErrInvalidCredentials
	}

	if err := s.q.ResetLoginAttempts(ctx, user.ID); err != nil {
		slog.Error("reset login attempts", "error", err, "user_id", user.ID)
	}

	result, err := s.issueTokenPair(ctx, user.ID, user.Email, meta)
	if err != nil {
		return TokenResult{}, err
	}
	result.EmailVerified = user.EmailVerified
	return result, nil
}

// Logout revokes the given raw refresh token.
func (s *AuthService) Logout(ctx context.Context, rawRefreshToken string) error {
	hash := auth.HashToken(rawRefreshToken)
	rt, err := s.q.GetRefreshTokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("get refresh token: %w", err)
	}
	return s.q.RevokeRefreshToken(ctx, rt.ID)
}

// Refresh validates a raw refresh token, issues a new access token, and returns
// the same refresh token (no rotation). The refresh token's last_used_at and
// device metadata are updated in place.
func (s *AuthService) Refresh(ctx context.Context, rawRefreshToken string, meta DeviceMeta) (TokenResult, error) {
	hash := auth.HashToken(rawRefreshToken)

	rt, err := s.q.GetRefreshTokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TokenResult{}, ErrTokenNotFound
		}
		return TokenResult{}, fmt.Errorf("get refresh token: %w", err)
	}

	if rt.RevokedAt.Valid {
		return TokenResult{}, ErrTokenRevoked
	}

	if rt.ExpiresAt.Time.Before(time.Now()) {
		return TokenResult{}, ErrTokenExpired
	}

	user, err := s.q.GetUserByID(ctx, rt.UserID)
	if err != nil {
		return TokenResult{}, fmt.Errorf("get user: %w", err)
	}

	if err := s.q.UpdateRefreshTokenLastUsed(ctx, db.UpdateRefreshTokenLastUsedParams{
		ID:         rt.ID,
		DeviceName: pgtype.Text{String: meta.DeviceName, Valid: meta.DeviceName != ""},
		IpAddress:  meta.IPAddress,
		UserAgent:  pgtype.Text{String: meta.UserAgent, Valid: meta.UserAgent != ""},
	}); err != nil {
		slog.Error("update refresh token last used", "token_id", rt.ID, "error", err)
	}

	tp, err := auth.IssueAccessToken(user.ID, user.Email, s.cfg.JWTSecret, s.cfg.AccessExpiry)
	if err != nil {
		return TokenResult{}, fmt.Errorf("issue access token: %w", err)
	}

	return TokenResult{
		AccessToken:        tp.AccessToken,
		AccessTokenExpiry:  tp.ExpiresAt,
		RefreshToken:       rawRefreshToken,
		RefreshTokenExpiry: rt.ExpiresAt.Time,
	}, nil
}

// RequestPasswordReset sends a reset email if the address exists.
// Always returns nil to prevent email enumeration.
func (s *AuthService) RequestPasswordReset(ctx context.Context, rawEmail string) error {
	normalizedEmail := users.NormalizeEmail(rawEmail)

	user, err := s.q.GetUserByEmail(ctx, normalizedEmail)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("get user: %w", err)
	}

	count, err := s.q.CountRecentPasswordResetsByEmail(ctx, normalizedEmail)
	if err != nil {
		return fmt.Errorf("count resets: %w", err)
	}
	if count >= 3 {
		return nil
	}

	rawToken, hashedToken, err := auth.GenerateResetToken()
	if err != nil {
		return fmt.Errorf("generate reset token: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate token id: %w", err)
	}
	_, err = s.q.InsertPasswordResetToken(ctx, db.InsertPasswordResetTokenParams{
		ID:        id,
		UserID:    user.ID,
		TokenHash: hashedToken,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("insert reset token: %w", err)
	}

	resetURL := fmt.Sprintf("%s/reset-password?token=%s", s.cfg.AppBaseURL, rawToken)
	subj, html, text := email.PasswordResetEmail(s.cfg.AppName, resetURL)
	s.goSend(func() {
		if sendErr := s.mailer.Send(normalizedEmail, subj, html, text); sendErr != nil {
			slog.Error("send reset email", "error", sendErr, "user_id", user.ID)
		}
	})

	return nil
}

// CompletePasswordReset validates the token and updates the user's password.
func (s *AuthService) CompletePasswordReset(ctx context.Context, rawToken, newPassword string) error {
	if err := users.ValidatePassword(newPassword, s.cfg.PasswordMinLen); err != nil {
		return err
	}

	hashedToken := auth.HashToken(rawToken)
	prt, err := s.q.GetPasswordResetTokenByHash(ctx, hashedToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTokenNotFound
		}
		return fmt.Errorf("get reset token: %w", err)
	}
	if prt.UsedAt.Valid {
		return ErrTokenUsed
	}
	if prt.ExpiresAt.Time.Before(time.Now()) {
		return ErrTokenExpired
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), s.cfg.BCryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	qtx := s.q.WithTx(tx)
	if err := qtx.UpdatePasswordHash(ctx, db.UpdatePasswordHashParams{
		ID:           prt.UserID,
		PasswordHash: string(newHash),
	}); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if err := qtx.MarkPasswordResetTokenUsed(ctx, prt.ID); err != nil {
		return fmt.Errorf("mark token used: %w", err)
	}
	if err := qtx.RevokeAllUserRefreshTokens(ctx, prt.UserID); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	user, userErr := s.q.GetUserByID(ctx, prt.UserID)
	if userErr != nil {
		slog.Error("get user after password reset", "user_id", prt.UserID, "error", userErr)
	} else {
		subj, html, text := email.PasswordChangedEmail(s.cfg.AppName)
		s.goSend(func() {
			if sendErr := s.mailer.Send(user.Email, subj, html, text); sendErr != nil {
				slog.Error("send password changed email", "error", sendErr, "user_id", user.ID)
			}
		})
	}

	return nil
}

// VerifyEmail validates a raw verification token from a link, marks the user's
// email as verified, and issues a token pair so the click auto-logs them in.
func (s *AuthService) VerifyEmail(ctx context.Context, rawToken string, meta DeviceMeta) (TokenResult, error) {
	hash := auth.HashToken(rawToken)
	record, err := s.q.GetVerificationCodeByTokenHash(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TokenResult{}, ErrTokenNotFound
		}
		return TokenResult{}, fmt.Errorf("get verification code: %w", err)
	}
	if record.UsedAt.Valid {
		return TokenResult{}, ErrTokenUsed
	}
	if record.ExpiresAt.Time.Before(time.Now()) {
		return TokenResult{}, ErrTokenExpired
	}
	if err := s.q.MarkVerificationCodeUsed(ctx, record.ID); err != nil {
		return TokenResult{}, fmt.Errorf("mark code used: %w", err)
	}
	if err := s.q.SetEmailVerified(ctx, record.UserID); err != nil {
		return TokenResult{}, fmt.Errorf("set email verified: %w", err)
	}
	user, err := s.q.GetUserByID(ctx, record.UserID)
	if err != nil {
		return TokenResult{}, fmt.Errorf("get user: %w", err)
	}
	result, err := s.issueTokenPair(ctx, user.ID, user.Email, meta)
	if err != nil {
		return TokenResult{}, err
	}
	result.EmailVerified = true
	return result, nil
}

// ChangePassword verifies the current password and replaces it with a new one.
func (s *AuthService) ChangePassword(ctx context.Context, userID uuid.UUID, currentPassword, newPassword string) error {
	user, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)); err != nil {
		return ErrInvalidCredentials
	}

	if err := users.ValidatePassword(newPassword, s.cfg.PasswordMinLen); err != nil {
		return err
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), s.cfg.BCryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	return s.q.UpdatePasswordHash(ctx, db.UpdatePasswordHashParams{
		ID:           userID,
		PasswordHash: string(newHash),
	})
}

// ResendVerification issues a fresh verification code if rate limit not exceeded.
func (s *AuthService) ResendVerification(ctx context.Context, userID uuid.UUID) error {
	user, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if user.EmailVerified {
		return ErrEmailAlreadyVerified
	}

	count, err := s.q.CountRecentVerificationCodesByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("count codes: %w", err)
	}
	if count >= 3 {
		oldest, oldestErr := s.q.GetOldestRecentVerificationCode(ctx, userID)
		retryAt := time.Now().Add(time.Hour)
		if oldestErr == nil && oldest.Valid {
			retryAt = oldest.Time.Add(time.Hour)
		}
		return RateLimitedError{RetryAt: retryAt}
	}

	return s.sendVerificationEmail(ctx, user)
}

// ── private helpers ───────────────────────────────────────────────────────────

func (s *AuthService) issueTokenPair(
	ctx context.Context,
	userID uuid.UUID,
	userEmail string,
	meta DeviceMeta,
) (TokenResult, error) {
	tp, err := auth.IssueAccessToken(userID, userEmail, s.cfg.JWTSecret, s.cfg.AccessExpiry)
	if err != nil {
		return TokenResult{}, fmt.Errorf("issue access token: %w", err)
	}

	rawRefresh, hashedRefresh, err := auth.GenerateRefreshToken()
	if err != nil {
		return TokenResult{}, fmt.Errorf("generate refresh token: %w", err)
	}

	newID, err := uuid.NewV7()
	if err != nil {
		return TokenResult{}, fmt.Errorf("generate token id: %w", err)
	}
	expiresAt := time.Now().Add(s.cfg.RefreshExpiry)

	_, err = s.q.InsertRefreshToken(ctx, db.InsertRefreshTokenParams{
		ID:         newID,
		UserID:     userID,
		TokenHash:  hashedRefresh,
		DeviceName: pgtype.Text{String: meta.DeviceName, Valid: meta.DeviceName != ""},
		IpAddress:  meta.IPAddress,
		UserAgent:  pgtype.Text{String: meta.UserAgent, Valid: meta.UserAgent != ""},
		ExpiresAt:  pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return TokenResult{}, fmt.Errorf("insert refresh token: %w", err)
	}

	return TokenResult{
		AccessToken:        tp.AccessToken,
		AccessTokenExpiry:  tp.ExpiresAt,
		RefreshToken:       rawRefresh,
		RefreshTokenExpiry: expiresAt,
	}, nil
}

func (s *AuthService) sendVerificationEmail(ctx context.Context, user db.User) error {
	rawToken, hashedToken, err := auth.GenerateVerificationToken()
	if err != nil {
		return fmt.Errorf("generate verification token: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate code id: %w", err)
	}
	_, err = s.q.InsertEmailVerificationCode(ctx, db.InsertEmailVerificationCodeParams{
		ID:        id,
		UserID:    user.ID,
		TokenHash: hashedToken,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("insert verification code: %w", err)
	}

	verifyURL := fmt.Sprintf("%s/auth/verify-email?token=%s", s.cfg.AppBaseURL, rawToken)
	subj, htmlBody, text := email.VerificationEmail(s.cfg.AppName, verifyURL)
	return s.mailer.Send(user.Email, subj, htmlBody, text)
}
