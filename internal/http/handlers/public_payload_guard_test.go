package handlers_test

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
	"github.com/softsrv/starter/internal/http/middleware"
)

// CLM-8: No response or frame describing one player to another (room roster,
// race roster, telemetry, and any later route built on the public player view)
// contains an email address. db.User, which carries a json email tag, is never
// serialised into such a response.

func TestPublicPayloadNoEmailLeak(t *testing.T) {
	const (
		namedEmail   = "leak.alpha@example.test"
		unnamedEmail = "leak.beta@example.test"
	)

	namedUser := db.User{
		ID:            uuid.Must(uuid.NewV7()),
		Email:         namedEmail,
		DisplayName:   pgtype.Text{String: "RowerAlpha", Valid: true},
		EmailVerified: true,
	}
	unnamedUser := db.User{
		ID:            uuid.Must(uuid.NewV7()),
		Email:         unnamedEmail,
		DisplayName:   pgtype.Text{},
		EmailVerified: true,
	}

	namedPlayer := app.NewPublicPlayer(namedUser.ID, namedUser.DisplayName)
	unnamedPlayer := app.NewPublicPlayer(unnamedUser.ID, unnamedUser.DisplayName)

	assertNoEmail := func(t *testing.T, payload []byte, contextDesc string) {
		t.Helper()
		s := string(payload)
		if strings.Contains(s, namedEmail) {
			t.Errorf("%s leaked named fixture email %q in payload: %s", contextDesc, namedEmail, s)
		}
		if strings.Contains(s, unnamedEmail) {
			t.Errorf("%s leaked unnamed fixture email %q in payload: %s", contextDesc, unnamedEmail, s)
		}
		if strings.Contains(s, "@") {
			t.Errorf("%s leaked '@' character in payload: %s", contextDesc, s)
		}
	}

	t.Run("RoomRosterPayload", func(t *testing.T) {
		roster := app.RoomRoster{
			Participants: []app.RoomRosterEntry{
				{
					PublicPlayer: namedPlayer,
					Ready:        true,
					Host:         true,
				},
				{
					PublicPlayer: unnamedPlayer,
					Ready:        false,
					Host:         false,
				},
			},
		}

		svc := &stubRoomService{
			rosterFn: func(ctx context.Context, caller, roomID uuid.UUID) (app.RoomRoster, error) {
				return roster, nil
			},
		}

		h := handlers.NewRoomHandler(context.Background(), svc, &fakeBeginner{}, nil, false)
		roomID := uuid.Must(uuid.NewV7())
		req := httptest.NewRequest(http.MethodGet, "/rooms/"+roomID.String(), nil)
		req.SetPathValue("id", roomID.String())

		token, err := auth.IssueAccessToken(namedUser.ID, namedUser.Email, testJWTSecret, 15*time.Minute)
		if err != nil {
			t.Fatalf("issue token: %v", err)
		}
		req.AddCookie(&http.Cookie{Name: "access_token", Value: token.AccessToken})
		rr := httptest.NewRecorder()

		fetcher := &handlerUserFetcher{user: namedUser}
		middleware.Authenticate(fetcher, testJWTSecret)(http.HandlerFunc(h.Roster)).ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("roster handler status = %d, want %d", rr.Code, http.StatusOK)
		}

		assertNoEmail(t, rr.Body.Bytes(), "room roster response")
	})

	t.Run("RaceRosterFrame", func(t *testing.T) {
		ctx := wsContext(t)
		raceID := uuid.Must(uuid.NewV7())
		players := []app.PublicPlayer{namedPlayer, unnamedPlayer}

		b := newObservedBroadcaster()
		handler := handlers.NewRaceWSHandler(nil, rosterSource{players: players}, b)
		srv := wsServer(t, namedUser, handler)

		conn := wsDial(t, ctx, srv.URL+"/races/"+raceID.String()+"/ws", wsHeaders(t, namedUser))
		_, payload, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read first ws frame: %v", err)
		}

		assertNoEmail(t, payload, "race roster websocket frame")
	})

	t.Run("TelemetryFrame", func(t *testing.T) {
		sample := app.TelemetrySample{
			ElapsedMilliseconds: 12500,
			DistanceMillimeters: 50000,
			StrokeRate:          28,
			Power:               240,
			SampledAt:           time.Now(),
		}

		// Shape RaceService.Ingest marshals and publishes
		payload, err := json.Marshal(struct {
			UserID uuid.UUID `json:"user_id"`
			app.TelemetrySample
		}{UserID: namedUser.ID, TelemetrySample: sample})
		if err != nil {
			t.Fatalf("marshal telemetry: %v", err)
		}

		assertNoEmail(t, payload, "telemetry frame")
	})

	t.Run("RaceResultsPayload", func(t *testing.T) {
		rank, elapsed, pace := 1, int64(400000), int64(100000)
		results := app.RaceResults{RaceID: uuid.Must(uuid.NewV7()), Finished: true, TargetMillimeters: 2000000, Results: []app.RaceResult{
			{PublicPlayer: namedPlayer, Rank: &rank, Status: "finished", ElapsedMilliseconds: &elapsed,
				DistanceMillimeters: 2000000, PaceMillisecondsPer500m: &pace},
			{PublicPlayer: unnamedPlayer, Status: "dnf", DistanceMillimeters: 500000},
		}}
		viewer := &stubRoomRaceViewer{resultsFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RaceResults, error) {
			return results, nil
		}}
		roomID := uuid.Must(uuid.NewV7())

		rr := serveRacePage(t, viewer, namedUser, roomID.String(), raceResultsEndpoint)

		if rr.Code != http.StatusOK {
			t.Fatalf("results handler status = %d, want %d", rr.Code, http.StatusOK)
		}
		if !strings.Contains(rr.Body.String(), "RowerAlpha") {
			t.Fatalf("results body missing the named player's public name: %s", rr.Body.String())
		}
		assertNoEmail(t, rr.Body.Bytes(), "race results response")
	})
}

