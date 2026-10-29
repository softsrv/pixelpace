package http

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
	"github.com/softsrv/starter/internal/http/middleware"
	"github.com/softsrv/starter/internal/realtime"
	"github.com/softsrv/starter/web"
)

// RouterConfig holds all dependencies required to build the router.
type RouterConfig struct {
	Queries           middleware.UserFetcher
	Pool              handlers.DBPinger
	AuthSvc           *app.AuthService
	UserSvc           *app.UserService
	RoomSvc           *app.RoomService
	RaceSvc           *app.RaceService
	Broadcaster       realtime.Broadcaster
	QuickMatchSvc     *app.QuickMatchService
	FriendSvc         *app.FriendService
	LeaderboardSvc    *app.LeaderboardService
	Renderer          *handlers.TemplateRenderer
	JWTSecret         string
	AppBaseURL        string // prefixes shareable links, e.g. room join links
	Secure            bool   // true in production
	DevMode           bool   // true outside production
	TrustedProxyCount int
}

// Router wraps the HTTP handler and provides lifecycle coordination for background tasks.
type Router struct {
	http.Handler
	roomH *handlers.RoomHandler
}

var _ http.Handler = (*Router)(nil)

// Wait blocks until every race begin launched by Start has finished.
func (r *Router) Wait() {
	if r.roomH != nil {
		r.roomH.Wait()
	}
}

// ResumeRaces relaunches end-of-race watching for every race left in
// progress by a previous run of the server. Call it once at startup.
func (r *Router) ResumeRaces() error {
	if r.roomH == nil {
		return nil
	}
	return r.roomH.ResumeRaces()
}

// NewRouter builds and returns the main http.Handler with all routes and middleware.
// ctx controls the lifetime of background goroutines (rate-limiter sweepers and race begins); it
// should be cancelled during application shutdown after the HTTP server drains.
func dashboardTemplateData(user db.User, devMode bool) map[string]any {
	return map[string]any{
		"User":    user,
		"DevMode": devMode,
	}
}

// rootSession classifies the access token presented on "/".
type rootSession int

const (
	rootSignedOut rootSession = iota // missing, malformed, expired or wrong-secret token
	rootOrphaned                     // valid token whose user no longer exists
	rootSignedIn                     // valid token for an existing user (or a lookup that failed transiently)
)

// classifyRootToken validates token and looks its user up with the same
// UserFetcher the Authenticate middleware uses.
func classifyRootToken(r *http.Request, cfg RouterConfig, token string) rootSession {
	claims, err := auth.ValidateAccessToken(token, cfg.JWTSecret)
	if err != nil {
		return rootSignedOut
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil || cfg.Queries == nil {
		return rootOrphaned
	}
	if _, err := cfg.Queries.GetUserByID(r.Context(), userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return rootOrphaned
		}
		// A transient store failure says nothing about the account: keep the
		// session rather than clearing a valid cookie and logging the user out.
		slog.WarnContext(r.Context(), "root: token user lookup failed; keeping session", "user_id", userID, "error", err)
		return rootSignedIn
	}
	return rootSignedIn
}

// clearAccessTokenCookie expires a stale access_token cookie.
func clearAccessTokenCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     "access_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

