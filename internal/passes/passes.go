// Package passes predicts when a satellite is above a ground station's
// elevation mask. SGP4 propagation comes from go-satellite (TEME position
// and GMST); the observer position and topocentric elevation are computed
// here on the WGS84 ellipsoid, because go-satellite places observers on a
// sphere. Floats stay inside the geometry: every output field is an integer
// (epoch seconds, whole degrees).
package passes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	satellite "github.com/joshuaferrara/go-satellite"

	"github.com/arjitsama/overpass/internal/config"
)

// Defaults from master plan section 7.
const (
	MaskDeg     = 10
	StepS       = 10
	DemoWindowS = 90
	// MinPassS drops grazing passes too short to be a usable contact.
	MinPassS = 60
	// MaxEpochAge bounds how far a prediction may be from the TLE epoch.
	MaxEpochAge = 30 * 24 * time.Hour
	// maxOverrun lets a pass that rises before the horizon finish after it.
	maxOverrun = 20 * time.Minute
)

// TLE is one two-line element set.
type TLE struct {
	Name    string
	Line1   string
	Line2   string
	NoradID int64
	Epoch   time.Time
}

// Site is a ground station location.
type Site struct {
	Name, Host           string
	LatDeg, LonDeg, AltM float64
}

// DefaultSites are the three stations of master plan 7.2 (config.DefaultSites).
var DefaultSites = FromConfig(config.DefaultSites)

// FromConfig converts configured sites.
func FromConfig(cs []config.Site) []Site {
	out := make([]Site, 0, len(cs))
	for _, c := range cs {
		out = append(out, Site{Name: c.Name, Host: c.Host, LatDeg: c.LatDeg, LonDeg: c.LonDeg, AltM: c.AltM})
	}
	return out
}

// Pass is one contact. All fields are integers.
type Pass struct {
	Station         string `json:"station"`
	Host            string `json:"host"`
	NoradID         int64  `json:"norad_id"`
	AOS             int64  `json:"aos"`
	LOS             int64  `json:"los"`
	MaxElevationDeg int64  `json:"max_elevation_deg"`
	DurationS       int64  `json:"duration_s"`
	DemoOfAOS       int64  `json:"demo_of_aos,omitempty"` // set on a --demo-pass replay
}

// Overlaps reports whether p and q share any second.
func (p Pass) Overlaps(q Pass) bool { return p.AOS < q.LOS && q.AOS < p.LOS }

// ParseTLE reads a name line (optional) and the two element lines.
func ParseTLE(r io.Reader) (TLE, error) {
	var lines []string
	sc := bufio.NewScanner(io.LimitReader(r, 4096))
	for sc.Scan() {
		if l := strings.TrimRight(sc.Text(), " \r\t"); l != "" {
			lines = append(lines, l)
		}
	}
	var t TLE
	switch len(lines) {
	case 2:
		t.Line1, t.Line2 = lines[0], lines[1]
	case 3:
		t.Name, t.Line1, t.Line2 = strings.TrimSpace(lines[0]), lines[1], lines[2]
	default:
		return t, fmt.Errorf("tle: want 2 or 3 lines, got %d", len(lines))
	}
	if len(t.Line1) != 69 || len(t.Line2) != 69 || t.Line1[0] != '1' || t.Line2[0] != '2' {
		return t, errors.New("tle: lines must be 69 characters starting with 1 and 2")
	}
	for _, l := range []string{t.Line1, t.Line2} {
		if !checksumOK(l) {
			return t, errors.New("tle: checksum mismatch")
		}
	}
	id, err := strconv.ParseInt(strings.TrimSpace(t.Line1[2:7]), 10, 64)
	if err != nil || strings.TrimSpace(t.Line2[2:7]) != strings.TrimSpace(t.Line1[2:7]) {
		return t, errors.New("tle: bad or inconsistent catalog number")
	}
	t.NoradID = id
	epoch, err := checkFields(t.Line1, t.Line2)
	if err != nil {
		return t, err
	}
	t.Epoch = epoch
	return t, nil
}