// inspectForForbiddenUserSerialisation checks an AST file for forbidden serialisation of db.User.
// It returns a list of violations detected.
func inspectForForbiddenUserSerialisation(fset *token.FileSet, file *ast.File) []string {
	var violations []string

	// Track identifiers in the file that hold db.User, *db.User, or []db.User
	userVars := make(map[string]bool)

	isUserType := func(expr ast.Expr) bool {
		switch t := expr.(type) {
		case *ast.SelectorExpr:
			if ident, ok := t.X.(*ast.Ident); ok && ident.Name == "db" && t.Sel.Name == "User" {
				return true
			}
		case *ast.StarExpr:
			if sel, ok := t.X.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "db" && sel.Sel.Name == "User" {
					return true
				}
			}
		case *ast.ArrayType:
			if sel, ok := t.Elt.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "db" && sel.Sel.Name == "User" {
					return true
				}
			}
		}
		return false
	}

	isUserExpr := func(expr ast.Expr) bool {
		switch e := expr.(type) {
		case *ast.Ident:
			if userVars[e.Name] {
				return true
			}
		case *ast.CompositeLit:
			if isUserType(e.Type) {
				return true
			}
		case *ast.CallExpr:
			// Known db.User-returning helpers, e.g. middleware.UserFromContext
			if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "middleware" && sel.Sel.Name == "UserFromContext" {
					return true
				}
			}
		case *ast.UnaryExpr:
			if e.Op == token.AND {
				if lit, ok := e.X.(*ast.CompositeLit); ok && isUserType(lit.Type) {
					return true
				}
			}
		}
		return false
	}

	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return true
		}

		// 1. Collect declarations
		switch decl := n.(type) {
		case *ast.FuncDecl:
			// Check parameters
			if decl.Type != nil && decl.Type.Params != nil {
				for _, param := range decl.Type.Params.List {
					if isUserType(param.Type) {
						for _, name := range param.Names {
							userVars[name.Name] = true
						}
					}
				}
			}
		case *ast.ValueSpec:
			// var x db.User or var x = ...
			isUser := isUserType(decl.Type)
			for i, name := range decl.Names {
				if isUser {
					userVars[name.Name] = true
				} else if i < len(decl.Values) && isUserExpr(decl.Values[i]) {
					userVars[name.Name] = true
				}
			}
		case *ast.AssignStmt:
			// x := ... or user, _ := middleware.UserFromContext(...)
			for i, rhs := range decl.Rhs {
				if call, ok := rhs.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "middleware" && sel.Sel.Name == "UserFromContext" {
							if len(decl.Lhs) > 0 {
								if lhsIdent, ok := decl.Lhs[0].(*ast.Ident); ok {
									userVars[lhsIdent.Name] = true
								}
							}
						}
					}
				}
				if isUserExpr(rhs) && i < len(decl.Lhs) {
					if lhsIdent, ok := decl.Lhs[i].(*ast.Ident); ok {
						userVars[lhsIdent.Name] = true
					}
				}
			}
		case *ast.RangeStmt:
			// for _, u := range users
			if isUserExpr(decl.X) {
				if decl.Value != nil {
					if valIdent, ok := decl.Value.(*ast.Ident); ok {
						userVars[valIdent.Name] = true
					}
				}
			}
		}

		// 2. Check function calls
		if call, ok := n.(*ast.CallExpr); ok {
			var isTargetCall bool

			// Check json.Marshal(expr) or json.MarshalIndent(expr, ...)
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkgIdent, ok := sel.X.(*ast.Ident); ok && pkgIdent.Name == "json" {
					if sel.Sel.Name == "Marshal" || sel.Sel.Name == "MarshalIndent" {
						isTargetCall = true
					}
				}
				// Check (*json.Encoder).Encode(expr)
				if sel.Sel.Name == "Encode" {
					// could be encoder.Encode(...) or json.NewEncoder(w).Encode(...)
					isTargetCall = true
				}
				// Check conn.Write(ctx, websocket.MessageText, expr)
				if sel.Sel.Name == "Write" {
					isTargetCall = true
				}
			}

			if isTargetCall {
				for _, arg := range call.Args {
					if isUserExpr(arg) {
						pos := fset.Position(arg.Pos())
						violations = append(violations, pos.String()+": db.User passed to serialisation call")
					}
				}
			}
		}

		return true
	})

	return violations
}

