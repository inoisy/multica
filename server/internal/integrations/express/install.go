package express

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// This file is the eXpress install backend, mirroring telegram/install.go: the
// CTS administrator registers a bot and hands over its bot_id and secret_key;
// a workspace admin pastes them into Multica. The InstallService checks the
// pair live against CTS, owns the at-rest encryption of the secret key, and
// serves the list / get / revoke management surface.

var (
	// ErrInstallationNotFound surfaces "no row matches in this workspace".
	ErrInstallationNotFound = errors.New("express installation not found")
	// ErrBotOwnedByAnotherWorkspace: the bot is connected to a live agent in a
	// different Multica workspace.
	ErrBotOwnedByAnotherWorkspace = errors.New("express: this bot is already connected to a different Multica workspace")
	// ErrBotOwnedBySameWorkspace: the bot is connected to another live agent in
	// this workspace.
	ErrBotOwnedBySameWorkspace = errors.New("express: this bot is already connected to another agent in this workspace")
	// ErrBotOwnedByArchivedAgent: the bot belongs to an archived agent here.
	ErrBotOwnedByArchivedAgent = errors.New("express: this bot is connected to an archived agent in this workspace")
)

type installQueries interface {
	WithTx(tx pgx.Tx) installQueries
	UpsertChannelInstallation(ctx context.Context, arg db.UpsertChannelInstallationParams) (db.ChannelInstallation, error)
	ReclaimDeadChannelInstallationByAppID(ctx context.Context, arg db.ReclaimDeadChannelInstallationByAppIDParams) (pgtype.UUID, error)
	GetChannelInstallationOwnerByAppID(ctx context.Context, arg db.GetChannelInstallationOwnerByAppIDParams) (db.GetChannelInstallationOwnerByAppIDRow, error)
	ListChannelInstallationsByWorkspace(ctx context.Context, arg db.ListChannelInstallationsByWorkspaceParams) ([]db.ChannelInstallation, error)
	GetChannelInstallationInWorkspace(ctx context.Context, arg db.GetChannelInstallationInWorkspaceParams) (db.ChannelInstallation, error)
	SetChannelInstallationStatus(ctx context.Context, arg db.SetChannelInstallationStatusParams) error
}

type dbInstallQueries struct{ *db.Queries }

func (q dbInstallQueries) WithTx(tx pgx.Tx) installQueries {
	return dbInstallQueries{q.Queries.WithTx(tx)}
}

// InstallService registers, lists and revokes eXpress bots.
type InstallService struct {
	box        *secretbox.Box
	q          installQueries
	tx         engine.TxStarter
	httpClient *http.Client
	logger     *slog.Logger
	// verify mints a token to prove the credentials; tests replace it.
	verify func(ctx context.Context, creds credentials) error
}

// NewInstallService binds the service to queries, a tx starter and the box
// that encrypts secret keys at rest.
func NewInstallService(q *db.Queries, tx engine.TxStarter, box *secretbox.Box, logger *slog.Logger) (*InstallService, error) {
	if q == nil {
		return nil, errors.New("express: InstallService requires queries")
	}
	return newInstallService(dbInstallQueries{q}, tx, box, logger)
}

func newInstallService(q installQueries, tx engine.TxStarter, box *secretbox.Box, logger *slog.Logger) (*InstallService, error) {
	if box == nil {
		return nil, errors.New("express: InstallService requires a non-nil secretbox.Box")
	}
	if q == nil || tx == nil {
		return nil, errors.New("express: InstallService requires queries and a tx starter")
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &InstallService{
		box: box, q: q, tx: tx, logger: logger,
		httpClient: &http.Client{Timeout: credentialVerificationTimeout},
	}
	s.verify = func(ctx context.Context, creds credentials) error {
		_, err := newBotxAPI(creds, s.httpClient, newTokenCache()).mintToken(ctx)
		return err
	}
	return s, nil
}

const credentialVerificationTimeout = 15 * time.Second

// RegisterParams are the pasted bot credentials plus who installs it for which
// agent.
type RegisterParams struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	InitiatorID pgtype.UUID
	CTSURL      string
	BotID       string
	SecretKey   string
	BotName     string
}

