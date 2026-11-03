package agent

import (
	"strings"
	"testing"
)

// The catalog cap exists because the detailed rendering is linear in KB count
// with no ceiling: a kb=all agent on a 541-KB tenant produced a 219k-token
// first user message, past a 200k window, before the model ran anything
// (upstream findings register U-14). These tests pin both forms.

func detailedCatalogKBs(n int) []*KnowledgeBaseInfo {
	out := make([]*KnowledgeBaseInfo, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, &KnowledgeBaseInfo{
			ID:          "kb-id-000",
			Name:        "hotpot-eval-hp-0000",
			Type:        "document",
			DocCount:    10,
			Description: "hotpot eval question corpus",
			RecentDocs: []RecentDocInfo{{
				KnowledgeID: "doc-1",
				Title:       "Ken Pruitt",
				Description: "Ken Pruitt was a Republican member of the Florida Senate.",
			}},
		})
	}
	return out
}

func TestCatalogDetailedUnderCap(t *testing.T) {
	got := formatKnowledgeBaseList(detailedCatalogKBs(maxDetailedCatalogKBs))
	for _, want := range []string{
		"<knowledge_bases>",
		"<knowledge_base id=\"kb-id-000\"",
		"<description>hotpot eval question corpus</description>",
		"<recent_documents>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("detailed form missing %q\n%v", want, got[:min(len(got), 400)])
		}
	}
	if strings.Contains(got, "catalog=\"compact\"") {
		t.Fatal("at-cap catalog must not use the compact form")
	}
}

func TestCatalogCompactOverCap(t *testing.T) {
	got := formatKnowledgeBaseList(detailedCatalogKBs(541))
	for _, want := range []string{
		"<knowledge_bases count=\"541\" catalog=\"compact\">",
		"<knowledge_base name=\"hotpot-eval-hp-0000\"",
		"531 more knowledge bases omitted",
		"<guidance>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact form missing %q\n%v", want, got[:min(len(got), 400)])
		}
	}
	for _, banned := range []string{"<recent_documents>", "<description>", "id=\"kb-id-000\""} {
		if strings.Contains(got, banned) {
			t.Fatalf("compact form must not contain %q (that is the bloat being capped)", banned)
		}
	}
	if n := strings.Count(got, "<knowledge_base "); n != catalogNameSample {
		t.Fatalf("compact form listed %d KBs, want the %d-name sample", n, catalogNameSample)
	}
}

func TestCatalogCompactSizeBounded(t *testing.T) {
	// The whole point of the cap: 541 KBs must render to something a first
	// user message can afford, not a six-figure token count.
	got := formatKnowledgeBaseList(detailedCatalogKBs(541))
	if len(got) > 8000 {
		t.Fatalf("compact catalog too large: %d bytes", len(got))
	}
}