func TestPublicPayloadASTGuard(t *testing.T) {
	// Red test verification: ensure the guard detects violations in snippet tests
	t.Run("FailsOnUserFromContextEncoded", func(t *testing.T) {
		src := `package test
import (
	"encoding/json"
	"net/http"
	"github.com/softsrv/starter/internal/http/middleware"
)
func handler(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	_ = json.NewEncoder(w).Encode(user)
}
`
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "bad.go", src, 0)
		if err != nil {
			t.Fatalf("parse snippet: %v", err)
		}
		violations := inspectForForbiddenUserSerialisation(fset, file)
		if len(violations) == 0 {
			t.Fatal("expected violation for json.NewEncoder(w).Encode(user), got none")
		}
	})

	t.Run("FailsOnSliceUserMarshal", func(t *testing.T) {
		src := `package test
import (
	"encoding/json"
	"github.com/softsrv/starter/internal/db"
)
func handler(users []db.User) {
	_, _ = json.Marshal(users)
}
`
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "bad_slice.go", src, 0)
		if err != nil {
			t.Fatalf("parse snippet: %v", err)
		}
		violations := inspectForForbiddenUserSerialisation(fset, file)
		if len(violations) == 0 {
			t.Fatal("expected violation for json.Marshal(users), got none")
		}
	})

	t.Run("ProductionHandlersPassClean", func(t *testing.T) {
		handlersDir := "."
		entries, err := os.ReadDir(handlersDir)
		if err != nil {
			t.Fatalf("read handlers dir: %v", err)
		}

		fset := token.NewFileSet()
		fileCount := 0
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(handlersDir, entry.Name())
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse file %s: %v", path, err)
			}
			fileCount++
			violations := inspectForForbiddenUserSerialisation(fset, file)
			for _, v := range violations {
				t.Errorf("violation in %s: %s", path, v)
			}
		}

		if fileCount == 0 {
			t.Fatal("no non-test .go files found in internal/http/handlers")
		}
	})
}
