package main

import (
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

func uuidString(id pgtype.UUID) string { return util.UUIDToString(id) }
