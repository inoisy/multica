import { ApiError } from "../api";

export const AGENT_MODEL_OWNED_BY_PROFILE = "agent_model_owned_by_profile";

/** The server refused to move the runtime, model or thinking level of an agent whose profile owns them. */
export function isAgentModelOwnedByProfile(error: unknown): boolean {
  if (!(error instanceof ApiError) || error.status !== 409) return false;
  const body = error.body;
  return typeof body === "object" && body !== null && "code" in body && body.code === AGENT_MODEL_OWNED_BY_PROFILE;
}
