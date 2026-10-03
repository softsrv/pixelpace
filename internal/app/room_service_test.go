package app

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/db"
)

// Script the database boundary, not the service: unexpected reads or writes fail
// immediately, including mutations after a rejected guard.
type roomDBStep struct {
	name   string
	args   []any
	values []any
	err    error
}

type roomTestDB struct {
	pgx.Tx
	t                          *testing.T
	steps                      []roomDBStep
	begins, commits, rollbacks int
}

func (d *roomTestDB) next(sql string, args []any) roomDBStep {
	d.t.Helper()
	if len(d.steps) == 0 {
		d.t.Fatalf("unexpected query: %s", sql)
	}
	s := d.steps[0]
	d.steps = d.steps[1:]
	if !strings.HasPrefix(sql, "-- name: "+s.name+" ") {
		d.t.Fatalf("query = %q, want %s", sql, s.name)
	}
	if s.args != nil {
		if len(args) != len(s.args) {
			d.t.Fatalf("%s args = %v, want %v", s.name, args, s.args)
		}
		for i, want := range s.args {
			if want == nil {
				id, ok := args[i].(uuid.UUID)
				if !ok || id.Version() != 7 {
					d.t.Fatalf("%s arg %d is not UUIDv7: %v", s.name, i, args[i])
				}
			} else if !reflect.DeepEqual(args[i], want) {
				d.t.Fatalf("%s arg %d = %v, want %v", s.name, i, args[i], want)
			}
		}
	}
	return s
}

func (d *roomTestDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	s := d.next(sql, args)
	return roomTestRow{s.values, s.err}
}

func (d *roomTestDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	s := d.next(sql, args)
	return pgconn.NewCommandTag("UPDATE 1"), s.err
}

func (d *roomTestDB) Begin(context.Context) (pgx.Tx, error) { d.begins++; return d, nil }
func (d *roomTestDB) Commit(context.Context) error          { d.commits++; return nil }
func (d *roomTestDB) Rollback(context.Context) error        { d.rollbacks++; return nil }

type roomTestRow struct {
	values []any
	err    error
}

func (r roomTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("unexpected scan arity")
	}
	for i, value := range r.values {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

var (
	roomTestID    = uuid.MustParse("01950000-0000-7000-8000-000000000001")
	roomTestHost  = uuid.MustParse("01950000-0000-7000-8000-000000000002")
	roomTestGuest = uuid.MustParse("01950000-0000-7000-8000-000000000003")
)

func roomStep(name, status string) roomDBStep {
	return roomDBStep{name: name, values: []any{roomTestID, roomTestID, roomTestHost, status, "ABCDEF", pgtype.Timestamptz{}}}
}
func participantStep(userID uuid.UUID, ready bool) roomDBStep {
	return roomDBStep{name: "GetRoomParticipant", args: []any{roomTestID, userID}, values: []any{roomTestID, roomTestID, userID, ready, pgtype.Timestamptz{}}}
}
func countStep(count int64) roomDBStep {
	return roomDBStep{name: "CountRoomParticipants", args: []any{roomTestID}, values: []any{count}}
}
func newRoomTestService(t *testing.T, steps ...roomDBStep) (*RoomService, *roomTestDB) {
	t.Helper()
	d := &roomTestDB{t: t, steps: steps}
	t.Cleanup(func() {
		if len(d.steps) != 0 {
			t.Errorf("%d database steps not consumed: %v", len(d.steps), d.steps)
		}
	})
	return NewRoomService(db.New(d), d), d
}

func TestGenerateJoinCode(t *testing.T) {
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	if joinCodeAlphabet != alphabet {
		t.Fatalf("alphabet = %q", joinCodeAlphabet)
	}
	pattern := regexp.MustCompile(`^[A-HJ-KMNP-Z2-9]{6}$`)
	for i := 0; i < 1000; i++ {
		code, err := generateJoinCode()
		if err != nil || len(code) != 6 || !pattern.MatchString(code) || strings.Trim(code, alphabet) != "" {
			t.Fatalf("code = %q, err = %v", code, err)
		}
	}
}

func TestRoomJoinGuards(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		exists       bool
		count        int64
		want         error
	}{
		{"success", "waiting", false, 7, nil},
		{"duplicate", "waiting", true, 1, ErrAlreadyParticipant},
		{"full", "waiting", false, 8, ErrRoomFull},
		{"overfull", "waiting", false, 9, ErrRoomFull},
		{"countdown", "counting_down", false, 1, ErrRoomNotJoinable},
		{"racing", "in_progress", false, 1, ErrRoomNotJoinable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []roomDBStep{roomStep("GetActiveRoomByJoinCode", tc.status)}
			steps[0].args = []any{"ABCDEF"}
			if tc.status == "waiting" {
				steps = append(steps, roomDBStep{name: "RoomParticipantExists", args: []any{roomTestID, roomTestGuest}, values: []any{tc.exists}})
				if !tc.exists {
					steps = append(steps, countStep(tc.count))
				}
			}
			if tc.want == nil {
				steps = append(steps, roomDBStep{name: "InsertRoomParticipant", args: []any{nil, roomTestID, roomTestGuest}})
			}
			s, d := newRoomTestService(t, steps...)
			err := s.JoinRoom(context.Background(), roomTestGuest, "ABCDEF")
			if !errors.Is(err, tc.want) {
				t.Fatalf("JoinRoom = %v, want %v", err, tc.want)
			}
			wantCommits := 0
			if tc.want == nil {
				wantCommits = 1
			}
			if d.commits != wantCommits {
				t.Fatalf("commits = %d", d.commits)
			}
		})
	}
}

