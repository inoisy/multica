"use client";

import type { AccountFact } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { useT } from "../i18n";

/**
 * The account a run confirmed with the daemon, exactly as it was at the run.
 * A run without a confirmation (an older daemon, a run from before the
 * confirmation existed) says "unknown"; the account is never guessed from
 * today's pool.
 */
export function AccountFactLine({ fact }: { fact: AccountFact | null | undefined }) {
  const { t } = useT("agents");
  if (!fact) {
    return <span className="text-muted-foreground">{t(($) => $.models.account_unknown)}</span>;
  }
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
      <span className="font-mono">{fact.subscription_key}</span>
      <Badge variant="success">{t(($) => $.models.account_confirmed)}</Badge>
      {fact.pool_revision > 0 ? (
        <span className="text-caption text-muted-foreground">
          {t(($) => $.models.account_pool, { revision: fact.pool_revision })}
        </span>
      ) : null}
    </span>
  );
}

/** The same fact as plain text, for places that take a string (a popover row, an aria label). */
export function useAccountFactText() {
  const { t } = useT("agents");
  return (fact: AccountFact | null | undefined): string => {
    if (!fact) return t(($) => $.models.account_unknown);
    const confirmed = `${fact.subscription_key} · ${t(($) => $.models.account_confirmed)}`;
    return fact.pool_revision > 0 ? `${confirmed} · ${t(($) => $.models.account_pool, { revision: fact.pool_revision })}` : confirmed;
  };
}
