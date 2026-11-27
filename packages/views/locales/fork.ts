import type { LocaleResources } from "@multica/core/i18n";

type Json = Record<string, unknown>;

function isObject(value: unknown): value is Json {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function deepMerge(base: Json, overlay: Json): Json {
  const out: Json = { ...base };
  for (const [key, value] of Object.entries(overlay)) {
    const current = out[key];
    out[key] = isObject(current) && isObject(value) ? deepMerge(current, value) : value;
  }
  return out;
}

// Fork strings live in <locale>/fork/<namespace>.json and are laid over the
// upstream namespace of the same name, so upstream JSON files stay untouched.
export function withForkStrings(
  resources: LocaleResources,
  fork: Record<string, Json>,
): LocaleResources {
  const out: LocaleResources = { ...resources };
  for (const [namespace, overlay] of Object.entries(fork)) {
    out[namespace] = deepMerge(out[namespace] ?? {}, overlay);
  }
  return out;
}