func NewRouter(ctx context.Context, cfg RouterConfig) *Router {
	mux := http.NewServeMux()

	// ── Handlers ──────────────────────────────────────────────────────────────
	authH := handlers.NewAuthHandler(cfg.AuthSvc, cfg.Renderer, cfg.Secure, cfg.TrustedProxyCount)
	sessH := handlers.NewSessionHandler(cfg.UserSvc, cfg.Renderer, cfg.Secure)
	friendH := handlers.NewFriendHandler(cfg.FriendSvc)
	leaderboardH := handlers.NewLeaderboardHandler(cfg.LeaderboardSvc)
	profileH := handlers.NewProfileHandler(cfg.AuthSvc, cfg.UserSvc, cfg.Renderer, cfg.Secure)
	roomH := handlers.NewRoomHandler(ctx, cfg.RoomSvc, cfg.RaceSvc, cfg.Renderer, cfg.Secure)
	racePageH := handlers.NewRacePageHandler(cfg.RaceSvc, cfg.Renderer, cfg.DevMode)
	lobbyH := handlers.NewLobbyHandler(cfg.RoomSvc, cfg.FriendSvc, cfg.Renderer, cfg.AppBaseURL)
	raceWSH := handlers.NewRaceWSHandler(cfg.RaceSvc, cfg.RaceSvc, cfg.Broadcaster)
	quickMatchH := handlers.NewQuickMatchHandler(cfg.QuickMatchSvc)

	// ── Rate limiters ─────────────────────────────────────────────────────────
	// Each limiter spawns a sweep goroutine that exits when ctx is cancelled.
	ipKey := middleware.IPKeyFunc(cfg.TrustedProxyCount)
	loginRL := middleware.NewRateLimiter(ctx, 5, 15*time.Minute, ipKey)
	registerRL := middleware.NewRateLimiter(ctx, 3, time.Hour, ipKey)
	refreshRL := middleware.NewRateLimiter(ctx, 10, time.Minute, middleware.CookieRefreshTokenKeyFunc)
	forgotRL := middleware.NewRateLimiter(ctx, 3, time.Hour, middleware.FormEmailKeyFunc)
	resetRL := middleware.NewRateLimiter(ctx, 5, time.Hour, ipKey)

	authMW := middleware.Authenticate(cfg.Queries, cfg.JWTSecret)
	verifiedMW := func(h http.Handler) http.Handler { return authMW(middleware.RequireEmailVerified(h)) }

	// ── Static assets ─────────────────────────────────────────────────────────
	staticFS, _ := fs.Sub(web.FS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	// ── Public routes ─────────────────────────────────────────────────────────
	mux.HandleFunc("GET /health", handlers.HandleLiveness)
	mux.HandleFunc("GET /ready", handlers.HandleReadiness(cfg.Pool))

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		// Only send the user to the dashboard if they present a genuinely valid
		// access token for an account that still exists; a stale, malformed or
		// orphaned cookie on exactly "/" should land on the landing page rather
		// than bouncing through a protected route to /login.
		if cookie, err := r.Cookie("access_token"); err == nil && cookie.Value != "" {
			switch classifyRootToken(r, cfg, cookie.Value) {
			case rootSignedIn:
				http.Redirect(w, r, "/dashboard", http.StatusFound)
				return
			case rootOrphaned:
				if r.URL.Path == "/" {
					clearAccessTokenCookie(w, cfg.Secure)
				}
			}
		}
		if r.URL.Path != "/" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		cfg.Renderer.Page(w, http.StatusOK, "landing.html", nil)
	})
	mux.HandleFunc("GET /login", authH.LoginPage)
	mux.HandleFunc("GET /register", authH.RegisterPage)
	mux.HandleFunc("GET /forgot-password", authH.ForgotPasswordPage)
	mux.HandleFunc("GET /reset-password", authH.ResetPasswordPage)
	mux.HandleFunc("GET /verify-email", authH.VerifyEmailPage)

	mux.Handle("POST /auth/login", loginRL.Middleware(http.HandlerFunc(authH.Login)))
	mux.Handle("POST /auth/register", registerRL.Middleware(http.HandlerFunc(authH.Register)))
	mux.Handle("POST /auth/refresh", refreshRL.Middleware(http.HandlerFunc(authH.Refresh)))
	mux.Handle("POST /auth/forgot-password", forgotRL.Middleware(http.HandlerFunc(authH.ForgotPassword)))
	mux.Handle("POST /auth/reset-password", resetRL.Middleware(http.HandlerFunc(authH.ResetPassword)))

	// ── Protected routes ──────────────────────────────────────────────────────
	mux.Handle("POST /friends/requests", authMW(http.HandlerFunc(friendH.Send)))
	mux.Handle("GET /friends/requests", authMW(http.HandlerFunc(friendH.ListRequests)))
	mux.Handle("POST /friends/requests/{id}/accept", authMW(http.HandlerFunc(friendH.Accept)))
	mux.Handle("POST /friends/requests/{id}/reject", authMW(http.HandlerFunc(friendH.Reject)))
	mux.Handle("GET /friends", authMW(http.HandlerFunc(friendH.ListFriends)))
	mux.Handle("GET /leaderboard", authMW(http.HandlerFunc(leaderboardH.Leaderboard)))
	mux.Handle("GET /races/{id}/ws", authMW(http.HandlerFunc(raceWSH.Serve)))

	mux.Handle("POST /auth/logout", authMW(http.HandlerFunc(authH.Logout)))
	mux.Handle("POST /auth/resend-verification", authMW(http.HandlerFunc(authH.ResendVerification)))

	// Email verification: public GET so users can click directly from their inbox.
	mux.HandleFunc("GET /auth/verify-email", authH.VerifyEmail)
	mux.Handle("GET /auth/sessions", verifiedMW(http.HandlerFunc(sessH.ListSessions)))
	mux.Handle("DELETE /auth/sessions/{id}", verifiedMW(http.HandlerFunc(sessH.RevokeSession)))

	mux.Handle("GET /profile", verifiedMW(http.HandlerFunc(profileH.ProfilePage)))
	mux.Handle("POST /profile/change-password", verifiedMW(http.HandlerFunc(profileH.ChangePassword)))
	mux.Handle("POST /profile/display-name", verifiedMW(http.HandlerFunc(profileH.SetDisplayName)))
	mux.Handle("POST /profile/delete", verifiedMW(http.HandlerFunc(profileH.DeleteAccount)))

	mux.Handle("POST /quick-match", verifiedMW(http.HandlerFunc(quickMatchH.Enqueue)))
	mux.Handle("POST /quick-match/leave", verifiedMW(http.HandlerFunc(quickMatchH.Leave)))
	mux.Handle("POST /quick-match/match", verifiedMW(http.HandlerFunc(quickMatchH.Match)))

	mux.Handle("POST /rooms", verifiedMW(http.HandlerFunc(roomH.Create)))
	mux.Handle("POST /rooms/join", verifiedMW(http.HandlerFunc(roomH.Join)))
	mux.Handle("GET /rooms/{id}", verifiedMW(http.HandlerFunc(roomH.Roster)))
	mux.Handle("POST /rooms/{id}/leave", verifiedMW(http.HandlerFunc(roomH.Leave)))
	mux.Handle("POST /rooms/{id}/kick", verifiedMW(http.HandlerFunc(roomH.Kick)))
	mux.Handle("POST /rooms/{id}/ready", verifiedMW(http.HandlerFunc(roomH.MarkReady)))
	mux.Handle("POST /rooms/{id}/start", verifiedMW(http.HandlerFunc(roomH.Start)))
	mux.Handle("GET /rooms/{id}/lobby", verifiedMW(http.HandlerFunc(lobbyH.Page)))
	mux.Handle("GET /rooms/{id}/lobby/panel", verifiedMW(http.HandlerFunc(lobbyH.Panel)))
	mux.Handle("POST /rooms/{id}/race-type", verifiedMW(http.HandlerFunc(lobbyH.ChangeRaceType)))
	mux.Handle("POST /rooms/{id}/unready", verifiedMW(http.HandlerFunc(lobbyH.MarkUnready)))
	mux.Handle("POST /rooms/{id}/invites", verifiedMW(http.HandlerFunc(lobbyH.Invite)))
	mux.Handle("GET /rooms/{id}/race", verifiedMW(http.HandlerFunc(racePageH.Page)))
	mux.Handle("GET /rooms/{id}/race/status", verifiedMW(http.HandlerFunc(racePageH.Status)))
	mux.Handle("POST /rooms/{id}/race/ready", verifiedMW(http.HandlerFunc(racePageH.Ready)))
	mux.Handle("GET /rooms/{id}/race/results", verifiedMW(http.HandlerFunc(racePageH.Results)))
	mux.Handle("GET /join/{code}", verifiedMW(http.HandlerFunc(lobbyH.JoinLink)))
	mux.Handle("GET /dashboard/race-card", verifiedMW(http.HandlerFunc(lobbyH.RaceCard)))
	mux.Handle("GET /invites", verifiedMW(http.HandlerFunc(lobbyH.Invites)))
	mux.Handle("POST /invites/{id}/accept", verifiedMW(http.HandlerFunc(lobbyH.AcceptInvite)))
	mux.Handle("POST /invites/{id}/decline", verifiedMW(http.HandlerFunc(lobbyH.DeclineInvite)))

	mux.Handle("GET /dashboard", verifiedMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := middleware.UserFromContext(r.Context())
		cfg.Renderer.Page(w, http.StatusOK, "dashboard.html", dashboardTemplateData(user, cfg.DevMode))
	})))

	// ── Global middleware chain ───────────────────────────────────────────────
	// BodyLimit is innermost so it wraps r.Body before any handler reads it.
	handler := middleware.RequestID(
		middleware.Logging(
			middleware.SecurityHeaders(cfg.Secure,
				middleware.BodyLimit(middleware.DefaultMaxBodyBytes)(mux),
			),
		),
	)
	return &Router{Handler: handler, roomH: roomH}
}
