/** Where the Models section of an agent starts; the inspector and an error toast scroll to it. */
export const AGENT_MODELS_ANCHOR = "agent-models";

// The section lives in the Advanced settings dialog, which may only be
// opening when this is called: wait a few frames for it to mount.
const MOUNT_FRAMES = 10;

export function scrollToAgentModels(frames = MOUNT_FRAMES): void {
  const section = document.getElementById(AGENT_MODELS_ANCHOR);
  if (!section) {
    if (frames > 0) requestAnimationFrame(() => scrollToAgentModels(frames - 1));
    return;
  }
  section.scrollIntoView({ behavior: "smooth", block: "start" });
  section.focus({ preventScroll: true });
}
