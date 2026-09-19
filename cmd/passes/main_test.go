package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/arjitsama/overpass/internal/passes"
)

const tle = "../../internal/passes/testdata/27844.tle"

func TestTableMatchesGolden(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-tle", tle, "-start", "2026-09-20T00:00:00Z"}, &out, time.Now); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("../../internal/passes/testdata/golden-passes.txt")
	if out.String() != string(want) {
		t.Fatalf("got\n%s", out.String())
	}
}

// Acceptance 5 end to end: -demo-pass starts within 5 s of now and lasts 90 s.
func TestDemoPassFlag(t *testing.T) {
	now := time.Now()
	var out bytes.Buffer
	err := run(context.Background(), []string{"-tle", tle, "-start", "2026-09-20T00:00:00Z", "-demo-pass", "-json"}, &out,
		func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	var ps []passes.Pass
	if err := json.Unmarshal(out.Bytes(), &ps); err != nil || len(ps) != 1 {
		t.Fatalf("%s %v", out.String(), err)
	}
	p := ps[0]
	if p.AOS < now.Unix()-5 || p.AOS > now.Unix()+5 || p.LOS-p.AOS != 90 || p.DemoOfAOS == 0 {
		t.Fatalf("demo pass %+v", p)
	}
}

func TestBadFlags(t *testing.T) {
	for _, args := range [][]string{{"-tle", "missing.tle"}, {"-tle", tle, "-hours", "0"}, {"-tle", tle, "-start", "yesterday"},
		{"-tle", tle, "-start", "2027-06-01T00:00:00Z"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}, time.Now); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
