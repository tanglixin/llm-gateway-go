package token

import "testing"

func TestFromGenerationInfo(t *testing.T) {
	stats := FromGenerationInfo(map[string]any{
		"PromptTokens":     11,
		"CompletionTokens": 22,
	})
	if stats.Prompt != 11 || stats.Completion != 22 || stats.Total() != 33 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestFromGenerationInfoAliases(t *testing.T) {
	stats := FromGenerationInfo(map[string]any{
		"prompt_tokens":     float64(5),
		"completion_tokens": int64(7),
	})
	if stats.Prompt != 5 || stats.Completion != 7 {
		t.Fatalf("alias mapping failed: %+v", stats)
	}
}

func TestFromGenerationInfoNil(t *testing.T) {
	if got := FromGenerationInfo(nil); got.Total() != 0 {
		t.Fatalf("nil info should be zero, got %+v", got)
	}
}
