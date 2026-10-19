// @vitest-environment node
import { expect, it } from "vitest";
import { compactModelRows, modelRowLabel, moveModelOption, toModelOptionInputs } from "./labels";

const row = (position: number, runtime_name: string, model: string, thinking_level = "") => ({
  position, runtime_id: `rt-${position}`, runtime_name, provider: "", model, thinking_level,
  custom_args: null, state: "available", until: null, chosen: false,
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
