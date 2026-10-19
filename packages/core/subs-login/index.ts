import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { z } from "zod";
import { api } from "../api";
import { parseWithFallback } from "../api/schema";

/**
 * Client for the subscription login helper (subs-login, /subs-login/). It
 * lives on the same origin and checks the same Multica session; a server
 * without it answers 404 and the login block stays hidden.
 */
const SUBS_LOGIN_API = "/subs-login/api";

export const LOGIN_STATES = ["logged_in", "revoked", "never", "check_failed", "unchecked"] as const;
export type LoginState = (typeof LOGIN_STATES)[number];
export type LoginAuth = "codex_auth" | "claude_oauth" | "api_key";

export interface LoginSession {
  session: string | null;
  state: "waiting" | "done" | "failed" | "cancelled" | "expired" | "none";
  url: string | null;
  code: string | null;
  message: string;
}

export interface LoginAccount {
  id: string;
  provider: string;
  auth: LoginAuth;
  state: LoginState;
  login: LoginSession | null;
}

export interface LoginAccounts {
  accounts: LoginAccount[];
  can_login: boolean;
}

const LoginSessionSchema = z.object({
  session: z.string().nullable().optional().catch(null).transform((v) => v ?? null),
  state: z.enum(["waiting", "done", "failed", "cancelled", "expired", "none"]).catch("failed"),
  url: z.string().nullable().optional().catch(null).transform((v) => v ?? null),
  code: z.string().nullable().optional().catch(null).transform((v) => v ?? null),
  message: z.string().optional().catch("").transform((v) => v ?? ""),
});

const LoginAccountsSchema = z.object({
  accounts: z
    .array(
      z.object({
        id: z.string(),
        provider: z.string().catch(""),
        auth: z.enum(["codex_auth", "claude_oauth", "api_key"]),
        state: z.enum(LOGIN_STATES).catch("unchecked"),
        login: LoginSessionSchema.nullable().optional().catch(null).transform((v) => v ?? null),
      }),
    )
    .catch([]),
  can_login: z.boolean().catch(false),
});

const FAILED_SESSION: LoginSession = { session: null, state: "failed", url: null, code: null, message: "" };

export class SubsLoginError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

async function subsLoginRequest(path: string, workspaceSlug: string, body?: unknown): Promise<unknown> {
  const headers: Record<string, string> = {};
  const token = api.getToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    headers["X-Subs-Login-Request"] = "1";
  }
  const sep = path.includes("?") ? "&" : "?";
  const response = await fetch(`${SUBS_LOGIN_API}/${path}${sep}ws=${encodeURIComponent(workspaceSlug)}`, {
    method: body === undefined ? "GET" : "POST",
    credentials: "same-origin",
    cache: "no-store",
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data: unknown = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = data && typeof data === "object" && "error" in data ? String(data.error) : response.statusText;
    throw new SubsLoginError(response.status, error);
  }
  return data;
}

/** Login status of the workspace's subscription accounts; null when the helper is not installed. */
export async function getLoginAccounts(workspaceSlug: string): Promise<LoginAccounts | null> {
  try {
    const data = await subsLoginRequest("accounts", workspaceSlug);
    return parseWithFallback(data, LoginAccountsSchema, { accounts: [], can_login: false }, {
      endpoint: "GET /subs-login/api/accounts",
    });
  } catch (error) {
    if (error instanceof SubsLoginError && (error.status === 404 || error.status === 502)) return null;
    throw error;
  }
}

export async function getLoginSession(workspaceSlug: string, account: string): Promise<LoginSession> {
  const data = await subsLoginRequest(`login?account=${encodeURIComponent(account)}`, workspaceSlug);
  return parseWithFallback(data, LoginSessionSchema, FAILED_SESSION, { endpoint: "GET /subs-login/api/login" });
}

export type LoginAction = { action: "start" } | { action: "cancel" } | { action: "secret"; value: string };

export async function postLogin(workspaceSlug: string, account: string, action: LoginAction): Promise<LoginSession> {
  const data = await subsLoginRequest("login", workspaceSlug, { account, ...action });
  return parseWithFallback(data, LoginSessionSchema, FAILED_SESSION, { endpoint: "POST /subs-login/api/login" });
}

export const subsLoginKeys = {
  accounts: (wsSlug: string) => ["subs-login", "accounts", wsSlug] as const,
};

export function loginAccountsOptions(wsSlug: string) {
  return queryOptions({
    queryKey: subsLoginKeys.accounts(wsSlug),
    queryFn: () => getLoginAccounts(wsSlug),
    enabled: wsSlug.length > 0,
    refetchInterval: 60_000,
    retry: false,
  });
}

export function useSubscriptionLogin(wsSlug: string, account: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (action: LoginAction) => postLogin(wsSlug, account, action),
    onSettled: () => qc.invalidateQueries({ queryKey: subsLoginKeys.accounts(wsSlug) }),
  });
}
