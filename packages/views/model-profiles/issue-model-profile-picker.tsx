"use client";

import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  issueModelProfileOptions,
  modelProfileListOptions,
  useSetIssueModelProfile,
} from "@multica/core/model-profiles";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { PropRow } from "../common/prop-row";
import { useT } from "../i18n";

const ASSIGNEE = "assignee";

/**
 * Issue sidebar row "Model profile": runs of this issue choose their model from it
 * instead of their agent's list. Empty means the assignee's own list. Renders
 * nothing against a backend without model profiles.
 */
export function IssueModelProfilePicker({
  wsId,
  issueId,
}: {
  wsId: string;
  issueId: string;
}) {
  const { t } = useT("issues");
  const current = useQuery(issueModelProfileOptions(wsId, issueId));
  const profiles = useQuery(modelProfileListOptions(wsId));
  const set = useSetIssueModelProfile(wsId, issueId);
  if (!current.data) return null;
  const items = [
    { value: ASSIGNEE, label: t(($) => $.model_profile.assignee) },
    ...(profiles.data ?? []).map((p) => ({ value: p.id, label: p.name })),
  ];
  const value = current.data.model_profile_id ?? ASSIGNEE;
  return (
    <PropRow label={t(($) => $.model_profile.label)}>
      <Select
        items={items}
        value={value}
        disabled={set.isPending}
        onValueChange={(next) => {
          if (!next || next === value) return;
          set.mutate(next === ASSIGNEE ? null : next, {
            onError: (error) =>
              toast.error(
                error instanceof Error ? error.message : String(error),
              ),
          });
        }}
      >
        <SelectTrigger
          size="sm"
          className="h-7 w-full border-0 px-2 text-caption shadow-none"
          aria-label={t(($) => $.model_profile.label)}
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {items.map((item) => (
            <SelectItem key={item.value} value={item.value}>
              {item.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </PropRow>
  );
}