// Register validates the credentials live (a token mint against CTS), seals
// the secret key, and persists one bot per (workspace, agent).
func (s *InstallService) Register(ctx context.Context, p RegisterParams) (db.ChannelInstallation, error) {
	ctsURL, err := normalizeCTSURL(p.CTSURL)
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	botID, err := normalizeBotID(p.BotID)
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	secret := strings.TrimSpace(p.SecretKey)
	if secret == "" {
		return db.ChannelInstallation{}, ErrInvalidSecretKey
	}
	vctx, cancel := context.WithTimeout(ctx, credentialVerificationTimeout)
	defer cancel()
	if err := s.verify(vctx, credentials{BotID: botID, CTSURL: ctsURL, SecretKey: secret}); err != nil {
		return db.ChannelInstallation{}, err
	}

	sealed, err := s.box.Seal([]byte(secret))
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("encrypt express secret key: %w", err)
	}
	cfgJSON, err := json.Marshal(installConfig{
		AppID:              botID,
		CTSURL:             ctsURL,
		BotName:            strings.TrimSpace(p.BotName),
		SecretKeyEncrypted: base64.StdEncoding.EncodeToString(sealed),
	})
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("encode express installation config: %w", err)
	}
	return s.persistInstall(ctx, p.WorkspaceID, p.AgentID, p.InitiatorID, botID, cfgJSON)
}

const pgUniqueViolation = "23505"

// persistInstall frees the bot's routing slot from a dead prior owner, then
// upserts the (workspace, agent) row. A live owner trips the unique
// (channel_type, app_id) index and is reported precisely.
func (s *InstallService) persistInstall(ctx context.Context, wsID, agentID, installerID pgtype.UUID, appID string, cfgJSON []byte) (db.ChannelInstallation, error) {
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("begin install tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	if _, err := qtx.ReclaimDeadChannelInstallationByAppID(ctx, db.ReclaimDeadChannelInstallationByAppIDParams{
		ChannelType: string(TypeExpress),
		AppID:       appID,
		WorkspaceID: wsID,
		AgentID:     agentID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return db.ChannelInstallation{}, fmt.Errorf("reclaim dead express installation: %w", err)
	}

	inst, err := qtx.UpsertChannelInstallation(ctx, db.UpsertChannelInstallationParams{
		WorkspaceID:     wsID,
		AgentID:         agentID,
		ChannelType:     string(TypeExpress),
		Config:          cfgJSON,
		InstallerUserID: installerID,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return db.ChannelInstallation{}, s.liveOwnerConflictErr(ctx, wsID, appID)
		}
		return db.ChannelInstallation{}, fmt.Errorf("upsert express installation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("commit express install: %w", err)
	}
	return inst, nil
}

func (s *InstallService) liveOwnerConflictErr(ctx context.Context, wsID pgtype.UUID, appID string) error {
	owner, err := s.q.GetChannelInstallationOwnerByAppID(ctx, db.GetChannelInstallationOwnerByAppIDParams{
		ChannelType: string(TypeExpress),
		AppID:       appID,
	})
	if err != nil {
		return ErrBotOwnedByAnotherWorkspace
	}
	switch {
	case owner.WorkspaceID != wsID:
		return ErrBotOwnedByAnotherWorkspace
	case owner.AgentArchivedAt.Valid:
		return ErrBotOwnedByArchivedAgent
	default:
		return ErrBotOwnedBySameWorkspace
	}
}

// ListByWorkspace returns every eXpress installation in the workspace.
func (s *InstallService) ListByWorkspace(ctx context.Context, wsID pgtype.UUID) ([]db.ChannelInstallation, error) {
	return s.q.ListChannelInstallationsByWorkspace(ctx, db.ListChannelInstallationsByWorkspaceParams{
		WorkspaceID: wsID,
		ChannelType: string(TypeExpress),
	})
}

// GetInWorkspace is the workspace-scoped lookup; a forged id from another
// workspace returns ErrInstallationNotFound.
func (s *InstallService) GetInWorkspace(ctx context.Context, id, wsID pgtype.UUID) (db.ChannelInstallation, error) {
	inst, err := s.q.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID:          id,
		WorkspaceID: wsID,
		ChannelType: string(TypeExpress),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ChannelInstallation{}, ErrInstallationNotFound
		}
		return db.ChannelInstallation{}, err
	}
	return inst, nil
}

// Revoke flips status to 'revoked'; the row stays for audit.
func (s *InstallService) Revoke(ctx context.Context, id pgtype.UUID) error {
	return s.q.SetChannelInstallationStatus(ctx, db.SetChannelInstallationStatusParams{
		ID:     id,
		Status: "revoked",
	})
}
