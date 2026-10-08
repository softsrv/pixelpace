package app

import (
	"math"
	"math/rand/v2"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/db"
)

var _ func(*db.Queries, pgxBeginner, *RoomService, QuickMatchServiceConfig) *QuickMatchService = NewQuickMatchService

func TestQuickMatchQuerySurface(t *testing.T) {
	source, err := os.ReadFile("quick_match_service.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"elo", "sharedskill", "crossplayer"} {
		if strings.Contains(strings.ToLower(string(source)), forbidden) {
			t.Errorf("forbidden rating term: %s", forbidden)
		}
	}
	sql, err := os.ReadFile("../../db/queries/quick_match.sql")
	if err != nil {
		t.Fatal(err)
	}
	queries := regexp.MustCompile(`-- name: (\w+) :(?:one|exec|many)`).FindAllStringSubmatch(string(sql), -1)
	if len(queries) != 4 {
		t.Fatalf("query count = %d", len(queries))
	}
	querier := reflect.TypeOf((*db.Querier)(nil)).Elem()
	for _, query := range queries {
		if _, ok := querier.MethodByName(query[1]); !ok {
			t.Errorf("missing generated query %s", query[1])
		}
		if !strings.Contains(string(source), "."+query[1]+"(") {
			t.Errorf("unused query %s", query[1])
		}
	}
}

func TestQuickMatchRating(t *testing.T) {
	var finishes []db.RecentFinishesByRaceTypeRow
	for _, ms := range []int32{1000, 2000, 3000, 4000, 5000, 99000} {
		finishes = append(finishes, db.RecentFinishesByRaceTypeRow{ElapsedMilliseconds: pgtype.Int4{Int32: ms, Valid: true}})
	}
	if average, rated := quickMatchRating(finishes); average != 3 || !rated {
		t.Fatalf("last five average = %v, rated %v", average, rated)
	}
	if average, rated := quickMatchRating(nil); average != 0 || rated {
		t.Fatalf("no history = %v, rated %v", average, rated)
	}
	if average, rated := quickMatchRating(finishes[:2]); average != 1.5 || !rated {
		t.Fatalf("short history = %v, rated %v", average, rated)
	}
	if _, rated := quickMatchRating([]db.RecentFinishesByRaceTypeRow{{}}); rated {
		t.Fatal("null elapsed time fabricated a rating")
	}
}

func TestQuickMatchWindows(t *testing.T) {
	for _, tc := range []struct {
		wait  time.Duration
		width float64
	}{
		{0, 5}, {time.Nanosecond, 5}, {10*time.Second - time.Nanosecond, 5},
		{10 * time.Second, 10}, {10*time.Second + time.Nanosecond, 10},
		{30*time.Second - time.Nanosecond, 10}, {30 * time.Second, 20}, {30*time.Second + time.Nanosecond, 20},
		{60*time.Second - time.Nanosecond, 20}, {60 * time.Second, math.Inf(1)}, {60*time.Second + time.Nanosecond, math.Inf(1)},
	} {
		if got := windowWidth(tc.wait); got != tc.width {
			t.Errorf("window(%v) = %v, want %v", tc.wait, got, tc.width)
		}
	}
	now := time.Unix(100, 0)
	s := NewQuickMatchService(nil, nil, nil, QuickMatchServiceConfig{Now: func() time.Time { return now }})
	a := quickMatchRacer{average: 60, rated: true, joinedAt: now.Add(-time.Minute)}
	b := quickMatchRacer{average: 68, rated: true, joinedAt: now}
	if quickMatchCompatible(a, b, s.cfg.Now()) || quickMatchCompatible(b, a, s.cfg.Now()) {
		t.Fatal("asymmetric windows paired")
	}
	now = now.Add(10 * time.Second)
	if !quickMatchCompatible(a, b, s.cfg.Now()) {
		t.Fatal("mutual windows did not pair")
	}
	b.average = 1000
	if quickMatchCompatible(a, b, s.cfg.Now()) {
		t.Fatal("one unbounded window overrode the other")
	}
	now = now.Add(50 * time.Second)
	if !quickMatchCompatible(a, b, s.cfg.Now()) {
		t.Fatal("both unbounded windows should pair")
	}
	b.rated = false
	if quickMatchCompatible(a, b, s.cfg.Now()) {
		t.Fatal("unrated paired with rated")
	}
}

