package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/Everfit-io/go-service-template/internal/app"
	"github.com/Everfit-io/go-service-template/internal/platform/buildinfo"
	"github.com/Everfit-io/go-service-template/internal/platform/config"
	"github.com/Everfit-io/go-service-template/internal/platform/logging"
)

// version and commit are injected at build time via -ldflags -X main.version=…
// When ldflags are absent (e.g. `go run`), resolveBuildInfo populates them from
// runtime/debug.ReadBuildInfo (Go modules version + VCS commit).
var (
	version = ""
	commit  = ""
)

// resolveBuildInfo returns version + commit using this precedence:
//  1. -ldflags -X main.version / main.commit injected by the Makefile.
//  2. internal/buildinfo/VERSION (embedded at compile time) — primary source.
//  3. runtime/debug.ReadBuildInfo() VCS metadata (Go 1.18+ auto-stamp).
//  4. "dev" / "unknown" as final fallback.
func resolveBuildInfo() (string, string) {
	v, c := version, commit
	if v == "" {
		v = buildinfo.Version()
	}
	if v == "" {
		v = "dev"
	}

	if c == "" {
		c = commitFromBuildInfo()
	}
	if c == "" {
		c = "unknown"
	}
	return v, c
}

const shortCommitLen = 7

// commitFromBuildInfo reads the VCS revision stamped by the Go toolchain,
// truncated to a short hash and suffixed "-dirty" on a modified tree.
func commitFromBuildInfo() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var c string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			c = s.Value
			if len(c) > shortCommitLen {
				c = c[:shortCommitLen]
			}
		case "vcs.modified":
			if s.Value == "true" {
				c += "-dirty"
			}
		}
	}
	return c
}

func main() {
	os.Exit(run())
}

func run() int {
	_ = flag.String("config", "", "optional path to config file (unused; config is env-driven)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", slog.String("error", err.Error()))
		return 1
	}

	version, commit = resolveBuildInfo()
	log := logging.New(cfg, version)
	slog.SetDefault(log)

	log.Info("go-service-template starting",
		slog.String("version", version),
		slog.String("commit", commit),
		slog.String("env", cfg.Env),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, cfg, log); err != nil {
		log.Error("fatal", slog.String("error", err.Error()))
		return 1
	}
	return 0
}
