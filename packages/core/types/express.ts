/** An eXpress (BotX) bot installation bound to a single Multica agent.
 *
 * Wire shape mirrors `ExpressInstallationResponse` in
 * `server/internal/handler/express.go`. New fields stay optional so older
 * desktop builds keep parsing the response. */
export interface ExpressInstallation {
  id: string;
  workspace_id: string;
  agent_id: string;
  /** The bot_id (UUID) the CTS administrator issued. */
  bot_id: string;
  /** The CTS origin the bot is registered on, e.g. https://cts.example.com. */
  cts_url: string;
  /** Optional display name the admin gave the bot. */
  bot_name: string;
  installer_user_id: string;
  status: "active" | "revoked" | string;
  installed_at: string;
  created_at: string;
  updated_at: string;
}

export interface ListExpressInstallationsResponse {
  installations: ExpressInstallation[];
  /** Whether the deployment has MULTICA_EXPRESS_SECRET_KEY configured. */
  configured: boolean;
  install_supported?: boolean;
}

/** Request body for a bot install: the registration the CTS admin hands over.
 * The backend validates it live (token mint) before persisting. */
export interface RegisterExpressRequest {
  cts_url: string;
  bot_id: string;
  secret_key: string;
  bot_name?: string;
}

export interface RedeemExpressBindingTokenResponse {
  workspace_id: string;
  installation_id: string;
  express_user_id: string;
}