func TestRoomLeave(t *testing.T) {
	for _, tc := range []struct {
		name  string
		user  uuid.UUID
		count int64
	}{
		{"guest", roomTestGuest, 1}, {"host handoff", roomTestHost, 1}, {"last participant", roomTestHost, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []roomDBStep{roomStep("GetRoomForUpdate", "waiting"), participantStep(tc.user, false),
				{name: "DeleteRoomParticipant", args: []any{roomTestID, tc.user}}, countStep(tc.count)}
			if tc.count == 0 {
				steps = append(steps, roomDBStep{name: "DissolveRoom", args: []any{roomTestID}})
			} else if tc.user == roomTestHost {
				steps = append(steps, roomDBStep{name: "GetRemainingRoomParticipant", args: []any{roomTestID}, values: []any{roomTestGuest}}, roomDBStep{name: "ReassignRoomHost", args: []any{roomTestID, roomTestGuest}})
			}
			s, d := newRoomTestService(t, steps...)
			if err := s.LeaveRoom(context.Background(), tc.user, roomTestID); err != nil {
				t.Fatal(err)
			}
			if d.begins != 1 || d.commits != 1 {
				t.Fatalf("transaction counts: %d/%d", d.begins, d.commits)
			}
		})
	}
}

func TestRoomKick(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caller uuid.UUID
		ready  bool
		want   error
	}{
		{"success", roomTestHost, false, nil}, {"non-host", roomTestGuest, false, ErrNotHost}, {"ready", roomTestHost, true, ErrParticipantReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []roomDBStep{roomStep("GetRoomForUpdate", "waiting")}
			if tc.caller == roomTestHost {
				steps = append(steps, participantStep(roomTestGuest, tc.ready))
			}
			if tc.want == nil {
				steps = append(steps, roomDBStep{name: "DeleteRoomParticipant", args: []any{roomTestID, roomTestGuest}}, countStep(1))
			}
			s, d := newRoomTestService(t, steps...)
			if err := s.KickParticipant(context.Background(), tc.caller, roomTestID, roomTestGuest); !errors.Is(err, tc.want) {
				t.Fatalf("Kick = %v, want %v", err, tc.want)
			}
			if tc.want != nil && d.commits != 0 {
				t.Fatal("rejection committed")
			}
		})
	}
}

func TestRoomMarkReadySelfScoped(t *testing.T) {
	s, d := newRoomTestService(t, roomStep("GetRoomForUpdate", "waiting"), participantStep(roomTestGuest, false),
		roomDBStep{name: "MarkRoomParticipantReady", args: []any{roomTestID, roomTestGuest}})
	if err := s.MarkReady(context.Background(), roomTestGuest, roomTestID); err != nil {
		t.Fatal(err)
	}
	if d.commits != 1 {
		t.Fatal("ready not committed")
	}
}

