import type { ModelOption, ModelOptionInput, ModelRowState } from "../types";

/** "codex·sol·high": runtime, model and thinking level of a row, empty parts left out. */
export function modelRowLabel(row: Pick<ModelRowState, "runtime_name" | "model" | "thinking_level">): string {
  return [row.runtime_name, row.model, row.thinking_level].filter(Boolean).join("·");
}

/** A profile's rows in order: "codex·sol·high → claude·opus → glm". */
export function compactModelRows(rows: ModelRowState[]): string {
  return [...rows].sort((a, b) => a.position - b.position).map(modelRowLabel).join(" → ");
}

/** Rows as the API takes them back, in their order; explicit args are kept. */
export function toModelOptionInputs(rows: (ModelOption | ModelRowState)[]): ModelOptionInput[] {
  return [...rows]
    .sort((a, b) => a.position - b.position)
    .map((row) => ({
      runtime_id: row.runtime_id,
      model: row.model,
      thinking_level: row.thinking_level,
      custom_args: row.custom_args,
    }));
}

/** Moves the row at index by delta (−1 up, +1 down); out of range is a no-op. */
export function moveModelOption<T>(rows: T[], index: number, delta: number): T[] {
  const target = index + delta;
  if (index < 0 || index >= rows.length || target < 0 || target >= rows.length) return rows;
  const next = [...rows];
  const [item] = next.splice(index, 1);
  next.splice(target, 0, item as T);
  return next;
}