// checkFields parses every field go-satellite parses, with the same slicing,
// because go-satellite calls log.Fatal (os.Exit) on a bad number rather than
// returning an error. It also bounds the orbital elements and returns the
// epoch.
func checkFields(l1, l2 string) (time.Time, error) {
	num := func(field, s string) (float64, error) {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, fmt.Errorf("tle: bad %s %q", field, s)
		}
		return v, nil
	}
	strip := func(s string) string { return strings.Replace(s, " ", "", 2) }
	yr, err := strconv.ParseInt(l1[18:20], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("tle: bad epoch year %q", l1[18:20])
	}
	fields := []struct{ name, s string }{
		{"epoch day", l1[20:32]}, {"ndot", strip(l1[33:43])},
		{"nddot", strip(l1[44:45] + "." + l1[45:50] + "e" + l1[50:52])},
		{"bstar", strip(l1[53:54] + "." + l1[54:59] + "e" + l1[59:61])},
		{"inclination", strip(l2[8:16])}, {"node", strip(l2[17:25])}, {"eccentricity", "." + l2[26:33]},
		{"argument of perigee", strip(l2[34:42])}, {"mean anomaly", strip(l2[43:51])}, {"mean motion", strip(l2[52:63])},
	}
	vals := make([]float64, len(fields))
	for i, f := range fields {
		if vals[i], err = num(f.name, f.s); err != nil {
			return time.Time{}, err
		}
	}
	day, incl, ecc, n := vals[0], vals[4], vals[6], vals[9]
	if day < 1 || day >= 367 || incl < 0 || incl > 180 || ecc < 0 || ecc >= 1 || n <= 0 || n > 20 {
		return time.Time{}, errors.New("tle: orbital elements out of range")
	}
	year := int(yr) + 1900
	if yr < 57 {
		year = int(yr) + 2000
	}
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	return start.Add(time.Duration((day - 1) * float64(24*time.Hour))), nil
}

// checksumOK: the last digit is the sum of digits (minus signs count 1) mod 10.
func checksumOK(l string) bool {
	sum := 0
	for _, c := range l[:68] {
		switch {
		case c >= '0' && c <= '9':
			sum += int(c - '0')
		case c == '-':
			sum++
		}
	}
	return int(l[68]-'0') == sum%10
}

// LoadTLE reads a cached TLE file.
func LoadTLE(path string) (TLE, error) {
	f, err := os.Open(path)
	if err != nil {
		return TLE{}, err
	}
	defer f.Close()
	return ParseTLE(f)
}

// FetchTLE downloads the current TLE for norad from CelesTrak. Call it only
// when asked to refresh; the demo runs from the cached file.
func FetchTLE(ctx context.Context, norad int64) (TLE, error) {
	u := fmt.Sprintf("https://celestrak.org/NORAD/elements/gp.php?CATNR=%d&FORMAT=tle", norad)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return TLE{}, err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return TLE{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return TLE{}, fmt.Errorf("celestrak: %s", resp.Status)
	}
	t, err := ParseTLE(resp.Body)
	if err == nil && t.NoradID != norad {
		return TLE{}, fmt.Errorf("celestrak returned catalog %d, want %d", t.NoradID, norad)
	}
	return t, err
}

// sat wraps go-satellite, which reports failures in fields, may panic on
// malformed elements and calls log.Fatal on unparseable numbers: every TLE is
// re-checked here, however it was built, before go-satellite sees it.
func sat(t TLE) (s satellite.Satellite, err error) {
	if len(t.Line1) != 69 || len(t.Line2) != 69 {
		return s, errors.New("tle: lines must be 69 characters")
	}
	if _, err := checkFields(t.Line1, t.Line2); err != nil {
		return s, err
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sgp4: %v", r)
		}
	}()
	s = satellite.TLEToSat(t.Line1, t.Line2, satellite.GravityWGS84)
	if s.Error != 0 {
		return s, fmt.Errorf("sgp4: %s", s.ErrorStr)
	}
	return s, nil
}

// observer is a site's position on the WGS84 ellipsoid (ECEF, km) and its
// local up vector.
type observer struct {
	x, y, z       float64
	lat, lon      float64
	upX, upY, upZ float64
}

const (
	wgs84A = 6378.137
	wgs84F = 1 / 298.257223563
)

