// Command agent runs one Overpass agent. The role flag picks which one.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "agent:", err)
		os.Exit(1)
	}
}

// run parses flags, loads config and serves until ctx is done.
func run(ctx context.Context, args []string, logw io.Writer) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(logw)
	cfgPath := fs.String("config", "", "path to the agent's YAML config (required)")
	role := fs.String("role", "", "override role: "+strings.Join(config.Roles, "|"))
	if err := fs.Parse(args); err != nil {
		return errs.New(errs.BadRequest, err.Error())
	}
	if *cfgPath == "" {
		return errs.New(errs.BadRequest, "--config is required")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *role != "" {
		cfg.Role = *role
		if err := cfg.Validate(); err != nil {
			return err
		}
	}

	log := slog.New(slog.NewJSONHandler(logw, nil)).With("agent", cfg.Host, "role", cfg.Role)
	a, err := newAgent(cfg, log)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(cfg.Port)))
	if err != nil {
		return errs.New(errs.Unavailable, fmt.Sprintf("listen on port %d: %v", cfg.Port, err))
	}
	return a.serve(ctx, ln)
}
