package passes

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

var fixedStart = time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)

func loadTestTLE(t *testing.T) TLE {
	t.Helper()
	tle, err := LoadTLE("testdata/27844.tle")
	if err != nil {
		t.Fatal(err)
	}
	return tle
}

// Acceptance 1: cached TLE + fixed start -> the golden table. The golden was
// cross-checked against skyfield: all 22 passes agree to within 1 s.
func TestGoldenTable(t *testing.T) {
	tle := loadTestTLE(t)
	table, err := Table(tle, DefaultSites, fixedStart, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := Render(&got, tle, table); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/golden-passes.txt")
	if got.String() != string(want) {
		t.Fatalf("table differs from golden:\n%s\nwant:\n%s", got.String(), want)
	}
}

// Acceptance 2: every pass lasts 1-15 minutes and peaks at 10-90 degrees.
func TestSanity(t *testing.T) {
	tle := loadTestTLE(t)
	table, err := Table(tle, DefaultSites, fixedStart, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, p := range table {
		if p.DurationS < 60 || p.DurationS > 15*60 || p.DurationS != p.LOS-p.AOS {
			t.Errorf("%s at %d: duration %d s", p.Station, p.AOS, p.DurationS)
		}
		if p.MaxElevationDeg < 10 || p.MaxElevationDeg > 90 {
			t.Errorf("%s at %d: max elevation %d", p.Station, p.AOS, p.MaxElevationDeg)
		}
		if p.NoradID != 27844 || p.AOS < fixedStart.Unix() {
			t.Errorf("bad pass %+v", p)
		}
		seen[p.Station] = true
	}
	if len(seen) != 3 {
		t.Fatalf("stations with passes: %v", seen)
	}
}

// Acceptance 5: the demo pass starts within 5 s of now and lasts 90 s.
func TestDemoPass(t *testing.T) {
	tle := loadTestTLE(t)
	table, _ := Table(tle, DefaultSites, fixedStart, 24*time.Hour)
	next, ok := NextPass(table, fixedStart)
	if !ok {
		t.Fatal("no pass")
	}
	now := time.Now()
	d := DemoPass(next, now)
	if d.AOS < now.Unix()-5 || d.AOS > now.Unix()+5 || d.LOS-d.AOS != 90 || d.DurationS != 90 {
		t.Fatalf("demo %+v at now=%d", d, now.Unix())
	}
	if d.Station != next.Station || d.MaxElevationDeg != next.MaxElevationDeg || d.DemoOfAOS != next.AOS {
		t.Fatalf("geometry not kept: %+v vs %+v", d, next)
	}
}

func TestParseTLERejects(t *testing.T) {
	good, _ := os.ReadFile("testdata/27844.tle")
	lines := strings.Split(strings.TrimSpace(string(good)), "\n")
	bad := map[string]string{
		"one line":     lines[1],
		"checksum":     lines[0] + "\n" + lines[1][:68] + string('0'+(lines[1][68]-'0'+1)%10) + "\n" + lines[2],
		"short":        lines[0] + "\n" + lines[1][:60] + "\n" + lines[2],
		"catalog diff": lines[0] + "\n" + lines[1] + "\n" + "2 27845" + lines[2][7:68] + "X",
	}
	for name, s := range bad {
		if _, err := ParseTLE(strings.NewReader(s)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if tle, err := ParseTLE(strings.NewReader(string(good))); err != nil || tle.NoradID != 27844 || tle.Name != "CUTE-1 (CO-55)" {
		t.Fatalf("%+v %v", tle, err)
	}
}

func TestOverlaps(t *testing.T) {
	a := Pass{AOS: 100, LOS: 200}
	for _, c := range []struct {
		b    Pass
		want bool
	}{{Pass{AOS: 150, LOS: 250}, true}, {Pass{AOS: 200, LOS: 300}, false}, {Pass{AOS: 0, LOS: 100}, false}, {Pass{AOS: 120, LOS: 130}, true}} {
		if a.Overlaps(c.b) != c.want {
			t.Errorf("%+v", c.b)
		}
	}
}

// A TLE whose numeric fields go-satellite cannot parse is rejected by
// ParseTLE, instead of go-satellite calling log.Fatal and killing the process.
func TestMalformedFieldsRejected(t *testing.T) {
	good, _ := os.ReadFile("testdata/27844.tle")
	lines := strings.Split(strings.TrimSpace(string(good)), "\n")
	fix := func(l string) string { // recompute the checksum
		sum := 0
		for _, c := range l[:68] {
			if c >= '0' && c <= '9' {
				sum += int(c - '0')
			} else if c == '-' {
				sum++
			}
		}
		return l[:68] + string(rune('0'+sum%10))
	}
	cases := map[string][2]string{
		"letter in epoch":  {fix(lines[1][:22] + "X" + lines[1][23:]), lines[2]},
		"letter in bstar":  {fix(lines[1][:55] + "Z" + lines[1][56:]), lines[2]},
		"letter in motion": {lines[1], fix(lines[2][:55] + "Q" + lines[2][56:])},
		"inclination 190":  {lines[1], fix(lines[2][:8] + "190.0000" + lines[2][16:])},
	}
	for name, l := range cases {
		if _, err := ParseTLE(strings.NewReader(l[0] + "\n" + l[1])); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	tle, _ := ParseTLE(strings.NewReader(string(good)))
	want := time.Date(2026, 9, 19, 7, 23, 53, 0, time.UTC)
	if d := tle.Epoch.Sub(want); d < -time.Second || d > time.Second {
		t.Fatalf("epoch %s", tle.Epoch)
	}
}

// A prediction far from the TLE epoch, or a decayed orbit, is an error, not
// an empty table.
func TestStaleOrDecayedTLE(t *testing.T) {
	tle := loadTestTLE(t)
	if _, err := Table(tle, DefaultSites, fixedStart.Add(60*24*time.Hour), time.Hour); err == nil {
		t.Fatal("prediction 60 days from epoch accepted")
	}
	decayed := tle
	decayed.Epoch = time.Time{} // skip the age guard to reach propagation
	// Epoch 2020, B* 0.95427 instead of 0.95427e-4: propagated six years on,
	// the orbit is nonsense (the reviewer's repro gave |r| ~ 1.4e9 km).
	decayed.Line1 = tle.Line1[:18] + "20" + tle.Line1[20:59] + "-1" + tle.Line1[61:]
	if _, err := Table(decayed, DefaultSites, fixedStart, time.Hour); err == nil {
		t.Fatal("decayed orbit gave a table")
	}
}

// A pass that rises inside the window is kept to its LOS even past the
// horizon; one already in progress at start is left out.
func TestWindowEdges(t *testing.T) {
	tle := loadTestTLE(t)
	full, _ := Table(tle, DefaultSites, fixedStart, 24*time.Hour)
	sv := full[1] // Svalbard 02:21:27-02:27:40
	cut, err := Table(tle, DefaultSites, time.Unix(sv.AOS-3600, 0), time.Hour+time.Minute)
	if err != nil || len(cut) == 0 || cut[len(cut)-1].AOS != sv.AOS || cut[len(cut)-1].LOS != sv.LOS {
		t.Fatalf("pass rising before the horizon not kept whole: %+v %v", cut, err)
	}
	mid, _ := Table(tle, DefaultSites, time.Unix(sv.AOS+60, 0), 30*time.Minute)
	for _, p := range mid {
		if p.AOS == sv.AOS {
			t.Fatal("pass in progress at start was included")
		}
	}
}
