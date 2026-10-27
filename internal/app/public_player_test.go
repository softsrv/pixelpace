package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/users"
)

func TestNewPublicPlayer(t *testing.T) {
	t.Run("named player keeps the name", func(t *testing.T) {
		id := uuid.Must(uuid.NewV7())
		got := NewPublicPlayer(id, pgtype.Text{String: "Rower", Valid: true})
		if got.Name != "Rower" || got.UserID != id {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("unnamed player gets a stable placeholder without the email", func(t *testing.T) {
		id := uuid.Must(uuid.NewV7())
		const email = "a@b.c"
		first := NewPublicPlayer(id, pgtype.Text{})
		second := NewPublicPlayer(id, pgtype.Text{})
		if first != second {
			t.Fatalf("placeholder not stable: %+v vs %+v", first, second)
		}
		if !strings.HasPrefix(first.Name, "Racer ") || strings.Contains(first.Name, "@") || strings.Contains(first.Name, email) {
			t.Fatalf("bad placeholder %q", first.Name)
		}
	})

	t.Run("back-to-back v7 ids get distinct placeholders refused by validation", func(t *testing.T) {
		seen := make(map[string]struct{}, 1000)
		for range 1000 {
			p := NewPublicPlayer(uuid.Must(uuid.NewV7()), pgtype.Text{})
			if _, dup := seen[p.Name]; dup {
				t.Fatalf("duplicate placeholder %q", p.Name)
			}
			seen[p.Name] = struct{}{}
			if err := users.ValidateDisplayName(p.Name); err == nil {
				t.Fatalf("placeholder %q passes name validation", p.Name)
			}
		}
	})

	t.Run("an empty valid name is treated as no name", func(t *testing.T) {
		p := NewPublicPlayer(uuid.Must(uuid.NewV7()), pgtype.Text{String: "", Valid: true})
		if !strings.HasPrefix(p.Name, "Racer ") {
			t.Fatalf("got %q", p.Name)
		}
	})

	t.Run("view serialises to exactly user_id and name", func(t *testing.T) {
		raw, err := json.Marshal(NewPublicPlayer(uuid.Must(uuid.NewV7()), pgtype.Text{}))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if len(m) != 2 || m["user_id"] == nil || m["name"] == nil {
			t.Fatalf("keys: %v", m)
		}
		typ := reflect.TypeOf(PublicPlayer{})
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if strings.Contains(strings.ToLower(f.Name), "email") || strings.Contains(strings.ToLower(string(f.Tag)), "email") {
				t.Fatalf("field %s carries email", f.Name)
			}
		}
	})
}