func TestRoomWaitingOnlyMutations(t *testing.T) {
	for _, status := range []string{"counting_down", "in_progress", "finished", "dissolved"} {
		for _, method := range []string{"leave", "kick", "ready"} {
			t.Run(status+"/"+method, func(t *testing.T) {
				s, d := newRoomTestService(t, roomStep("GetRoomForUpdate", status))
				var err error
				switch method {
				case "leave":
					err = s.LeaveRoom(context.Background(), roomTestHost, roomTestID)
				case "kick":
					err = s.KickParticipant(context.Background(), roomTestHost, roomTestID, roomTestGuest)
				case "ready":
					err = s.MarkReady(context.Background(), roomTestGuest, roomTestID)
				}
				if !errors.Is(err, ErrRoomNotJoinable) || d.commits != 0 {
					t.Fatalf("err = %v, commits = %d", err, d.commits)
				}
			})
		}
	}
}

func TestRoomStartRaceGuards(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		caller       uuid.UUID
		count        int64
		unready      bool
		want         error
	}{
		{"two", "waiting", roomTestHost, 2, false, nil}, {"eight", "waiting", roomTestHost, 8, false, nil},
		{"non-host", "waiting", roomTestGuest, 2, false, ErrNotHost},
		{"countdown", "counting_down", roomTestHost, 2, false, ErrInvalidStatusTransition},
		{"in progress", "in_progress", roomTestHost, 2, false, ErrInvalidStatusTransition},
		{"finished", "finished", roomTestHost, 2, false, ErrInvalidStatusTransition},
		{"dissolved", "dissolved", roomTestHost, 2, false, ErrInvalidStatusTransition},
		{"empty", "waiting", roomTestHost, 0, false, ErrNotEnoughParticipants},
		{"one", "waiting", roomTestHost, 1, false, ErrNotEnoughParticipants},
		{"nine", "waiting", roomTestHost, 9, false, ErrRoomFull},
		{"unready", "waiting", roomTestHost, 2, true, ErrNotAllReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []roomDBStep{roomStep("GetRoomForUpdate", tc.status)}
			if tc.caller == roomTestHost && tc.status == "waiting" {
				steps = append(steps, countStep(tc.count))
				if tc.count >= 2 && tc.count <= 8 {
					steps = append(steps, roomDBStep{name: "RoomHasUnreadyParticipants", args: []any{roomTestID}, values: []any{tc.unready}})
				}
			}
			if tc.want == nil {
				steps = append(steps, roomDBStep{name: "StartRoomCountdown", args: []any{roomTestID}})
			}
			s, d := newRoomTestService(t, steps...)
			if err := s.StartRace(context.Background(), tc.caller, roomTestID); !errors.Is(err, tc.want) {
				t.Fatalf("StartRace = %v, want %v", err, tc.want)
			}
			wantCommits := 0
			if tc.want == nil {
				wantCommits = 1
			}
			if d.commits != wantCommits {
				t.Fatalf("commits = %d", d.commits)
			}
		})
	}
}

func TestRoomBeginRaceGuard(t *testing.T) {
	for _, status := range []string{"waiting", "counting_down", "in_progress", "finished", "dissolved"} {
		t.Run(status, func(t *testing.T) {
			steps := []roomDBStep{roomStep("GetRoomForUpdate", status)}
			want := ErrInvalidStatusTransition
			if status == "counting_down" {
				want = nil
				steps = append(steps, roomDBStep{name: "BeginRoomRace", args: []any{roomTestID}})
			}
			s, d := newRoomTestService(t, steps...)
			if err := s.BeginRace(context.Background(), roomTestID); !errors.Is(err, want) {
				t.Fatalf("BeginRace = %v, want %v", err, want)
			}
			if want != nil && d.commits != 0 {
				t.Fatal("rejection committed")
			}
		})
	}
}

func TestRoomCreate(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		raceErr, participantErr error
	}{
		{"success", nil, nil}, {"invalid race", pgx.ErrNoRows, nil}, {"participant failure", nil, errors.New("participant insert failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []roomDBStep{{name: "GetRaceType", args: []any{roomTestID}, values: []any{roomTestID, "distance", int64(500000), "500m"}, err: tc.raceErr}}
			if tc.raceErr == nil {
				steps = append(steps,
					roomDBStep{name: "InsertRoom", args: []any{nil, roomTestID, roomTestHost, "ABCDEF"}},
					roomDBStep{name: "InsertRoomParticipant", args: []any{nil, nil, roomTestHost}, err: tc.participantErr})
			}
			s, d := newRoomTestService(t, steps...)
			s.newJoinCode = func() (string, error) { return "ABCDEF", nil }
			code, err := s.CreateRoom(context.Background(), roomTestHost, roomTestID)
			switch {
			case tc.raceErr != nil:
				if !errors.Is(err, ErrInvalidRaceType) || d.begins != 0 {
					t.Fatalf("invalid race: %v, begins %d", err, d.begins)
				}
			case tc.participantErr != nil:
				if !errors.Is(err, tc.participantErr) || d.commits != 0 || d.rollbacks != 1 {
					t.Fatalf("failed atomic create: %v, tx = %+v", err, d)
				}
			default:
				if err != nil || code != "ABCDEF" || d.begins != 1 || d.commits != 1 {
					t.Fatalf("create: code %q, err %v, tx = %+v", code, err, d)
				}
			}
		})
	}
}

