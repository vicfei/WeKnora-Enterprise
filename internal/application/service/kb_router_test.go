package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestSortKBScoresDesc(t *testing.T) {
	in := []KBScore{{KBID: "a", Score: 0.2}, {KBID: "b", Score: 0.9}, {KBID: "c", Score: 0.5}}
	sortKBScoresDesc(in)
	if in[0].KBID != "b" || in[1].KBID != "c" || in[2].KBID != "a" {
		t.Fatalf("order wrong: %v", in)
	}
}

func TestDocDigestPrefersManualContent(t *testing.T) {
	k := &types.Knowledge{Title: "T"}
	if err := k.SetManualMetadata(types.NewManualKnowledgeMetadata("hello content", "publish", 1)); err != nil {
		t.Fatal(err)
	}
	d := docDigest(k)
	if d != "T\nhello content" {
		t.Fatalf("digest=%q", d)
	}
}

func TestDocDigestTruncatesLongContent(t *testing.T) {
	long := make([]byte, kbRouterDigestMaxRunes*3)
	for i := range long {
		long[i] = 'x'
	}
	k := &types.Knowledge{Title: "T"}
	if err := k.SetManualMetadata(types.NewManualKnowledgeMetadata(string(long), "publish", 1)); err != nil {
		t.Fatal(err)
	}
	d := docDigest(k)
	want := 2 + kbRouterDigestMaxRunes // "T\n" + capped content
	if got := len([]rune(d)); got != want {
		t.Fatalf("digest runes=%d want=%d", got, want)
	}
}

func TestDocDigestFallbackToTitleOnly(t *testing.T) {
	k := &types.Knowledge{Title: "only-title"}
	if d := docDigest(k); d != "only-title" {
		t.Fatalf("digest=%q", d)
	}
}

func TestFoldBestPerKB(t *testing.T) {
	in := []KBScore{
		{KBID: "a", Name: "a", Score: 0.7},
		{KBID: "b", Name: "b", Score: 0.9},
		{KBID: "a", Name: "a", Score: 0.5},
	}
	out := foldBestPerKB(in)
	if len(out) != 2 {
		t.Fatalf("want 2 KBs, got %d", len(out))
	}
	sortKBScoresDesc(out)
	if out[0].KBID != "b" || out[0].Score != 0.9 || out[1].KBID != "a" || out[1].Score != 0.7 {
		t.Fatalf("fold wrong: %v", out)
	}
}
