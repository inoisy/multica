import type { ModelChoice, ModelRowState } from "@multica/core/types";
import { modelRowLabel } from "@multica/core/model-profiles";
import { useT } from "../i18n";

export function formatModelTime(value: string | null | undefined): string {
  if (!value) return "";
  const date = new Date(value);
  const sameDay = date.toDateString() === new Date().toDateString();
  return date.toLocaleString(undefined, sameDay
    ? { hour: "2-digit", minute: "2-digit" }
    : { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" });
}

/** Human text of a model choice: where its list came from, why this row, a row's state. */
export function useModelText() {
  const { t } = useT("agents");
  const source = (choice: ModelChoice, issueIdentifier?: string) => {
    switch (choice.source) {
      case "issue_profile":
        return t(($) => $.models.source_issue, { name: choice.profile_name, issue: issueIdentifier ?? "" });
      case "agent_list":
        return t(($) => $.models.source_own);
      case "agent_profile":
        return t(($) => $.models.source_profile, { name: choice.profile_name });
      default:
        return t(($) => $.models.source_fields);
    }
  };
  const reason = (choice: ModelChoice) => {
    switch (choice.reason) {
      case "primary":
        return t(($) => $.models.reason_primary);
      case "fallback_row": {
        const blocked = choice.rows.find((row) => row.position < choice.position && row.state !== "available");
        if (blocked?.until) {
          return t(($) => $.models.reason_fallback_row_until, {
            n: blocked.position,
            time: formatModelTime(blocked.until),
            m: choice.position,
          });
        }
        return t(($) => $.models.reason_fallback_row, { n: blocked?.position ?? 1, m: choice.position });
      }
      case "fallback_profile":
        return t(($) => $.models.reason_fallback_profile, {
          exhausted: choice.exhausted_profiles.join(", "),
          name: choice.profile_name,
        });
      case "all_unavailable":
        return choice.wait_until
          ? t(($) => $.models.reason_waiting_until, { time: formatModelTime(choice.wait_until) })
          : t(($) => $.models.reason_waiting);
      default:
        return t(($) => $.models.reason_no_models);
    }
  };
  const state = (row: ModelRowState) => {
    switch (row.state) {
      case "available":
        return t(($) => $.models.state_available);
      case "limited":
        return t(($) => $.models.state_limited, { time: formatModelTime(row.until) });
      case "offline":
        return t(($) => $.models.state_offline);
      case "missing":
        return t(($) => $.models.state_missing);
      case "not_permitted":
        return t(($) => $.models.state_not_permitted);
      default:
        return row.state;
    }
  };
  const chosen = (choice: ModelChoice) => {
    const row = choice.rows.find((r) => r.chosen);
    return row ? modelRowLabel(row) : "";
  };
  return { source, reason, state, chosen };
}
