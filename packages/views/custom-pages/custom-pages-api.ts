import { queryOptions } from "@tanstack/react-query";
import { api } from "@multica/core/api";

/**
 * Свои страницы workspace — пункты группы «Страницы» в сайдбаре. Список хранит
 * отдельный сервис multica-pages (/pages-api/, тот же origin и та же сессия):
 * видят все участники, меняют владелец и админы.
 */
export interface CustomPage {
  id: string;
  title: string;
  /** Путь на этом сайте (`/takes/`) или `https://…`. */
  url: string;
  /** embed — во фрейме внутри Multica, tab — в новой вкладке. */
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
  const data = (await response.json().catch(() => ({}))) as CustomPagesState & { error?: string };
  if (!response.ok) throw new PagesError(response.status, data.error ?? response.statusText);
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
