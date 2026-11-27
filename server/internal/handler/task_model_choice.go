package handler

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// hydrateTaskModelChoices attaches the recorded model choice and account to
// run responses in one query. It is display metadata: a lookup failure leaves
// the fields empty instead of failing the read.
func (h *Handler) hydrateTaskModelChoices(ctx context.Context, resp []AgentTaskResponse) {
	if len(resp) == 0 {
		return
	}
	ids := make([]pgtype.UUID, 0, len(resp))
	for _, r := range resp {
		if id, err := parseUUIDString(r.ID); err == nil {
			ids = append(ids, id)
		}
	}
	rows, err := h.Queries.ListTaskModelChoices(ctx, ids)
	if err != nil || len(rows) == 0 {
		return
	}
	byTask := make(map[string]int, len(rows))
	for i, row := range rows {
		byTask[uuidToString(row.TaskID)] = i
	}
	for i := range resp {
		j, ok := byTask[resp[i].ID]
		if !ok {
			continue
		}
		row := rows[j]
		if row.ModelChoice != nil {
			resp[i].ModelChoice = json.RawMessage(row.ModelChoice)
		}
		if fact, ok := service.ParseAccountFact(row.AccountFact); ok {
			resp[i].AccountFact = &fact
		}
	}
}

// hydrateTaskModelChoice is hydrateTaskModelChoices for a single run.
func (h *Handler) hydrateTaskModelChoice(ctx context.Context, resp *AgentTaskResponse) {
	one := []AgentTaskResponse{*resp}
	h.hydrateTaskModelChoices(ctx, one)
	*resp = one[0]
}

// taskIsModelRouted reports whether the run's model list chose its runtime;
// a lookup error counts as a plain run.
func taskIsModelRouted(ctx context.Context, q *db.Queries, taskID pgtype.UUID) bool {
	c, err := service.TaskModelChoice(ctx, q, taskID)
	return err == nil && c.ModelChoice != nil
}
