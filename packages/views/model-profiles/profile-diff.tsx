"use client";

import type { ModelRowState } from "@multica/core/types";
import { modelRowLabel, rowDifferences } from "@multica/core/model-profiles";
import { Badge } from "@multica/ui/components/ui/badge";
import { useT } from "../i18n";
import { useModelText } from "./model-text";

/**
 * How a personal profile stands to the shared profile it was made from:
 * "matches shared", the rows where it differs, or nothing to compare with.
 * `matchesBase` and `diffRows` are the server's verdict.
 */
export function ProfileDiffBadge({ matchesBase, diffRows }: { matchesBase: boolean | null; diffRows: number[] }) {
  const { t } = useT("agents");
  if (matchesBase === null) {
    return <Badge variant="outline">{t(($) => $.models.base_unknown)}</Badge>;
  }
  return matchesBase ? (
    <Badge variant="success">{t(($) => $.models.matches_base)}</Badge>
  ) : (
    <Badge variant="warning">{t(($) => $.models.differs_rows, { rows: diffRows.join(", ") })}</Badge>
  );
}

/**
 * The rows of a personal profile with, under every differing one, what the
 * shared profile has at that place; rows only the shared profile has follow.
 */
export function PersonalRowsList({
  rows,
  baseRows,
  diffRows,
  showPosition = true,
}: {
  rows: ModelRowState[];
  baseRows: ModelRowState[];
  diffRows: number[];
  showPosition?: boolean;
}) {
  const { t } = useT("agents");
  const text = useModelText();
  const pairs = rowDifferences(rows, baseRows, diffRows);
  return (
    <ol className="w-full space-y-2" data-testid="personal-rows">
      {pairs.map(({ position, row, baseRow, differs }) =>
        row ? (
          <li key={position} className="space-y-1">
            <div className="flex flex-wrap items-center gap-2 text-body">
              {showPosition ? (
                <span className="w-16 shrink-0 text-caption text-muted-foreground">
                  {position === 1 ? t(($) => $.models.row_primary) : t(($) => $.models.row_n, { n: position })}
                </span>
              ) : null}
              <span className="min-w-[10rem] flex-1 break-words">{modelRowLabel(row)}</span>
              {row.state ? <Badge variant={row.state === "available" ? "secondary" : "outline"}>{text.state(row)}</Badge> : null}
              {row.chosen ? <Badge>{t(($) => $.models.current)}</Badge> : null}
            </div>
            {differs ? (
              <div className="flex flex-wrap items-center gap-2 pl-0 text-caption text-muted-foreground sm:pl-[4.5rem]">
                <Badge variant="warning">{t(($) => $.models.row_differs)}</Badge>
                <span>
                  {baseRow
                    ? t(($) => $.models.row_in_shared, { row: modelRowLabel(baseRow) })
                    : t(($) => $.models.row_not_in_shared)}
                </span>
              </div>
            ) : null}
          </li>
        ) : baseRow ? (
          <li key={position} className="flex flex-wrap items-center gap-2 text-caption text-muted-foreground">
            <Badge variant="warning">{t(($) => $.models.row_differs)}</Badge>
            <span>{t(($) => $.models.row_only_shared, { row: `${position}. ${modelRowLabel(baseRow)}` })}</span>
          </li>
        ) : null,
      )}
    </ol>
  );
}
