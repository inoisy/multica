import type { ModelChoice, ModelRowState } from "@multica/core/types";
import { compactModelRows, modelRowLabel } from "@multica/core/model-profiles";
import { useT } from "../i18n";
import { useAvailabilityText } from "./availability-text";
import { formatModelTime } from "./format-time";

export { formatModelTime };

/** Human text of a model choice: where its list came from, why this row, a row's state. */
export function useModelText() {
  const { t } = useT("agents");
  const availability = useAvailabilityText();
  const source = (choice: ModelChoice, issueIdentifier?: string) => {
    switch (choice.source) {
      case "issue_profile":
        return t(($) => $.models.source_issue, { name: choice.profile_name, issue: issueIdentifier ?? "" });
      case "agent_list":
        return t(($) => $.models.source_personal, { name: choice.profile_name });
      case "agent_profile":
        return choice.profile_personal
          ? t(($) => $.models.source_personal, { name: choice.profile_name })
          : t(($) => $.models.source_profile, { name: choice.profile_name });
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
        if (blocked?.reason_code) {
          return t(($) => $.models.reason_fallback_row_why, {
            n: blocked.position,
            why: availability.describe(blocked),
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
        return choice.wait_reason_code || choice.wait_until
          ? t(($) => $.models.reason_waiting_why, {
              why: availability.describe({
                blocked: true,
                reason_code: choice.wait_reason_code,
                until: choice.wait_until,
                until_estimated: choice.wait_until_estimated,
              }),
            })
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
        return availability.describe({ blocked: true, ...row });
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
  const atRun = t(($) => $.models.model_at_run);
  const rowLabel = (row: Parameters<typeof modelRowLabel>[0]) => modelRowLabel(row, atRun);
  const compact = (rows: ModelRowState[]) => compactModelRows(rows, atRun);
  const chosen = (choice: ModelChoice) => {
    const row = choice.rows.find((r) => r.chosen);
    return row ? rowLabel(row) : "";
  };
  return { source, reason, state, chosen, rowLabel, compact, atRun };
}
