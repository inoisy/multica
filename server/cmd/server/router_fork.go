package main

// Routes and integrations the fork adds. router.go calls each function once,
// at the place where its routes belong, so upstream edits around them do not
// conflict with the fork's route lists.

import (
	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/express"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func setupExpress(h *handler.Handler, queries *db.Queries, pool *pgxpool.Pool, bus *events.Bus, channelRouter *engine.Router, channelRegistry *channel.Registry) {
	// eXpress (BotX) integration. BYO bot: the CTS administrator registers a
	// bot whose URL is <API>/api/express, a workspace admin pastes its CTS
	// address, bot_id and secret_key. BotX is push-only, so inbound is the
	// public webhook below (JWT-verified per bot), handed straight to the
	// engine's inbound handler; replies go out on chat:done like DingTalk.
	// Gated by MULTICA_EXPRESS_SECRET_KEY (at-rest encryption of the secret).
	if expressKey, err := secretbox.LoadKey("MULTICA_EXPRESS_SECRET_KEY"); err == nil {
		box, err := secretbox.New(expressKey)
		if err != nil {
			slog.Error("express: secretbox.New failed; express integration disabled", "error", err)
		} else {
			expressBindingSvc := express.NewBindingTokenService(queries, pool)
			h.ExpressBindingTokens = expressBindingSvc
			expressReplier := express.NewOutboundReplier(express.OutboundReplierConfig{
				Binding: expressBindingSvc,
				Decrypt: box.Open,
				AppURL:  appURLFromEnv(),
				Logger:  slog.Default(),
			})
			channelRouter.Register(express.TypeExpress, express.NewExpressResolverSet(queries, pool, expressReplier))
			express.NewOutbound(queries, box.Open, nil, slog.Default()).Register(bus)
			express.RegisterExpress(channelRegistry, express.ChannelDeps{Decrypt: box.Open, Logger: slog.Default()})
			h.ExpressWebhook = express.NewWebhook(express.WebhookConfig{
				Queries: queries,
				Decrypt: box.Open,
				Handler: channelRouter.Handle,
				Logger:  slog.Default(),
			})
			installSvc, ierr := express.NewInstallService(queries, pool, box, slog.Default())
			if ierr != nil {
				slog.Error("express: InstallService init failed; install disabled", "error", ierr)
			} else {
				h.ExpressInstall = installSvc
			}
			slog.Info("express integration enabled (BotX webhook)")
		}
	} else {
		slog.Info("express integration disabled (MULTICA_EXPRESS_SECRET_KEY not set)")
	}
}

func forkPublicRoutes(r chi.Router, h *handler.Handler) {
	// eXpress (BotX) bot endpoints. No Multica auth: CTS signs every request
	// with a JWT made from the bot's secret key, verified in the handler.
	// The CTS administrator registers <API>/api/express as the bot URL.
	r.Post("/api/express/command", h.ExpressCommand)
	r.Get("/api/express/status", h.ExpressStatus)
	r.Post("/api/express/notification/callback", h.ExpressNotificationCallback)
}

func forkDaemonWorkspaceRoutes(r chi.Router, h *handler.Handler) {
	// Subscriptions the workspace's runtimes spend: the daemon probes them
	// and reports usage and the state of each credential file.
	r.Get("/workspaces/{workspaceId}/subscriptions", h.DaemonListSubscriptions)
	r.Post("/workspaces/{workspaceId}/subscription-usage", h.DaemonReportSubscriptionUsage)
	r.Post("/runtimes/{runtimeId}/subscription-logins/{loginId}/result", h.DaemonReportSubscriptionLogin)
}

func forkDaemonTaskRoutes(r chi.Router, h *handler.Handler) {
	r.Post("/tasks/{taskId}/account", h.ConfirmTaskAccount)
}