func quickMatchTestRacers(n int, joined time.Time) []quickMatchRacer {
	racers := make([]quickMatchRacer, n)
	for i := range racers {
		racers[i] = quickMatchRacer{userID: uuid.Must(uuid.NewV7()), joinedAt: joined}
	}
	return racers
}

func TestQuickMatchRoomFormation(t *testing.T) {
	now := time.Unix(100, 0)
	for _, tc := range []struct {
		name  string
		count int
		wait  time.Duration
		want  int
	}{
		{"eight immediate", 8, 0, 8}, {"one never", 1, time.Hour, 0},
		{"two before deadline", 2, 10*time.Second - time.Nanosecond, 0},
		{"two at deadline", 2, 10 * time.Second, 2}, {"cap eight", 12, 0, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			racers := quickMatchTestRacers(tc.count, now.Add(-tc.wait))
			if got := selectQuickMatch(racers, racers[0].userID, now); len(got) != tc.want {
				t.Fatalf("group size = %d, want %d", len(got), tc.want)
			}
		})
	}
	racers := quickMatchTestRacers(3, now.Add(-10*time.Second))
	racers[1].rated, racers[1].average = true, 60
	group := selectQuickMatch(racers, racers[0].userID, now)
	if len(group) != 2 || group[0].userID != racers[0].userID || group[1].userID != racers[2].userID {
		t.Fatalf("unrated group = %+v", group)
	}
	for i := range racers {
		racers[i].rated = true
		racers[i].average = 60 + float64(i)*8
	}
	// Adjacent racers fit, but the first and third do not; no transitive groups.
	if group := selectQuickMatch(racers, racers[1].userID, now); len(group) != 2 {
		t.Fatalf("pairwise compatibility: %+v", group)
	}
	if group := selectQuickMatch(racers, uuid.Nil, now); len(group) != 0 {
		t.Fatal("absent caller formed group")
	}
	// An unrelated old entry must not start a younger group's timer.
	racers[0].rated = false
	racers[1].joinedAt, racers[2].joinedAt = now, now
	racers[2].average = racers[1].average
	if group := selectQuickMatch(racers, racers[1].userID, now); len(group) != 0 {
		t.Fatal("unrelated entry supplied room timer")
	}
}

func TestQuickMatchHostLottery(t *testing.T) {
	for _, rated := range []bool{true, false} {
		group := quickMatchTestRacers(3, time.Time{})
		for i := range group {
			group[i].rated = rated
			group[i].average = 60
		}
		eligible := map[uuid.UUID]bool{group[0].userID: true, group[1].userID: true, group[2].userID: true}
		if rated {
			group[0].average = 70
			delete(eligible, group[0].userID)
		}
		seen := make(map[uuid.UUID]bool)
		for seed := uint64(0); seed < 50; seed++ {
			rng := rand.New(rand.NewPCG(seed, 1))
			s := NewQuickMatchService(nil, nil, nil, QuickMatchServiceConfig{IntN: rng.IntN})
			host := quickMatchHost(group, s.cfg.IntN)
			if !eligible[host] {
				t.Fatalf("ineligible host %v", host)
			}
			seen[host] = true
		}
		if len(seen) != len(eligible) {
			t.Fatalf("lottery fixed: %v", seen)
		}
	}
	group := quickMatchTestRacers(3, time.Time{})
	for i := range group {
		group[i].rated = true
		group[i].average = float64(100 - i)
	}
	if host := quickMatchHost(group, func(n int) int {
		if n != 1 {
			t.Errorf("eligible count %d", n)
		}
		return 0
	}); host != group[2].userID {
		t.Fatal("host was not lowest average")
	}
}
