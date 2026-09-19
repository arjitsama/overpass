// Command passes prints the pass table for one satellite over the ground
// stations: tonight's contacts, from a cached TLE.
//
//	passes                                   # next 24 h from now, cached TLE (run from the repo root)
//	passes -start 2026-09-20T00:00:00Z -hours 12
//	passes -refresh-tle                      # fetch the current TLE from CelesTrak first
//	passes -demo-pass                        # the next pass replayed in a 90 s window from now
//	passes -json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/passes"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, time.Now); err != nil {
		fmt.Fprintln(os.Stderr, "passes:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer, now func() time.Time) error {
	fs := flag.NewFlagSet("passes", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "agent config whose sites and satellite to use (default: the three master-plan sites)")
	tlePath := fs.String("tle", "", "cached TLE file (default: the config's satellite.tle_file, data/27844.tle)")
	startS := fs.String("start", "", "start time, RFC 3339 (default now)")
	hours := fs.Int("hours", 24, "horizon in hours")
	refresh := fs.Bool("refresh-tle", false, "fetch the current TLE from CelesTrak and overwrite -tle")
	demo := fs.Bool("demo-pass", false, "print the next pass replayed in a 90 s window starting now")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hours < 1 || *hours > 72 {
		return fmt.Errorf("-hours must be 1-72")
	}
	cfg := config.Config{Role: "ops", Port: 1}
	if *cfgPath != "" {
		c, err := config.Load(*cfgPath)
		if err != nil {
			return err
		}
		cfg = c
	}
	cfg.ApplyDefaults()
	if *tlePath == "" {
		*tlePath = cfg.Satellite.TLEFile
	}
	if *refresh {
		if err := refreshTLE(ctx, cfg.Satellite.NoradID, *tlePath); err != nil {
			return err
		}
	}
	tle, err := passes.LoadTLE(*tlePath)
	if err != nil {
		return err
	}
	if tle.NoradID != cfg.Satellite.NoradID {
		return fmt.Errorf("%s holds NORAD %d, config says %d", *tlePath, tle.NoradID, cfg.Satellite.NoradID)
	}
	start := now()
	if *startS != "" {
		if start, err = time.Parse(time.RFC3339, *startS); err != nil {
			return fmt.Errorf("-start: %w", err)
		}
	}
	table, err := passes.Table(tle, passes.FromConfig(cfg.Sites), start, time.Duration(*hours)*time.Hour)
	if err != nil {
		return err
	}
	if *demo {
		next, ok := passes.NextPass(table, start)
		if !ok {
			return fmt.Errorf("no pass in the next %d h", *hours)
		}
		table = []passes.Pass{passes.DemoPass(next, now())}
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(table)
	}
	return passes.Render(out, tle, table)
}

// refreshTLE fetches the configured satellite's TLE from CelesTrak and
// replaces the cache atomically, so an interrupted write never leaves a
// broken file. It works even when the old cache is unreadable.
func refreshTLE(ctx context.Context, norad int64, path string) error {
	t, err := passes.FetchTLE(ctx, norad)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	body := fmt.Sprintf("%s\n%s\n%s\n", t.Name, t.Line1, t.Line2)
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
