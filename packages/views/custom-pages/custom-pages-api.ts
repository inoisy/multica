import { queryOptions } from "@tanstack/react-query";
import { api, CustomPagesStateSchema, parseWithFallback } from "@multica/core/api";

/**
 * Workspace pages are served by multica-pages on the same origin and session.
 * Members read them; workspace owners and admins may edit them.
 */
export interface CustomPage {
  id: string;
  title: string;
  /** Same-origin path or an HTTPS URL. */
  url: string;
  /** Embed inside Multica or open a separate tab. */
  mode: "embed" | "tab";
  description: string;
}

export interface CustomPagesState {
  pages: CustomPage[];
  can_edit: boolean;
}

export class PagesError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

async function pagesRequest(wsSlug: string, body?: unknown): Promise<CustomPagesState> {
  const headers: Record<string, string> = {};
  const token = api.getToken?.();
  if (token) headers.Authorization = `Bearer ${token}`;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    headers["X-Pages-Request"] = "1";
  }
  const response = await fetch(`/pages-api/api/pages?ws=${encodeURIComponent(wsSlug)}`, {
    method: body === undefined ? "GET" : "POST",
    credentials: "same-origin",
    cache: "no-store",
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const raw: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    const message = raw && typeof raw === "object" && "error" in raw && typeof raw.error === "string"
      ? raw.error : response.statusText;
    throw new PagesError(response.status, message);
  }
  const data = parseWithFallback<CustomPagesState | null>(raw, CustomPagesStateSchema, null, {
    endpoint: "/pages-api/api/pages",
  });
  if (!data) throw new PagesError(502, "Could not read workspace pages");
  return data;
}

export const customPagesKey = (wsSlug: string | null) => ["custom-pages", wsSlug] as const;

export function customPagesOptions(wsSlug: string | null) {
  return queryOptions({
    queryKey: customPagesKey(wsSlug),
    queryFn: () => pagesRequest(wsSlug!),
    enabled: !!wsSlug,
    staleTime: 60_000,
    retry: false,
  });
}

export function saveCustomPages(wsSlug: string | null, pages: Partial<CustomPage>[]) {
  return pagesRequest(wsSlug!, { pages });
}