func TestRoomCreateDoesNotRetryOtherFailures(t *testing.T) {
	for _, code := range []string{"23503", "40001"} {
		t.Run(code, func(t *testing.T) {
			cause := &pgconn.PgError{Code: code}
			s, d := newRoomTestService(t,
				roomDBStep{name: "GetRaceType", values: []any{roomTestID, "distance", int64(500000), "500m"}},
				roomDBStep{name: "InsertRoom", err: cause})
			_, err := s.CreateRoom(context.Background(), roomTestHost, roomTestID)
			if !errors.Is(err, cause) || d.begins != 1 || d.rollbacks != 1 || d.commits != 0 {
				t.Fatalf("unexpected retry: %v, tx %+v", err, d)
			}
		})
	}
}

func TestRoomMissingRows(t *testing.T) {
	for _, method := range []string{"join", "leave", "kick", "ready", "start", "begin"} {
		t.Run(method, func(t *testing.T) {
			query := "GetRoomForUpdate"
			if method == "join" {
				query = "GetActiveRoomByJoinCode"
			}
			s, d := newRoomTestService(t, roomDBStep{name: query, err: pgx.ErrNoRows})
			ctx := context.Background()
			var err error
			switch method {
			case "join":
				err = s.JoinRoom(ctx, roomTestGuest, "ABCDEF")
			case "leave":
				err = s.LeaveRoom(ctx, roomTestGuest, roomTestID)
			case "kick":
				err = s.KickParticipant(ctx, roomTestHost, roomTestID, roomTestGuest)
			case "ready":
				err = s.MarkReady(ctx, roomTestGuest, roomTestID)
			case "start":
				err = s.StartRace(ctx, roomTestHost, roomTestID)
			case "begin":
				err = s.BeginRace(ctx, roomTestID)
			}
			if !errors.Is(err, ErrRoomNotFound) || d.commits != 0 {
				t.Fatalf("missing room: %v, commits %d", err, d.commits)
			}
		})
	}
}

func TestRoomMissingParticipant(t *testing.T) {
	for _, method := range []string{"leave", "kick", "ready"} {
		t.Run(method, func(t *testing.T) {
			s, d := newRoomTestService(t, roomStep("GetRoomForUpdate", "waiting"), roomDBStep{name: "GetRoomParticipant", err: pgx.ErrNoRows})
			ctx := context.Background()
			var err error
			switch method {
			case "leave":
				err = s.LeaveRoom(ctx, roomTestGuest, roomTestID)
			case "kick":
				err = s.KickParticipant(ctx, roomTestHost, roomTestID, roomTestGuest)
			case "ready":
				err = s.MarkReady(ctx, roomTestGuest, roomTestID)
			}
			if !errors.Is(err, ErrParticipantNotFound) || d.commits != 0 {
				t.Fatalf("missing participant: %v, commits %d", err, d.commits)
			}
		})
	}
}

func TestRoomCreateRetryBound(t *testing.T) {
	steps := []roomDBStep{{name: "GetRaceType", values: []any{roomTestID, "distance", int64(500000), "500m"}}}
	for i := 0; i < 10; i++ {
		steps = append(steps, roomDBStep{name: "InsertRoom", err: &pgconn.PgError{Code: "23505"}})
	}
	s, d := newRoomTestService(t, steps...)
	calls := 0
	s.newJoinCode = func() (string, error) { calls++; return "ABCDEF", nil }
	code, err := s.CreateRoom(context.Background(), roomTestHost, roomTestID)
	var pgErr *pgconn.PgError
	if code != "" || !errors.As(err, &pgErr) || calls != 10 || d.begins != 10 || d.rollbacks != 10 || d.commits != 0 {
		t.Fatalf("retry bound: code %q err %v calls %d tx %+v", code, err, calls, d)
	}
}
