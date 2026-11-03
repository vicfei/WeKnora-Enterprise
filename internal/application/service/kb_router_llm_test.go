package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestDictScoreKBsRanksByQueryCoverage(t *testing.T) {
	candidates := []*types.KnowledgeBase{
		{ID: "eye", Name: "眼科手术库", Description: "白内障 手术 门诊 统计"},
		{ID: "bond", Name: "债券池", Description: "信用债 久期 评级"},
		{ID: "eye2", Name: "门诊手术周报", Description: "手术 门诊"},
	}
	scores := dictScoreKBs(candidates, "眼科 白内障 手术")
	if scores[0].KBID != "eye" {
		t.Fatalf("top = %s (%.2f), want eye", scores[0].KBID, scores[0].Score)
	}
	if scores[0].Score != 1.0 {
		t.Fatalf("eye coverage = %.2f, want 1.0", scores[0].Score)
	}
	// eye2 covers 手术 only → 1/3.
	for _, s := range scores {
		if s.KBID == "eye2" && s.Score > 0.34+1e-9 {
			t.Fatalf("eye2 coverage = %.2f, want ~0.33", s.Score)
		}
	}
}

func TestDictScoreKBsEmptyQuery(t *testing.T) {
	if dictScoreKBs([]*types.KnowledgeBase{{ID: "a", Name: "x"}}, "  ") != nil {
		t.Fatal("empty query must yield no scoring")
	}
}

func TestParseKBRouterSelection(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []int
	}{
		{"clean json", `{"kb_indexes": [2, 5]}`, []int{2, 5}},
		{"prose wrapped", "好的，需要查这两个库：\n{\"kb_indexes\":[1]} 以上。", []int{1}},
		{"empty selection", `{"kb_indexes": []}`, []int{}},
		{"wrong shape", `{"kb_ids": [1]}`, nil},
		{"no json", "我认为不需要选库。", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseKBRouterSelection(tc.raw)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestKBRouterLLMEnabledEnv(t *testing.T) {
	cases := map[string]bool{"": true, "0": false, "false": false, "OFF": false, "1": true}
	for raw, want := range cases {
		t.Setenv(kbRouterLLMEnabledEnv, raw)
		if got := kbRouterLLMEnabled(); got != want {
			t.Fatalf("env %q -> %v, want %v", raw, got, want)
		}
	}
}
