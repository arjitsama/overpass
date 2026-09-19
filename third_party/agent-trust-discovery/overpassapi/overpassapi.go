// Package overpassapi is a small Overpass addition to the fork (third_party/
// PATCHES.md): an exported entrypoint that builds the trust-index HTTP handler
// in-process. Go forbids importing another module's internal/ packages, so
// Overpass's tests and tools cannot call internal/server.Build directly; this
// shim lives inside the fork module, where those imports are legal, and exposes
// only standard-library types across the boundary. It changes no upstream code.
package overpassapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"

	"github.com/agentnameservice/agent-trust-discovery/internal/config"
	"github.com/agentnameservice/agent-trust-discovery/internal/server"
)

// Start builds the trust index over the given SQLite path, loading the default
// profile and profiles/ directory from configDir (the fork's config/). When
// adminKey is non-empty the /v1/internal/* routes require that bearer; empty
// means admin auth is off (tests and trusted-network demos). It returns the
// HTTP handler and a close func for the store. Thresholds are the upstream
// defaults (20/50/80/90) — Overpass gates on the trust vector in its own flight
// rules, not on the index's recommendedProfile.
func Start(ctx context.Context, dbPath, configDir, adminKey string, logw io.Writer) (http.Handler, func() error, error) {
	if logw == nil {
		logw = io.Discard
	}
	cfg := config.Config{
		DBPath:          dbPath,
		AdminRequireKey: adminKey != "",
		AdminKey:        adminKey,
		LogLevel:        "error",
		Classify:        config.Classify{Untrusted: 20, Transactional: 50, Fiduciary: 80, IdentityFiduciary: 90},
	}
	logger := slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: slog.LevelError}))
	handler, db, err := server.Build(ctx, cfg,
		filepath.Join(configDir, "default-profile.yaml"), filepath.Join(configDir, "profiles"), logger)
	if err != nil {
		return nil, nil, err
	}
	return handler, db.Close, nil
}
