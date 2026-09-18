package chatpipeline

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestApplyIntentPromptOverride_AgentOverrideWins(t *testing.T) {
	cm := &types.ChatManage{
		PipelineRequest: types.PipelineRequest{
			IntentPromptOverrides: map[string]string{"chitchat": "agent prompt"},
		},
		PipelineState: types.PipelineState{Intent: types.IntentChitchat},
	}
	global := map[string]string{"chitchat": "global prompt"}

	if !applyIntentPromptOverride(cm, global) {
		t.Fatal("expected applied=true")
	}
	if cm.SystemPromptOverride != "agent prompt" {
		t.Errorf("override: got %q, want %q", cm.SystemPromptOverride, "agent prompt")
	}
}

func TestApplyIntentPromptOverride_PreservesAgentWhitespace(t *testing.T) {
	// Agent-supplied prompts with surrounding whitespace must reach the model
	// verbatim; trim is only used for emptiness detection.
	raw := "  agent prompt with trailing newline\n"
	cm := &types.ChatManage{
		PipelineRequest: types.PipelineRequest{
			IntentPromptOverrides: map[string]string{"chitchat": raw},
		},
		PipelineState: types.PipelineState{Intent: types.IntentChitchat},
	}

	if !applyIntentPromptOverride(cm, nil) {
		t.Fatal("expected applied=true")
	}
	if cm.SystemPromptOverride != raw {
		t.Errorf("override: got %q, want %q", cm.SystemPromptOverride, raw)
	}
}

func TestApplyIntentPromptOverride_BlankAgentFallsBackToGlobal(t *testing.T) {
	cm := &types.ChatManage{
		PipelineRequest: types.PipelineRequest{
			IntentPromptOverrides: map[string]string{"chitchat": "   \n\t  "},
		},
		PipelineState: types.PipelineState{Intent: types.IntentChitchat},
	}
	global := map[string]string{"chitchat": "global prompt"}

	if !applyIntentPromptOverride(cm, global) {
		t.Fatal("expected applied=true")
	}
	if cm.SystemPromptOverride != "global prompt" {
		t.Errorf("override: got %q, want %q", cm.SystemPromptOverride, "global prompt")
	}
}

func TestApplyIntentPromptOverride_NoOverrideAndNoGlobal(t *testing.T) {
	cm := &types.ChatManage{
		PipelineState: types.PipelineState{Intent: types.IntentChitchat},
	}

	if applyIntentPromptOverride(cm, nil) {
		t.Fatal("expected applied=false")
	}
	if cm.SystemPromptOverride != "" {
		t.Errorf("override should remain empty, got %q", cm.SystemPromptOverride)
	}
}

func TestApplyIntentPromptOverride_GlobalOnly(t *testing.T) {
	cm := &types.ChatManage{
		PipelineState: types.PipelineState{Intent: types.IntentGreeting},
	}
	global := map[string]string{"greeting": "hi there"}

	if !applyIntentPromptOverride(cm, global) {
		t.Fatal("expected applied=true")
	}
	if cm.SystemPromptOverride != "hi there" {
		t.Errorf("override: got %q, want %q", cm.SystemPromptOverride, "hi there")
	}
}

// TestParseOutput_UnparsableFallsBackToOriginalQuery pins the degradation
// contract for query understanding: when the LLM returns output that cannot be
// parsed as the structured {"rewrite_query","intent",...} JSON, RewriteQuery
// must remain the original user query (which OnEvent sets before calling
// parseOutput) instead of leaking the raw model text into the downstream
// retrieval query.
func TestParseOutput_UnparsableFallsBackToOriginalQuery(t *testing.T) {
	p := &PluginQueryUnderstand{}
	cm := &types.ChatManage{
		PipelineState: types.PipelineState{
			RewriteQuery: "original user query",
			Intent:       types.IntentKBSearch,
		},
	}

	p.parseOutput(cm, "The answer is: check the admin console")

	if cm.RewriteQuery != "original user query" {
		t.Fatalf("RewriteQuery = %q, want original user query", cm.RewriteQuery)
	}
	if cm.Intent != types.IntentKBSearch {
		t.Errorf("Intent = %q, want kb_search", cm.Intent)
	}
}

// TestParseOutput_UnparsableBlankDoesNotRewrite verifies that empty LLM output
// also leaves the original query untouched.
func TestParseOutput_UnparsableBlankDoesNotRewrite(t *testing.T) {
	p := &PluginQueryUnderstand{}
	cm := &types.ChatManage{
		PipelineState: types.PipelineState{
			RewriteQuery: "original user query",
			Intent:       types.IntentKBSearch,
		},
	}

	p.parseOutput(cm, "   \n\t  ")

	if cm.RewriteQuery != "original user query" {
		t.Fatalf("RewriteQuery = %q, want original user query", cm.RewriteQuery)
	}
}

