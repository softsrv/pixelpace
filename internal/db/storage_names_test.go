package db_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Migration 000021 renamed races, race_types and race_type_segments and kept
// compatibility views under the old names until a contract migration drops
// them. These checks read the source (nothing runs) to pin that the app has
// moved to the new names: queries name only the new tables and columns, the
// generated package carries nothing produced from the views, and app code
// never touches the old twin columns still present on rooms and
// quick_match_queue.

// stripSQLComments drops `--` line comments so prose may still say "race".
func stripSQLComments(sql string) string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestQueriesNameOnlyRenamedStorage(t *testing.T) {
	files, err := filepath.Glob("../../db/queries/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob queries: %v (%d files)", err, len(files))
	}
	old := regexp.MustCompile(`(?i)\b(races|race_types|race_type_segments|race_type_id|mode)\b`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(stripSQLComments(string(b)), "\n") {
			if m := old.FindString(line); m != "" {
				t.Errorf("%s:%d names old storage identifier %q: %s", filepath.Base(f), i+1, m, strings.TrimSpace(line))
			}
		}
	}
}

func TestGeneratedPackageHasNoCompatibilityViewCode(t *testing.T) {
	fset := token.NewFileSet()
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	var funcs []string
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, src, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						types[ts.Name.Name] = true
					}
				}
			case *ast.FuncDecl:
				funcs = append(funcs, d.Name.Name)
			}
		}
	}
	for _, want := range []string{"Activity", "Preset", "PresetSegment"} {
		if !types[want] {
			t.Errorf("generated package lacks model %s", want)
		}
	}
	for _, banned := range []string{"Race", "RaceType", "RaceTypeSegment"} {
		if types[banned] {
			t.Errorf("generated package has model %s from a compatibility view or old table", banned)
		}
	}
	// A method named for an old table: GetRaceType, ListDistanceRaceTypes,
	// LockRace, CreateRace, FinishRace, ListUnfinishedRaces and the like.
	// RaceParticipant*/RaceRoom*/RaceTelemetry* name other tables and stay.
	oldMethod := regexp.MustCompile(`RaceType|^(Lock|Create|Finish)Race$|UnfinishedRaces$`)
	for _, name := range funcs {
		if oldMethod.MatchString(name) {
			t.Errorf("generated package has %s, named for an old table", name)
		}
	}
	for name := range types {
		if oldMethod.MatchString(name) {
			t.Errorf("generated package has type %s, named for an old table", name)
		}
	}
}

func TestAppCodeLeavesOldTwinColumnsAlone(t *testing.T) {
	// Room.Mode/RaceTypeID and QuickMatchQueue.RaceTypeID mirror the new
	// columns only for the pre-rename binary; the sync trigger fills them.
	twin := regexp.MustCompile(`\.(Mode|RaceTypeID)\b|\b(Mode|RaceTypeID)\s*:`)
	for _, dir := range []string{"../app", "../http"} {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(b), "\n") {
				if m := twin.FindString(line); m != "" {
					t.Errorf("%s:%d uses old twin field %q", path, i+1, m)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