func newObserver(s Site) observer {
	lat, lon := s.LatDeg*math.Pi/180, s.LonDeg*math.Pi/180
	e2 := wgs84F * (2 - wgs84F)
	n := wgs84A / math.Sqrt(1-e2*math.Sin(lat)*math.Sin(lat))
	h := s.AltM / 1000
	return observer{
		x: (n + h) * math.Cos(lat) * math.Cos(lon), y: (n + h) * math.Cos(lat) * math.Sin(lon),
		z: (n*(1-e2) + h) * math.Sin(lat), lat: lat, lon: lon,
		upX: math.Cos(lat) * math.Cos(lon), upY: math.Cos(lat) * math.Sin(lon), upZ: math.Sin(lat),
	}
}

// elevation returns the satellite's elevation in degrees at t.
func elevation(s satellite.Satellite, o observer, t time.Time) (float64, error) {
	t = t.UTC()
	y, mo, d := t.Date()
	// Propagate takes the satellite by value and reports errors on its copy,
	// so judge the result itself: a finite position above the surface and
	// inside a plausible orbit.
	pos, _ := satellite.Propagate(s, y, int(mo), d, t.Hour(), t.Minute(), t.Second())
	r := math.Sqrt(pos.X*pos.X + pos.Y*pos.Y + pos.Z*pos.Z)
	if math.IsNaN(r) || math.IsInf(r, 0) || r < wgs84A-25 || r > 100000 {
		return 0, fmt.Errorf("sgp4: implausible position (|r| = %.0f km) at %s", r, t.Format(time.RFC3339))
	}
	gmst := satellite.GSTimeFromDate(y, int(mo), d, t.Hour(), t.Minute(), t.Second())
	ecef := satellite.ECIToECEF(pos, gmst)
	rx, ry, rz := ecef.X-o.x, ecef.Y-o.y, ecef.Z-o.z
	rng := math.Sqrt(rx*rx + ry*ry + rz*rz)
	return math.Asin((rx*o.upX+ry*o.upY+rz*o.upZ)/rng) * 180 / math.Pi, nil
}

// Predict returns the passes of t over site whose AOS is at or after start
// and before start+horizon, with the satellite above MaskDeg. A pass already
// in progress at start is left out (it cannot be booked from its AOS); a pass
// that rises before the horizon is followed to its LOS. Passes shorter than
// MinPassS are dropped. The start must be within MaxEpochAge of the epoch.
func Predict(t TLE, site Site, start time.Time, horizon time.Duration) ([]Pass, error) {
	if !t.Epoch.IsZero() {
		if d := start.Sub(t.Epoch); d > MaxEpochAge || d < -MaxEpochAge {
			return nil, fmt.Errorf("tle epoch %s is more than %s from %s; refresh the TLE",
				t.Epoch.Format(time.RFC3339), MaxEpochAge, start.UTC().Format(time.RFC3339))
		}
	}
	s, err := sat(t)
	if err != nil {
		return nil, err
	}
	o := newObserver(site)
	el := func(ts time.Time) (float64, error) { return elevation(s, o, ts) }
	var out []Pass
	start = start.UTC().Truncate(time.Second)
	end := start.Add(horizon)
	prev, err := el(start)
	if err != nil {
		return nil, err
	}
	var aos time.Time
	inPass := prev >= MaskDeg
	maxEl := prev
	for ts := start.Add(StepS * time.Second); !ts.After(end) || (inPass && !aos.IsZero() && !ts.After(end.Add(maxOverrun))); ts = ts.Add(StepS * time.Second) {
		cur, err := el(ts)
		if err != nil {
			return nil, err
		}
		switch {
		case !inPass && cur >= MaskDeg && ts.After(end):
			// a new rise after the horizon: not in this window
		case !inPass && cur >= MaskDeg:
			aos, err = crossing(el, ts.Add(-StepS*time.Second), ts)
			if err != nil {
				return nil, err
			}
			inPass, maxEl = true, cur
		case inPass && cur >= MaskDeg:
			maxEl = math.Max(maxEl, cur)
		case inPass && cur < MaskDeg:
			inPass = false
			if aos.IsZero() {
				continue // began before start: not a whole pass
			}
			los, err := crossing(el, ts.Add(-StepS*time.Second), ts)
			if err != nil {
				return nil, err
			}
			peak, err := peakElevation(el, aos, los)
			if err != nil {
				return nil, err
			}
			if los.Sub(aos) < MinPassS*time.Second {
				aos = time.Time{}
				continue
			}
			out = append(out, Pass{Station: site.Name, Host: site.Host, NoradID: t.NoradID,
				AOS: aos.Unix(), LOS: los.Unix(), MaxElevationDeg: int64(math.Round(math.Max(peak, maxEl))),
				DurationS: los.Unix() - aos.Unix()})
			aos = time.Time{}
		}
	}
	return out, nil
}

