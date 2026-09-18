package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestBuildAgentTimeCondition(t *testing.T) {
	local := func(s string) time.Time {
		tt, err := time.ParseInLocation("2006-01-02", s, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return tt
	}
	cases := []struct {
		name         string
		min, max     string
		wantNil      bool
		wantOp       string
		wantStartDay string
		wantHasEnd   bool
	}{
		{"both bounds", "2026-06-01", "2026-09-30", false, types.SearchTimeOpBetween, "2026-06-01", true},
		{"min only", "2026-06-01", "", false, types.SearchTimeOpGte, "2026-06-01", false},
		{"max only", "", "2026-06-30", false, types.SearchTimeOpLte, "2026-06-30T23:59:59", false},
		{"none", "", "", true, "", "", false},
		{"whitespace only", "  ", " ", true, "", "", false},
		{"unparseable min", "June", "2026-09-30", false, types.SearchTimeOpLte, "2026-09-30T23:59:59", false},
		{"inverted window", "2026-09-30", "2026-06-01", true, "", "", false},
		{"full rfc3339 tolerated", "2026-06-01T00:00:00Z", "2026-09-30T23:59:59Z", false, types.SearchTimeOpBetween, "2026-06-01T00:00:00Z", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildAgentTimeCondition(context.Background(), tc.min, tc.max)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %+v, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("got nil")
			}
			if got.Op != tc.wantOp {
				t.Errorf("op = %q, want %q", got.Op, tc.wantOp)
			}
			if got.Field != types.SearchTimeFieldBusinessDate || got.Strictness != types.SearchTimeStrictnessFilter {
				t.Errorf("field/strictness = %q/%q", got.Field, got.Strictness)
			}
			var wantStart time.Time
			if strings.HasSuffix(tc.wantStartDay, "Z") {
				wantStart, _ = time.Parse(time.RFC3339, tc.wantStartDay)
			} else if strings.Contains(tc.wantStartDay, "T") {
				wantStart, _ = time.ParseInLocation("2006-01-02T15:04:05", tc.wantStartDay, time.Local)
			} else {
				wantStart = local(tc.wantStartDay)
			}
			if !got.Start.Equal(wantStart) {
				t.Errorf("start = %v, want %v", got.Start, wantStart)
			}
			if tc.wantHasEnd && got.End.IsZero() {
				t.Error("expected non-zero end")
			}
			if !tc.wantHasEnd && !got.End.IsZero() {
				t.Errorf("expected zero end, got %v", got.End)
			}
		})
	}
}