// TestParseOutput_ValidJSONStillAppliesRewrite guards the happy path: a
// well-formed structured output still overrides RewriteQuery and Intent.
func TestParseOutput_ValidJSONStillAppliesRewrite(t *testing.T) {
	p := &PluginQueryUnderstand{}
	cm := &types.ChatManage{
		PipelineState: types.PipelineState{
			RewriteQuery: "original user query",
			Intent:       types.IntentKBSearch,
		},
	}

	p.parseOutput(cm, `{"rewrite_query":"rewritten query","intent":"summarize"}`)

	if cm.RewriteQuery != "rewritten query" {
		t.Fatalf("RewriteQuery = %q, want rewritten query", cm.RewriteQuery)
	}
	if cm.Intent != types.IntentSummarize {
		t.Errorf("Intent = %q, want summarize", cm.Intent)
	}
}

// --- time_condition extraction (time-aware retrieval) ---

func TestParseTimeConditionField_ValidShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want *types.SearchTimeCondition
	}{
		{
			name: "explicit between window",
			raw:  `{"time_condition":{"field":"business_date","op":"between","start":"2026-06-18T00:00:00+08:00","end":"2026-09-18T23:59:59+08:00","strictness":"filter"}}`,
			want: &types.SearchTimeCondition{
				Field: types.SearchTimeFieldBusinessDate, Op: types.SearchTimeOpBetween,
				Start: time.Date(2026, 6, 18, 0, 0, 0, 0, time.FixedZone("", 8*3600)),
				End:   time.Date(2026, 9, 18, 23, 59, 59, 0, time.FixedZone("", 8*3600)),
				Strictness: types.SearchTimeStrictnessFilter,
			},
		},
		{
			name: "defaults collapse (field/strictness omitted)",
			raw:  `{"time_condition":{"op":"gte","start":"2025-09-18T00:00:00Z"}}`,
			want: &types.SearchTimeCondition{
				Field: types.SearchTimeFieldBusinessDate, Op: types.SearchTimeOpGte,
				Start:      time.Date(2025, 9, 18, 0, 0, 0, 0, time.UTC),
				Strictness: types.SearchTimeStrictnessFilter,
			},
		},
		{
			name: "prefer strictness preserved",
			raw:  `{"time_condition":{"op":"gte","start":"2025-09-18T00:00:00Z","strictness":"prefer"}}`,
			want: &types.SearchTimeCondition{
				Field: types.SearchTimeFieldBusinessDate, Op: types.SearchTimeOpGte,
				Start:      time.Date(2025, 9, 18, 0, 0, 0, 0, time.UTC),
				Strictness: types.SearchTimeStrictnessPrefer,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.raw), &obj); err != nil {
				t.Fatal(err)
			}
			got := parseTimeConditionField(obj)
			if got == nil {
				t.Fatalf("got nil, want %+v", tc.want)
			}
			if got.Field != tc.want.Field || got.Op != tc.want.Op || got.Strictness != tc.want.Strictness ||
				!got.Start.Equal(tc.want.Start) || !got.End.Equal(tc.want.End) {
				t.Fatalf("got %+v, want %+v", *got, *tc.want)
			}
		})
	}
}

func TestParseTimeConditionField_RejectsWrongOrAmbiguous(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"absent", `{"rewrite_query":"x"}`},
		{"explicit null", `{"time_condition":null}`},
		{"unknown op", `{"time_condition":{"op":"approx","start":"2026-01-01T00:00:00Z"}}`},
		{"missing start", `{"time_condition":{"op":"gte"}}`},
		{"between missing end", `{"time_condition":{"op":"between","start":"2026-01-01T00:00:00Z"}}`},
		{"inverted window", `{"time_condition":{"op":"between","start":"2026-06-01T00:00:00Z","end":"2026-01-01T00:00:00Z"}}`},
		{"unparseable date", `{"time_condition":{"op":"gte","start":"去年"}}`},
		{"not an object", `{"time_condition":"last 3 months"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.raw), &obj); err != nil {
				t.Fatal(err)
			}
			if got := parseTimeConditionField(obj); got != nil {
				t.Fatalf("got %+v, want nil (rather no time than a wrong time)", *got)
			}
		})
	}
}

func TestParseOutput_TimeConditionFlowsToChatManage(t *testing.T) {
	p := &PluginQueryUnderstand{}
	cm := &types.ChatManage{PipelineState: types.PipelineState{RewriteQuery: "q", Intent: types.IntentKBSearch}}

	p.parseOutput(cm, `{"rewrite_query":"近三个月XX公司评级报告","intent":"kb_search","time_condition":{"op":"between","start":"2026-06-18T00:00:00Z","end":"2026-09-18T00:00:00Z"}}`)

	if cm.TimeCondition == nil {
		t.Fatal("TimeCondition not propagated to ChatManage")
	}
	if cm.TimeCondition.Op != types.SearchTimeOpBetween || cm.TimeCondition.Strictness != types.SearchTimeStrictnessFilter {
		t.Fatalf("TimeCondition = %+v", *cm.TimeCondition)
	}
}

func TestParseOutput_NoTimeConditionLeavesNil(t *testing.T) {
	p := &PluginQueryUnderstand{}
	cm := &types.ChatManage{PipelineState: types.PipelineState{RewriteQuery: "q", Intent: types.IntentKBSearch}}

	p.parseOutput(cm, `{"rewrite_query":"什么是RAG架构","intent":"kb_search"}`)

	if cm.TimeCondition != nil {
		t.Fatalf("TimeCondition = %+v, want nil for a time-less query", *cm.TimeCondition)
	}
}
