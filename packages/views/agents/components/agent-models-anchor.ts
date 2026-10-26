/** Where the Models section of an agent starts; the inspector and an error toast scroll to it. */
export const AGENT_MODELS_ANCHOR = "agent-models";

export function scrollToAgentModels(): void {
  const section = document.getElementById(AGENT_MODELS_ANCHOR);
  if (!section) return;
  section.scrollIntoView({ behavior: "smooth", block: "start" });
  section.focus({ preventScroll: true });
}
