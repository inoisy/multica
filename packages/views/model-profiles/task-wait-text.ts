import type { AgentTask } from "@multica/core/types";
import { useModelText } from "./model-text";

/**
 * Why a queued run is not starting, from the choice stored on the run: the
 * reason code and the time the server gave (marked when it is an estimate).
 * Null for a run that is not waiting for a model.
 */
export function useTaskWaitText() {
  const text = useModelText();
  return (task: Pick<AgentTask, "status" | "model_choice">): string | null => {
    const choice = task.model_choice;
    if (!choice || choice.reason !== "all_unavailable") return null;
    if (task.status !== "queued" && task.status !== "deferred") return null;
    return text.reason(choice);
  };
}