func forkUserRoutes(r chi.Router, h *handler.Handler) {
	// Instance-level subscriptions. Readable by members of any workspace;
	// every write is gated inside the handler on MULTICA_INSTANCE_ADMINS.
	r.Route("/api/subscriptions", func(r chi.Router) {
		r.Get("/", h.ListSubscriptions)
		r.Post("/", h.CreateSubscription)
		r.Get("/discrepancies", h.ListSubscriptionDiscrepancies)
		r.Get("/{key}", h.GetSubscription)
		r.Patch("/{key}", h.UpdateSubscription)
		r.Delete("/{key}", h.DeleteSubscription)
		r.Get("/{key}/usage", h.ListSubscriptionUsage)
		r.Post("/{key}/usage", h.RecordSubscriptionUsage)
		r.Post("/{key}/login", h.StartSubscriptionLogin)
		r.Get("/{key}/login", h.GetSubscriptionLogin)
		r.Delete("/{key}/login", h.CancelSubscriptionLogin)
		// Forced re-check of the provider limit; owner or admin of a
		// workspace spending the account, like the recheck of refusals.
		r.Post("/{key}/probe", h.StartSubscriptionProbe)
		r.Get("/{key}/probe/{probeId}", h.GetSubscriptionProbe)
		// Journal of the commands an instance admin ran by hand.
		r.Get("/{key}/manual-runs", h.ListSubscriptionManualRuns)
		r.Post("/{key}/manual-runs", h.OpenSubscriptionManualRun)
		r.Post("/{key}/manual-runs/{runId}/finish", h.FinishSubscriptionManualRun)
	})
	r.Post("/api/runtime-pools", h.CreateRuntimePool)
	// Pools of accounts: readable by members of any workspace for the
	// workspaces they are in; every write is gated inside the handler on
	// MULTICA_INSTANCE_ADMINS.
	r.Route("/api/pools", func(r chi.Router) {
		r.Get("/", h.ListPools)
		r.Post("/", h.CreatePool)
		r.Get("/{id}", h.GetPool)
		r.Patch("/{id}", h.UpdatePool)
		r.Delete("/{id}", h.DeletePool)
		r.Put("/{id}/grants", h.SetPoolGrants)
	})
}

func forkWorkspaceMemberRoutes(r chi.Router, h *handler.Handler) {
	// Subscriptions a runtime spends. Reading is member-visible
	// (subs-run reads it with the run's task token); changing it
	// is gated inside the handler on instance admins, not on the
	// workspace role.
	r.Get("/runtime-profiles/{profileId}/subscriptions", h.GetRuntimeProfileSubscriptions)
	r.Put("/runtime-profiles/{profileId}/subscriptions", h.SetRuntimeProfileSubscriptions)
}

func forkWorkspaceIntegrationRoutes(r chi.Router, h *handler.Handler, queries *db.Queries) {
	// eXpress integration. Same admin/member split as Telegram.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURL(queries, "id"))
		r.Get("/express/installations", h.ListExpressInstallations)
	})
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner", "admin"))
		r.Delete("/express/installations/{installationId}", h.RevokeExpressInstallation)
		r.Post("/express/install", h.RegisterExpressBot)
	})
}

func forkBindingRoutes(r chi.Router, h *handler.Handler) {
	// eXpress binding-token redemption. Same rationale as Telegram.
	r.Post("/api/express/binding/redeem", h.RedeemExpressBindingToken)
}

func forkIssueRoutes(r chi.Router, h *handler.Handler) {
	r.Get("/workflow-artifact", h.GetIssueWorkflowArtifact)
	r.Get("/model-profile", h.GetIssueModelProfile)
	r.Put("/model-profile", h.SetIssueModelProfile)
}

func forkAgentRoutes(r chi.Router, h *handler.Handler) {
	// Model list of the agent: profile, own list, disallowed
	// tools, and what the server chooses for its next run.
	r.Get("/models", h.GetAgentModels)
	r.Put("/models", h.UpdateAgentModels)
	// Personal profile of the agent: made from a shared one,
	// edited, or one model only; DELETE returns the agent to the
	// shared profile it was made from.
	r.Post("/models/personal", h.CreateAgentPersonalProfile)
	r.Put("/models/personal", h.SetAgentPersonalProfile)
	r.Delete("/models/personal", h.ResetAgentPersonalProfile)
	r.Put("/models/single", h.SetAgentSingleModel)
}

func forkWorkspaceRoutes(r chi.Router, h *handler.Handler) {
	// Model profiles of the workspace: agents and issues reference
	// them; an edit reaches every referencing run from its next claim.
	r.Route("/api/model-profiles", func(r chi.Router) {
		r.Get("/", h.ListModelProfiles)
		r.Post("/", h.CreateModelProfile)
		r.Post("/import", h.ImportModelProfiles)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", h.GetModelProfile)
			r.Put("/", h.UpdateModelProfile)
			r.Delete("/", h.DeleteModelProfile)
			r.Post("/preview", h.PreviewModelProfile)
		})
	})
	r.Get("/api/runtime-unavailable", h.ListRuntimeUnavailable)
	r.Post("/api/runtime-unavailable/recheck", h.RecheckRuntimeUnavailable)
}

func forkRuntimeRoutes(r chi.Router, h *handler.Handler) {
	r.Get("/model-usage", h.GetRuntimeModelUsage)
	r.Get("/run-facts", h.GetRuntimeRunFacts)
}
