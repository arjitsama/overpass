package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/verify"
)

// verifyPeer runs one read-only VerifyPeer against host using the config's
// verifier (environments + trust roots) and prints a single line. It writes
// nothing and serves nothing; scripts/smoke.sh uses it to check every production
// host. Exit is non-zero when verification does not pass.
func verifyPeer(ctx context.Context, cfg config.Config, host string, out io.Writer) error {
	v, err := verify.New(cfg, verify.Options{Self: cfg.Host, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		return err
	}
	res := v.VerifyPeer(ctx, host)
	if res.OK() {
		fmt.Fprintf(out, "VERIFIED %s %s (%s)\n", host, res.ANSName, res.Verdict)
		return nil
	}
	reason := res.Verdict
	for _, c := range res.Checks {
		if c.Verdict == verify.Fail {
			reason = c.Name + ": " + c.Reason
			break
		}
	}
	fmt.Fprintf(out, "FAILED %s %s\n", host, reason)
	return errs.New(errs.CardRejectedSignature, "verification failed for "+host+": "+reason)
}
