import { api } from "@multica/core/api";

/**
 * Клиент пульта конвейера (multica-control, /control/). Пульт живёт в том же
 * origin и принимает ту же сессию, что и API Multica: bearer-токен или
 * cookie + CSRF. Обслуживает несколько workspace — свой передаётся `?ws=`.
 */
const CONTROL_API = "/control/api";

export class ControlError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

function controlHeaders(): Record<string, string> {
  const token = api.getToken?.();
  if (token) return { Authorization: `Bearer ${token}` };
  const cookies = new Map(
    document.cookie
      .split(";")
      .map((part) => {
        const index = part.indexOf("=");
        return [part.slice(0, index).trim(), part.slice(index + 1)] as const;
      })
      .filter(([key]) => key),
  );
  const csrf = cookies.get("multica_csrf_session") ?? cookies.get("multica_csrf");
  return csrf ? { "X-CSRF-Token": decodeURIComponent(csrf) } : {};
}

export async function controlRequest<T>(
  path: string,
  body?: unknown,
  workspaceSlug?: string | null,
): Promise<T> {
  const headers = controlHeaders();
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    headers["X-Control-Request"] = "1";
  }
  const query = workspaceSlug ? `?ws=${encodeURIComponent(workspaceSlug)}` : "";
  const response = await fetch(`${CONTROL_API}/${path}${query}`, {
    method: body === undefined ? "GET" : "POST",
    credentials: "same-origin",
    cache: "no-store",
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = (await response.json().catch(() => ({}))) as { error?: string };
  if (!response.ok) {
    throw new ControlError(response.status, data.error ?? response.statusText);
  }
  return data as T;
}
