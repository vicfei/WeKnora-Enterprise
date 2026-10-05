package service

import (
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// The governance switch of time-aware retrieval (设计 §4.1): on a
// require_business_date KB a missing business date is a data bug and gets
// rejected; everywhere else the ingest path is byte-for-byte unchanged.
func TestEnforceBusinessDateRequirement(t *testing.T) {
	date := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		kb      *types.KnowledgeBase
		date    *time.Time
		wantErr bool
	}{
		{"switch off tolerates a missing date", &types.KnowledgeBase{}, nil, false},
		{"switch off with a date is fine", &types.KnowledgeBase{}, &date, false},
		{"switch on accepts a date", &types.KnowledgeBase{RequireBusinessDate: true}, &date, false},
		{"switch on rejects a missing date", &types.KnowledgeBase{RequireBusinessDate: true}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := enforceBusinessDateRequirement(tc.kb, tc.date, "test")
			if got := err != nil; got != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
