package app

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/softsrv/starter/internal/db"
)

// These structural checks cover the query surface and the explicit lifecycle
// exclusions; behavior is exercised separately through RoomService.
func TestRoomQuerySurface(t *testing.T) {
	sql, err := os.ReadFile("../../db/queries/rooms.sql")
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("room_service.go")
	if err != nil {
		t.Fatal(err)
	}
	queries := regexp.MustCompile(`-- name: (\w+) :(?:one|exec|many)`).FindAllStringSubmatch(string(sql), -1)
	if len(queries) == 0 {
		t.Fatal("no annotated room queries")
	}
	querier := reflect.TypeOf((*db.Querier)(nil)).Elem()
	generated := reflect.TypeOf((*db.Queries)(nil))
	for _, query := range queries {
		name := query[1]
		if !strings.Contains(string(source), "."+name+"(") {
			t.Errorf("unused room query: %s", name)
		}
		if _, ok := querier.MethodByName(name); !ok {
			t.Errorf("Querier missing %s", name)
		}
		if _, ok := generated.MethodByName(name); !ok {
			t.Errorf("Queries missing %s", name)
		}
	}
	for _, forbidden := range []string{"internal/realtime", "Broadcast", "time.After", "time.Sleep", "time.NewTimer", "time.NewTicker", "go func", `"finished"`} {
		if strings.Contains(string(source), forbidden) {
			t.Errorf("forbidden lifecycle responsibility: %s", forbidden)
		}
	}
	if regexp.MustCompile(`(?i)SET\s+status\s*=\s*'finished'`).Match(sql) {
		t.Fatal("room service must not finish races")
	}
}
