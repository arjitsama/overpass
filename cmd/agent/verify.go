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
	dane := daneOutcome(res)
	if res.OK() {
		fmt.Fprintf(out, "VERIFIED %s %s (%s; DANE %s)\n", host, res.ANSName, res.Verdict, dane)
		return nil
	}
	reason := res.Verdict
	for _, c := range res.Checks {
		if c.Verdict == verify.Fail {
			reason = c.Name + ": " + c.Reason
			break
		}
	}
	fmt.Fprintf(out, "FAILED %s %s (DANE %s)\n", host, reason, dane)
	return errs.New(errs.CardRejectedSignature, "verification failed for "+host+": "+reason)
}

// daneOutcome returns the DANE result by name (Verified / Skipped / NoRecords /
// Mismatch) from the tlsa check's detail, so the outcome is never a bare pass.
// TLSA-without-DNSSEC is "Skipped" (present but not relied on), which is a
// warning that still passes verification — not a rejection.
func daneOutcome(res verify.Result) string {
	for _, c := range res.Checks {
		if c.Name != verify.CheckTLSA {
			continue
		}
		switch c.Detail["outcome"] {
		case "DANEVerified":
			return "Verified"
		case "DANESkipped":
			return "Skipped"
		case "DANENoRecords":
			return "NoRecords"
		case "DANEMismatch":
			return "Mismatch"
		case "":
			return "Unknown"
		default:
			return c.Detail["outcome"]
		}
	}
	return "Unknown"
}
