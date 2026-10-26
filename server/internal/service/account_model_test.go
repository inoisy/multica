package service

import (
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
)

func TestAccountModelIsNeverGuessedFromFirstCandidate(t *testing.T) {
	key := func(model string) db.Subscription {
		return db.Subscription{Auth: "api_key", Settings: []byte(`{"env":{"ANTHROPIC_MODEL":"` + model + `"}}`)}
	}
	for _, tc := range []struct {
		name     string
		subs     []db.Subscription
		model    string
		deferred bool
	}{
		{"same", []db.Subscription{key("glm"), key("glm")}, "glm", false},
		{"different", []db.Subscription{key("glm"), key("minimax")}, "", true},
		{"unknown", []db.Subscription{key("glm"), key("")}, "", true},
		{"mixed", []db.Subscription{key("glm"), {Auth: "claude_oauth"}}, "", true},
		{"own model", []db.Subscription{{Auth: "codex_auth"}}, "saved", false},
		{"empty pool", nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ModelForAccountCandidates("saved", tc.subs)
			if got.Model != tc.model || got.AtRun != tc.deferred {
				t.Fatalf("got %+v", got)
			}
		})
	}
}