// crossing bisects [a, b] (one step apart, the mask crossed inside) to the
// first second at or above the mask for a rise, or the first below for a set.
func crossing(el func(time.Time) (float64, error), a, b time.Time) (time.Time, error) {
	ea, err := el(a)
	if err != nil {
		return time.Time{}, err
	}
	rising := ea < MaskDeg
	for b.Sub(a) > time.Second {
		mid := a.Add(b.Sub(a) / 2).Truncate(time.Second)
		em, err := el(mid)
		if err != nil {
			return time.Time{}, err
		}
		if (em >= MaskDeg) == rising {
			b = mid
		} else {
			a = mid
		}
	}
	return b, nil
}

// peakElevation samples every 5 s, then every second around the coarse peak.
func peakElevation(el func(time.Time) (float64, error), aos, los time.Time) (float64, error) {
	best, at := -90.0, aos
	for ts := aos; !ts.After(los); ts = ts.Add(5 * time.Second) {
		e, err := el(ts)
		if err != nil {
			return 0, err
		}
		if e > best {
			best, at = e, ts
		}
	}
	for ts := at.Add(-5 * time.Second); !ts.After(at.Add(5 * time.Second)); ts = ts.Add(time.Second) {
		if ts.Before(aos) || ts.After(los) {
			continue
		}
		e, err := el(ts)
		if err != nil {
			return 0, err
		}
		best = math.Max(best, e)
	}
	return best, nil
}

// Table predicts every site and returns the passes sorted by AOS, then station.
func Table(t TLE, sites []Site, start time.Time, horizon time.Duration) ([]Pass, error) {
	var all []Pass
	for _, s := range sites {
		ps, err := Predict(t, s, start, horizon)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.Name, err)
		}
		all = append(all, ps...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].AOS != all[j].AOS {
			return all[i].AOS < all[j].AOS
		}
		return all[i].Station < all[j].Station
	})
	return all, nil
}

// DemoPass replays the next real pass in a 90-second window starting at now:
// same station, satellite and peak elevation, compressed in time. Signed
// objects then carry that window's real wall-clock times, so every expiry
// check runs for real (master plan 7.11). The elevation profile itself is not
// replayed; nothing downstream needs more than the peak.
func DemoPass(p Pass, now time.Time) Pass {
	d := p
	d.DemoOfAOS = p.AOS
	d.AOS = now.Unix()
	d.LOS = d.AOS + DemoWindowS
	d.DurationS = DemoWindowS
	return d
}

// NextPass returns the first pass whose AOS is at or after now.
func NextPass(ps []Pass, now time.Time) (Pass, bool) {
	for _, p := range ps {
		if p.AOS >= now.Unix() {
			return p, true
		}
	}
	return Pass{}, false
}

// Render writes the pass table in the format cmd/passes prints and the
// golden file records.
func Render(w io.Writer, t TLE, table []Pass) error {
	fmt.Fprintf(w, "%s (NORAD %d), mask %d deg\n", t.Name, t.NoradID, MaskDeg)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATION\tAOS (UTC)\tLOS (UTC)\tMAX EL\tDURATION")
	for _, p := range table {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%dm%02ds\n", p.Station,
			time.Unix(p.AOS, 0).UTC().Format("2006-01-02 15:04:05"), time.Unix(p.LOS, 0).UTC().Format("15:04:05"),
			p.MaxElevationDeg, p.DurationS/60, p.DurationS%60)
	}
	return tw.Flush()
}
