import { describe, expect, it } from "vitest";
import { ApiError } from "../api";
import { isAgentModelOwnedByProfile } from "./errors";

describe("isAgentModelOwnedByProfile", () => {
  it("recognises the profile refusal by its code", () => {
    const error = new ApiError("owned", 409, "Conflict", { error: "owned", code: "agent_model_owned_by_profile" });
    expect(isAgentModelOwnedByProfile(error)).toBe(true);
  });

  it("does not take another 409 or another error for it", () => {
    expect(isAgentModelOwnedByProfile(new ApiError("moved", 409, "Conflict", { code: "pool_revision_conflict" }))).toBe(false);
    expect(isAgentModelOwnedByProfile(new ApiError("x", 500, "Server", { code: "agent_model_owned_by_profile" }))).toBe(false);
    expect(isAgentModelOwnedByProfile(new Error("x"))).toBe(false);
    expect(isAgentModelOwnedByProfile(new ApiError("x", 409, "Conflict"))).toBe(false);
  });
});
