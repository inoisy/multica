// @vitest-environment node
import { expect, it } from "vitest";
import { compactModelRows, modelRowLabel, moveModelOption, rowDifferences, toModelOptionInputs } from "./labels";

const row = (position: number, runtime_name: string, model: string, thinking_level = "") => ({
  position, runtime_id: `rt-${position}`, runtime_name, provider: "", model, thinking_level,
  custom_args: null, state: "available", until: null, reason_code: null, until_estimated: false, chosen: false,
  effective_model: model, model_at_run: false,
});

it("labels rows compactly in position order", () => {
  expect(modelRowLabel(row(1, "codex", "sol", "high"))).toBe("codex·sol·high");
  expect(compactModelRows([row(2, "claude", "opus"), row(1, "codex", "sol", "high"), row(3, "glm", "")])).toBe(
    "codex·sol·high → claude·opus → glm",
  );
});

it("turns rows back into inputs keeping explicit args", () => {
  const rows = [row(2, "claude", "opus"), { ...row(1, "codex", "sol"), custom_args: ["--x"] }];
  expect(toModelOptionInputs(rows)).toEqual([
    { runtime_id: "rt-1", model: "sol", thinking_level: "", custom_args: ["--x"] },
    { runtime_id: "rt-2", model: "opus", thinking_level: "", custom_args: null },
  ]);
});

it("moves rows within bounds only", () => {
  expect(moveModelOption(["a", "b", "c"], 2, -1)).toEqual(["a", "c", "b"]);
  expect(moveModelOption(["a", "b"], 0, -1)).toEqual(["a", "b"]);
  expect(moveModelOption(["a", "b"], 1, 1)).toEqual(["a", "b"]);
});

it("pairs personal rows with the shared rows and trusts the server's differing places", () => {
  const own = [row(1, "codex", "sol", "xhigh"), row(2, "claude", "opus")];
  const base = [row(1, "codex", "sol", "high"), row(2, "claude", "opus"), row(3, "glm", "")];
  const pairs = rowDifferences(own, base, [1, 3]);
  expect(pairs.map((p) => [p.position, p.row?.thinking_level ?? null, p.baseRow?.thinking_level ?? null, p.differs])).toEqual([
    [1, "xhigh", "high", true],
    [2, "", "", false],
    [3, null, "", true],
  ]);
  expect(rowDifferences(own, [], [1, 2]).every((p) => p.baseRow === null && p.differs)).toBe(true);
});
