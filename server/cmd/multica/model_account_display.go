package main

import (
	"encoding/json"
	"fmt"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func reasonCodeLabel(code, fallback string) string {
	switch code {
	case "":
		if fallback != "" {
			return fallback
		}
		return "—"
	case "agent_cap", protocol.AccountRefusalAgentCap:
		return "agent cap reached"
	case "provider_limit":
		return "provider quota limit"
	case "provider_refusal":
		return "provider refused account"
	case "disabled", protocol.AccountRefusalDisabled:
		return "account disabled"
	case "login_required":
		return "sign-in required"
	case "offline":
		return "runtime offline"
	case "missing":
		return "runtime missing"
	case "not_permitted":
		return "runtime not permitted"
	case "same_family":
		return "same model family excluded"
	default:
		return "unknown reason (" + code + ")"
	}
}

func profileDifference(matches *bool, rows []int32) string {
	if matches == nil {
		return "base unknown"
	}
	if *matches {
		return "matches base"
	}
	return fmt.Sprintf("differs in rows %v", rows)
}

func accountFactLabel(fact *protocol.AccountFact) string {
	if fact == nil {
		return "unknown"
	}
	return fmt.Sprintf("%s (%s), pool %s revision %d — confirmed by daemon", fact.SubscriptionKey, fact.SubscriptionID, fact.PoolID, fact.PoolRevision)
}

func taskAccountLabel(task map[string]any) string {
	raw, err := json.Marshal(task["account_fact"])
	if err != nil {
		return accountFactLabel(nil)
	}
	var fact *protocol.AccountFact
	if json.Unmarshal(raw, &fact) != nil {
		return accountFactLabel(nil)
	}
	return accountFactLabel(fact)
}

func taskWaitLabel(task map[string]any) string {
	if choice, ok := task["model_choice"].(map[string]any); ok {
		if code := str(choice["wait_reason_code"]); code != "" {
			return reasonCodeLabel(code, "")
		}
	}
	return reasonCodeLabel(str(task["wait_reason_code"]), "")
}
