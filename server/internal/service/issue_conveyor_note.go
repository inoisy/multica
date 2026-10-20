package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const conveyorResultExample = `{
  "schema_version": 1,
  "outcome": "pass",
  "artifact": {"repo": "<repo>", "branch": "<branch>", "commit": "<full commit sha>"},
  "checks": [{"name": "tests", "command": "<command you ran>", "ok": true}],
  "report": "<what was done, how it was checked, what the next step needs to know>"
}`

// conveyorHandoffNote is the instruction a route run starts with. It is the
// only place that tells an executor how to hand a step over, so the route and
// its prompt cannot drift apart.
func conveyorHandoffNote(issue db.Issue, cv db.IssueConveyor, action db.IssueConveyorEvent, route []ConveyorNode) string {
	step := ""
	if node := conveyorNodeAt(route, action.Idx.Int32); node != nil {
		step = node.Step
	}
	id := util.UUIDToString(issue.ID)
	var d struct {
		Reason     string `json:"why"`
		Report     string `json:"report"`
		Detail     string `json:"reason"`
		ReturnedBy string `json:"returned_by"`
	}
	_ = json.Unmarshal(action.Details, &d)

	var b strings.Builder
	if action.Action.String == conveyorActionNotifyCoordinator {
		fmt.Fprintf(&b, "Задача ведётся по маршруту, и он ждёт координатора (шаг «%s», раунд %d).\n", step, action.Round.Int32)
		reason := action.Reason.String
		if reason == "" {
			reason = d.Reason
		}
		fmt.Fprintf(&b, "Причина: %s.\n", reason)
		if cv.AcceptedCommit.Valid {
			fmt.Fprintf(&b, "Принятый commit: %s.\n", cv.AcceptedCommit.String)
		}
		if d.Report != "" {
			fmt.Fprintf(&b, "\nПоследний отчёт исполнителя:\n%s\n", d.Report)
		}
		if cv.Status == ConveyorCompleted {
			b.WriteString("\nВсе шаги пройдены. Примите работу по обычным правилам; статус задачи конвейер не меняет.\n")
		} else {
			fmt.Fprintf(&b, "\nРешите по существу. Вернуть работу в маршрут новым раундом: `multica issue conveyor resume %s --step <шаг> --revision %d`; остановить маршрут: `multica issue conveyor disable %s --revision %d`.\n", id, cv.Revision, id, cv.Revision)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "Задача ведётся по маршруту: ваш шаг «%s», раунд %d. Сделайте его и сдайте результат командой:\n\n", step, action.Round.Int32)
	if d.ReturnedBy != "" {
		fmt.Fprintf(&b, "Работу вернул шаг «%s» — исправьте то, что он нашёл, остальное не трогайте:\n\n%s\n\n", d.ReturnedBy, d.Report)
	}
	fmt.Fprintf(&b, "    multica issue conveyor result %s --file result.json\n\nФормат result.json:\n\n%s\n\n", id, conveyorResultExample)
	if step == conveyorStepDev {
		b.WriteString("artifact.repo — репозиторий, как он записан в настройках workspace (URL; для локального репозитория — его путь или путь вашей рабочей копии).\n")
	}
	b.WriteString("outcome: `pass` — шаг выполнен; `changes_requested` — нужны правки предыдущего шага; `blocked` — не можете продолжить; `needs_coordinator` — нужно решение координатора.\n")
	if node := conveyorNodeAt(route, action.Idx.Int32); node != nil && node.Back != "" {
		fmt.Fprintf(&b, "`changes_requested` вернёт работу на шаг «%s» с вашим отчётом (всего кругов не больше %d, дальше — координатор): в report перечислите, что именно исправить.\n", node.Back, node.backRounds())
	}
	if step == conveyorStepQA || step == conveyorStepReview {
		fmt.Fprintf(&b, "Проверяйте именно принятый commit")
		if cv.AcceptedCommit.Valid {
			fmt.Fprintf(&b, " %s", cv.AcceptedCommit.String)
		}
		b.WriteString(" и укажите его в artifact.commit.\n")
	}
	b.WriteString("Не упоминайте следующего исполнителя и координатора, не ставьте будильники на свой ран: следующий шаг запустит сервер после того, как этот ран завершится.\n")
	return b.String()
}
