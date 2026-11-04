package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// TestRacePageLaneCount renders race.html with various LaneCount values and
// verifies that the lane tag renders "N lane" / "N lanes" when LaneCount > 0,
// and renders nothing when LaneCount is 0 or absent (nil / not in map).
func TestRacePageLaneCount(t *testing.T) {
	render := func(data map[string]any) string {
		t.Helper()
		tmpl, err := template.ParseFS(FS, "templates/base.html", "templates/race.html")
		if err != nil {
			t.Fatalf("parse race page: %v", err)
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
			t.Fatalf("execute race page: %v", err)
		}
		return buf.String()
	}

	tests := []struct {
		name      string
		laneCount any
		wantTag   string
		shouldTag bool
	}{
		{
			name:      "LaneCount absent",
			laneCount: nil,
			shouldTag: false,
		},
		{
			name:      "LaneCount 0",
			laneCount: 0,
			shouldTag: false,
		},
		{
			name:      "LaneCount 1 singular",
			laneCount: 1,
			wantTag:   `<span class="pp-tag pp-arcade">1 lane</span>`,
			shouldTag: true,
		},
		{
			name:      "LaneCount 3 plural",
			laneCount: 3,
			wantTag:   `<span class="pp-tag pp-arcade">3 lanes</span>`,
			shouldTag: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := map[string]any{
				"User": map[string]any{"Email": "rower@example.com"},
				"Race": map[string]any{"Label": "2000m"},
			}
			if tc.laneCount != nil {
				data["LaneCount"] = tc.laneCount
			}
			out := render(data)
			hasTag := strings.Contains(out, "pp-tag pp-arcade")
			if tc.shouldTag {
				if !strings.Contains(out, tc.wantTag) {
					t.Errorf("rendered output missing %q; got:\n%s", tc.wantTag, out)
				}
			} else {
				if hasTag {
					t.Errorf("rendered output unexpectedly contains lane tag when LaneCount is %v; got:\n%s", tc.laneCount, out)
				}
			}
		})
	}
}
