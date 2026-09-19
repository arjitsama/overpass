// Command trustseed imports Overpass's agents into the trust index and seeds a
// baseline pass_delivery observation for the honest stations, marked as seed
// data (master plan §11). It NEVER seeds identity or integrity: those come from
// real measurement (the prober/hydrator in production). The lookalike is
// imported but not seeded, so it stays downlink-probation with no audited
// history — exactly the cold-start contrast the demo shows.
//
// Preferred over seeding is earned history: run a few real passes through the
// auditor so pass_delivery is genuine. Seeding is the documented fallback for a
// stack without a live pass loop; pass -warmup 0 to import agents only.
//
//	trustseed -config deploy/local/trustseed.yaml
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/arjitsama/overpass/internal/trust"
)

type seedConfig struct {
	URL         string `yaml:"url"`
	AdminKeyEnv string `yaml:"admin_key_env"`
	Agents      []struct {
		AgentID     string `yaml:"agent_id"`
		DNSName     string `yaml:"dns_name"`
		DisplayName string `yaml:"display_name"`
		FirstSeen   string `yaml:"first_seen"` // RFC3339; older agents earn a higher agentage integrity signal
	} `yaml:"agents"`
	SeedStations []string `yaml:"seed_stations"` // agentIds to give a baseline pass_delivery
	SeedPasses   int      `yaml:"seed_passes"`   // booked==delivered for the baseline; default 3
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "trustseed:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "", "seed config YAML (required)")
	flag.Parse()
	if *cfgPath == "" {
		return fmt.Errorf("-config is required")
	}
	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		return err
	}
	var cfg seedConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.URL == "" {
		return fmt.Errorf("url is required")
	}
	key := ""
	if cfg.AdminKeyEnv != "" {
		key = os.Getenv(cfg.AdminKeyEnv)
	}
	c := &trust.Client{BaseURL: cfg.URL, AdminKey: key}
	ctx := context.Background()

	agents := make([]trust.Agent, 0, len(cfg.Agents))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, a := range cfg.Agents {
		first := a.FirstSeen
		if first == "" {
			first = now
		}
		agents = append(agents, trust.Agent{
			AgentID: a.AgentID, DNSName: a.DNSName, DisplayName: a.DisplayName,
			Status: "ACTIVE", FirstSeen: first, LastUpdated: now,
		})
	}
	if err := c.ImportAgents(ctx, agents); err != nil {
		return fmt.Errorf("import agents: %w", err)
	}
	fmt.Printf("imported %d agents\n", len(agents))

	passes := cfg.SeedPasses
	if passes <= 0 {
		passes = 3
	}
	value, _ := json.Marshal(map[string]int{"booked": passes, "delivered": passes, "auditFailures": 0})
	for _, id := range cfg.SeedStations {
		if err := c.SeedObservation(ctx, id, "pass_delivery", value); err != nil {
			return fmt.Errorf("seed %s: %w", id, err)
		}
		fmt.Printf("seeded pass_delivery for %s (%d/%d, marked seed)\n", id, passes, passes)
	}
	return nil
}
