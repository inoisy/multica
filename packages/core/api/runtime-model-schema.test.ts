import { describe, expect, it } from "vitest";
import { ManualRunListSchema, ModelRowStateSchema, RuntimeModelUsageSchema, RuntimeRunFactsSchema } from "./schemas";

describe("runtime model schemas", () => {
  it("reads the facts of an environment and keeps an unknown state a string", () => {
    const parsed = RuntimeRunFactsSchema.parse({
      runtime_id: "rt-1", selection_state: "archived", machine_status: "online",
      run_facts_24h: { since: "a", until: "b", runs: 3, unknown_account_runs: 1, accounts: { k: 2 } },
    });
    expect(parsed.selection_state).toBe("archived");
    expect(parsed.run_facts_24h.accounts).toEqual({ k: 2 });
  });

  it("turns missing lists and counters into empty ones instead of failing the page", () => {
    const parsed = RuntimeModelUsageSchema.parse({ selected_in: null, deletable: true });
    expect(parsed.selected_in).toEqual([]);
    expect(parsed.serving_agents).toEqual([]);
    expect(parsed.hidden_agents).toBe(0);
    expect(parsed.deletable).toBe(true);
  });

  it("reads the journal of manual runs with the open one marked and no cursor on the last page", () => {
    const parsed = ManualRunListSchema.parse({
      key: "acc", runs: [{ id: "r", started_at: "2026-10-25T10:00:00Z", finished_at: null, exit_code: null, unfinished: true }],
    });
    expect(parsed.runs[0]).toMatchObject({ unfinished: true, finished_at: null, exit_code: null, command: "" });
    expect(parsed.next_before).toBeNull();
  });

  it("keeps the model of a row empty and the launch flag false from an older server", () => {
    const row = ModelRowStateSchema.parse({ runtime_id: "rt-1", model: "opus" });
    expect(row.effective_model).toBe("");
    expect(row.model_at_run).toBe(false);
  });
});
