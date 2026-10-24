"use client";

import type { AvailabilityVerdict } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { useAvailabilityText } from "../model-profiles";

/** The tone of a verdict: usable, held back by the agents' limit, or blocked for a harder reason. */
export function verdictVariant(verdict: Pick<AvailabilityVerdict, "blocked" | "usable" | "reason_code">): "success" | "warning" | "destructive" | "outline" {
  if (verdict.usable && !verdict.blocked) return "success";
  if (!verdict.blocked) return "outline";
  if (verdict.reason_code === "agent_cap" || verdict.reason_code === "disabled") return "warning";
  return "destructive";
}

/**
 * "Available" or why it is not and until when, as the server decided it. One
 * badge for an account, a pool, an environment and a task: the same verdict
 * reads the same everywhere.
 */
export function VerdictBadge({ verdict }: { verdict: AvailabilityVerdict }) {
  const availability = useAvailabilityText();
  return <Badge variant={verdictVariant(verdict)}>{availability.describe(verdict)}</Badge>;
}
