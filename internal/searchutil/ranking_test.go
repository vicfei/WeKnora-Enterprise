package searchutil

import (
	"math/rand"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// legacy defaults mirrored from types.LegacyComposite* for the pin test
func legacyResolved() types.ResolvedRanking {
	return (&types.RankingConfig{}).Resolve()
}

func TestCompositeScoreMatchesLegacyFormula(t *testing.T) {
	r := legacyResolved()
	if got := r.RerankWeight; got != 0.6 {
		t.Fatalf("legacy rerank weight = %v, want 0.6", got)
	}
	cases := []struct{ model, base, src, rec float64 }{
		{0.9, 0.033, 1.0, 0},
		{0.5, 0.016, 0.95, 0},
		{0.0, 0.0, 1.0, 0},
		{1.0, 1.0, 1.0, 0},
		{0.42, 0.007, 1.0, 0.8}, // recency inert: default weight 0
	}
	for _, c := range cases {
		want := 0.6*c.model + 0.3*c.base + 0.1*c.src
		if want > 1 {
			want = 1
		}
		if got := CompositeScore(c.model, c.base, c.src, c.rec, r); got != want {
			t.Fatalf("CompositeScore(%v) = %v, want legacy %v", c, got, want)
		}
	}
}

func TestCompositeScoreClampAndCustomWeights(t *testing.T) {
	r := types.ResolvedRanking{RerankWeight: 1.0}
	if got := CompositeScore(2.0, 0, 1, 0, r); got != 1 {
		t.Fatalf("clamp-high = %v, want 1", got)
	}
	if got := CompositeScore(-1, 0, 1, 0, r); got != 0 {
		t.Fatalf("clamp-low = %v, want 0", got)
	}
	r2 := types.ResolvedRanking{RerankWeight: 0.5, RetrievalWeight: 0.5}
	if got := CompositeScore(0.8, 0.2, 1, 0, r2); got != 0.5 {
		t.Fatalf("custom weights = %v, want 0.5", got)
	}
}

func TestRecencySignal(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	if got := RecencySignal("", 180, now); got != 0 {
		t.Fatalf("missing timestamp = %v, want 0", got)
	}
	if got := RecencySignal("not-a-time", 180, now); got != 0 {
		t.Fatalf("unparsable timestamp = %v, want 0", got)
	}
	if got := RecencySignal(now.Format(time.RFC3339), 180, now); got != 1 {
		t.Fatalf("fresh doc = %v, want 1", got)
	}
	// exactly one half-life ago => 0.5
	old := now.AddDate(0, 0, -180)
	if got := RecencySignal(old.Format(time.RFC3339), 180, now); mathAbs(got-0.5) > 1e-9 {
		t.Fatalf("one half-life = %v, want 0.5", got)
	}
	if got := RecencySignal(old.Format(time.RFC3339), 0, now); got != 0 {
		t.Fatalf("disabled half-life = %v, want 0", got)
	}
}

func mathAbs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// naiveMMR is the straightforward O(k·n·pairs) reference implementation.
func naiveMMR(k int, lambda float64, scores []float64, tokenSets []map[string]struct{}) []int {
	var selected []int
	inSel := make([]bool, len(scores))
	for len(selected) < k && len(selected) < len(scores) {
		bestIdx, bestScore := -1, -1.0
		for i := range scores {
			if inSel[i] {
				continue
			}
			maxRed := 0.0
			for _, j := range selected {
				if jac := Jaccard(tokenSets[i], tokenSets[j]); jac > maxRed {
					maxRed = jac
				}
			}
			mmr := lambda*scores[i] - (1.0-lambda)*maxRed
			if mmr > bestScore {
				bestScore = mmr
				bestIdx = i
			}
		}
		selected = append(selected, bestIdx)
		inSel[bestIdx] = true
	}
	return selected
}

func TestSelectMMRMatchesNaive(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	tokens := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	for trial := 0; trial < 200; trial++ {
		n := 1 + rng.Intn(12)
		k := rng.Intn(n + 1)
		lambda := []float64{0.7, 1.0, 0.5, 0.0}[rng.Intn(4)]
		scores := make([]float64, n)
		tokenSets := make([]map[string]struct{}, n)
		for i := range scores {
			scores[i] = float64(rng.Intn(100)) / 100.0
			ts := map[string]struct{}{}
			for _, tok := range tokens {
				if rng.Intn(2) == 1 {
					ts[tok] = struct{}{}
				}
			}
			tokenSets[i] = ts
		}
		got := SelectMMR(k, lambda, scores, tokenSets)
		want := naiveMMR(k, lambda, scores, tokenSets)
		if len(got) != len(want) {
			t.Fatalf("trial %d: len = %d, want %d", trial, len(got), len(want))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("trial %d (n=%d k=%d λ=%v): selection %v != naive %v",
					trial, n, k, lambda, got, want)
			}
		}
	}
}

func TestSelectMMREdgeCases(t *testing.T) {
	if got := SelectMMR(0, 0.7, []float64{1}, []map[string]struct{}{{}}); got != nil {
		t.Fatalf("k=0 = %v, want nil", got)
	}
	if got := SelectMMR(3, 0.7, nil, nil); got != nil {
		t.Fatalf("empty = %v, want nil", got)
	}
	// mismatched tokenSets length is a caller bug; treat as no-op
	if got := SelectMMR(2, 0.7, []float64{1, 2}, []map[string]struct{}{{}}); got != nil {
		t.Fatalf("mismatched = %v, want nil", got)
	}
	// lambda=1.0 (pure relevance) must return candidates in score order
	scores := []float64{0.3, 0.9, 0.6}
	sets := []map[string]struct{}{{"x": {}}, {"x": {}}, {"x": {}}}
	got := SelectMMR(3, 1.0, scores, sets)
	want := []int{1, 2, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pure-relevance order = %v, want %v", got, want)
		}
	}
}
