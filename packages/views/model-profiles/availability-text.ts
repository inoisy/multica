import type { AvailabilityVerdict, ReasonCode } from "@multica/core/types";
import { useLocale, useT } from "../i18n";
import { formatModelTime } from "./format-time";

/** The part of a verdict the interface words: why something cannot run and until when. */
export type VerdictLike = Pick<AvailabilityVerdict, "reason_code" | "until" | "until_estimated"> &
  Partial<Pick<AvailabilityVerdict, "blocked" | "usable">>;

/**
 * The one wording of "can it run, why not, from when" for accounts, pools,
 * environments, rows and tasks. Everything is read from the server's reason
 * code and time; the text of a refusal is never parsed.
 */
export function useAvailabilityText() {
  const { t } = useT("agents");
  const locale = useLocale();

  const reason = (code: ReasonCode | null | undefined): string => {
    switch (code) {
      case "agent_cap":
        return t(($) => $.verdict.reason.agent_cap);
      case "provider_limit":
        return t(($) => $.verdict.reason.provider_limit);
      case "provider_refusal":
        return t(($) => $.verdict.reason.provider_refusal);
      case "login_required":
        return t(($) => $.verdict.reason.login_required);
      case "disabled":
        return t(($) => $.verdict.reason.disabled);
      case "offline":
        return t(($) => $.verdict.reason.offline);
      case "missing":
        return t(($) => $.verdict.reason.missing);
      case "not_permitted":
        return t(($) => $.verdict.reason.not_permitted);
      case "same_family":
        return t(($) => $.verdict.reason.same_family);
      default:
        return t(($) => $.verdict.reason.unknown);
    }
  };

  /** "until 17:40", "about 17:40, estimate" or "end unknown"; an estimate is never worded as a reset. */
  const until = (verdict: Pick<VerdictLike, "until" | "until_estimated">): string => {
    if (!verdict.until) return t(($) => $.verdict.until_unknown);
    const time = formatModelTime(verdict.until, locale);
    return verdict.until_estimated
      ? t(($) => $.verdict.until_estimated, { time })
      : t(($) => $.verdict.until, { time });
  };

  /** One line for a blocked verdict: the reason and the time; "Available" otherwise. */
  const describe = (verdict: VerdictLike): string => {
    if (verdict.blocked === false || (verdict.blocked === undefined && !verdict.reason_code)) {
      return t(($) => $.verdict.available);
    }
    return `${reason(verdict.reason_code)} · ${until(verdict)}`;
  };

  /** "available from 17:40" for a blocked verdict, with the estimate marked and an unknown time said so. */
  const availableAt = (verdict: Pick<VerdictLike, "until" | "until_estimated">): string => {
    if (!verdict.until) return t(($) => $.verdict.available_unknown);
    const time = formatModelTime(verdict.until, locale);
    return verdict.until_estimated
      ? t(($) => $.verdict.available_at_estimated, { time })
      : t(($) => $.verdict.available_at, { time });
  };

  return { reason, until, describe, availableAt };
}
