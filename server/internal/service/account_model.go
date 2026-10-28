package service

import (
	"encoding/json"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AccountModel is a display value; it never replaces the saved row or a run's
// snapshot. AtRun means candidates disagree or the account model is unknown.
type AccountModel struct {
	Model string `json:"model"`
	AtRun bool   `json:"at_run"`
}

func SubscriptionModel(sub db.Subscription) string {
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal(sub.Settings, &settings) != nil {
		return ""
	}
	return strings.TrimSpace(settings.Env["ANTHROPIC_MODEL"])
}

func modelForAccounts(saved string, accounts []Account) AccountModel {
	if len(accounts) == 0 {
		return AccountModel{AtRun: true}
	}
	var model string
	for i, a := range accounts {
		value := saved
		if a.Auth == "api_key" {
			value = a.Model
		}
		if value == "" || i > 0 && value != model {
			return AccountModel{AtRun: true}
		}
		model = value
	}
	return AccountModel{Model: model}
}
