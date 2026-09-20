package verify

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agentnameservice/ans-sdk-go/models"
	ansverify "github.com/agentnameservice/ans-sdk-go/verify"
	"github.com/agentnameservice/ans-sdk-go/verify/scitt"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/wellknown"
)

// Check names, in the order VerifyPeer runs them.
const (
	CheckRegistered    = "registered"
	CheckBadge         = "badge"
	CheckReceipt       = "scitt_receipt"
	CheckStatusToken   = "status_token"
	CheckCertChain     = "cert_chain"
	CheckTLSA          = "tlsa"
	CheckCardHash      = "card_hash"
	CheckCardSignature = "card_signature"
)

// Verdicts.
const (
	Pass = "pass"
	Warn = "warn"
	Fail = "fail"
	Skip = "skip"
)

const maxCardBytes = 64 << 10

// Check is one verdict with its reason.
type Check struct {
	Name    string            `json:"name"`
	Verdict string            `json:"verdict"`
	Reason  string            `json:"reason,omitempty"`
	Detail  map[string]string `json:"detail,omitempty"`
}

// Result is everything VerifyPeer decided about one peer.
type Result struct {
	Host     string    `json:"host"`
	URL      string    `json:"url"`
	Env      string    `json:"env"`
	AgentID  string    `json:"agent_id,omitempty"`
	ANSName  string    `json:"ans_name,omitempty"`
	TokenIat time.Time `json:"token_iat,omitempty"`
	Checks   []Check   `json:"checks"`
	Verdict  string    `json:"verdict"`
}

// OK reports whether the peer may be talked to (warnings allowed).
func (r Result) OK() bool { return r.Verdict == Pass || r.Verdict == Warn }

// Failed returns the failed checks' reasons.
func (r Result) Failed() []string {
	var out []string
	for _, c := range r.Checks {
		if c.Verdict == Fail {
			out = append(out, c.Name+": "+c.Reason)
		}
	}
	return out
}

// Verifier checks peers across environments.
type Verifier struct {
	self   string
	envs   map[string]*env
	peers  []config.Peer
	keeper map[string]*Keeper // per environment
	emit   func(bus.Event)
	log    *slog.Logger
	dialer func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Options configure a Verifier.
type Options struct {
	Self    string          // this agent's host, for events
	Emit    func(bus.Event) // receives one event per check; may be nil
	Log     *slog.Logger
	Policy  Policy // zero MaxAge means DefaultMaxAge
	Dialer  func(ctx context.Context, network, addr string) (net.Conn, error)
	Timeout time.Duration
}

// New builds a Verifier from config: one env per environment, the peer list.
func New(cfg config.Config, o Options) (*Verifier, error) {
	if o.Policy.MaxAge == 0 {
		o.Policy.MaxAge = DefaultMaxAge
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Emit == nil {
		o.Emit = func(bus.Event) {}
	}
	if o.Dialer == nil {
		o.Dialer = (&net.Dialer{Timeout: httpTimeout}).DialContext
	}
	v := &Verifier{self: o.Self, envs: map[string]*env{}, peers: cfg.Peers, keeper: map[string]*Keeper{},
		emit: o.Emit, log: o.Log, dialer: o.Dialer}
	for name, ec := range cfg.Environments {
		e, err := newEnv(name, ec)
		if err != nil {
			return nil, err
		}
		v.envs[name] = e
		v.keeper[name] = NewKeeper(o.Policy, e.freshToken)
	}
	return v, nil
}

// peerFor finds the configured peer for host (a hostname or host:port, or a
// peer name when byName), or an unconfigured peer at https://host in envName.
func (v *Verifier) peerFor(host, envName string, byName bool) config.Peer {
	for _, p := range v.peers {
		u, err := url.Parse(p.URL)
		if err == nil && (strings.EqualFold(u.Host, host) || strings.EqualFold(u.Hostname(), host) || (byName && p.Name == host)) {
			return p
		}
	}
	return config.Peer{Name: host, URL: "https://" + host, Env: envName}
}

// VerifyPeer runs every check against host (a configured peer's host or
// name, else a production agent) and fails closed: any failed check fails
// the peer, and a check that cannot run for want of an earlier one is
// skipped with the reason. Every check emits a bus event. It never panics.
func (v *Verifier) VerifyPeer(ctx context.Context, host string) Result {
	return v.verify(ctx, v.peerFor(host, config.ProdEnv, true))
}

func (v *Verifier) verify(ctx context.Context, p config.Peer) (res Result) {
	res = Result{Host: p.Name, URL: p.URL, Env: p.EnvName()}
	run := &peerRun{v: v, peer: p, res: &res}
	defer func() {
		if r := recover(); r != nil {
			run.add(Check{Name: "internal", Verdict: Fail, Reason: "verifier panic"})
		}
		if run.tr != nil {
			run.tr.CloseIdleConnections()
		}
		res.Verdict = overall(res.Checks)
		v.emit(bus.Event{Agent: v.self, Kind: "verify_peer", Subject: res.Host, Result: res.Verdict,
			Reason: strings.Join(res.Failed(), "; "), Data: map[string]any{"env": res.Env, "agent_id": res.AgentID}})
	}()
	u, err := url.Parse(p.URL)
	if err != nil || u.Scheme != "https" || !validHost(u.Hostname()) || u.User != nil {
		run.add(Check{Name: CheckRegistered, Verdict: Fail, Reason: fmt.Sprintf("invalid peer URL %q", p.URL)})
		return res
	}
	run.url, res.Host = u, strings.ToLower(u.Hostname())
	e, ok := v.envs[res.Env]
	if !ok {
		run.add(Check{Name: CheckRegistered, Verdict: Fail, Reason: "unknown environment " + res.Env})
		return res
	}
	run.env = e
	run.all(ctx)
	return res
}

// validHost accepts DNS hostnames: letters, digits, hyphens, dots.
func validHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

func overall(cs []Check) string {
	if len(cs) == 0 {
		return Fail
	}
	out := Pass
	for _, c := range cs {
		switch c.Verdict {
		case Fail, Skip:
			return Fail
		case Warn:
			out = Warn
		}
	}
	return out
}

// peerRun carries state between the checks for one peer.
type peerRun struct {
	v     *Verifier
	env   *env
	peer  config.Peer
	url   *url.URL
	res   *Result
	fqdn  models.Fqdn
	badge *models.Badge
	token *Token
	leaf  *x509.Certificate
	card  []byte
	pin   string          // hex SHA-256 of the server leaf, once attested
	tr    *http.Transport // one pinned transport per run, closed at the end
	// receiptOK is set only when the log entry was fetched and its SCITT
	// receipt verified against the log's root keys; the card-hash Warn path
	// depends on it so an unavailable log can never soften a Fail.
	receiptOK bool
}

func (r *peerRun) add(c Check) {
	r.res.Checks = append(r.res.Checks, c)
	r.v.emit(bus.Event{Agent: r.v.self, Kind: "verify_check", Subject: r.res.Host, Result: c.Verdict, Reason: c.Reason,
		Data: map[string]any{"check": c.Name, "env": r.res.Env}})
}

func (r *peerRun) skip(name, why string) {
	r.add(Check{Name: name, Verdict: Skip, Reason: "not run: " + why})
}

func (r *peerRun) all(ctx context.Context) {
	if !r.registered(ctx) {
		for _, n := range []string{CheckBadge, CheckReceipt, CheckStatusToken, CheckCertChain, CheckTLSA, CheckCardHash, CheckCardSignature} {
			r.skip(n, "peer is not registered")
		}
		return
	}
	r.badgeStatus()
	r.receipt(ctx)
	haveToken := r.statusToken(ctx)
	if !haveToken {
		for _, n := range []string{CheckCertChain, CheckTLSA, CheckCardHash, CheckCardSignature} {
			r.skip(n, "no status token to attest certificates")
		}
		return
	}
	if !r.certChain(ctx) {
		for _, n := range []string{CheckTLSA, CheckCardHash, CheckCardSignature} {
			r.skip(n, "server certificate not attested")
		}
		return
	}
	r.tlsa(ctx)
	if !r.fetchCard(ctx) {
		return
	}
	// The signature check runs first: a card with no registered hash is
	// tolerated only when its signature binds it to a log-attested key.
	sig := r.cardSignature(ctx)
	r.cardHash(sig.Verdict == Pass)
	r.add(sig)
}

func (r *peerRun) registered(ctx context.Context) bool {
	fqdn, err := models.NewFqdn(r.res.Host)
	if err != nil {
		r.add(Check{Name: CheckRegistered, Verdict: Fail, Reason: "invalid host: " + err.Error()})
		return false
	}
	r.fqdn = fqdn
	rec, err := r.env.dns.FindPreferredBadge(ctx, fqdn)
	if errors.Is(err, ansverify.ErrRecordNotFound) || (err == nil && rec == nil) {
		r.add(Check{Name: CheckRegistered, Verdict: Fail, Reason: "no _ans-badge TXT record: not an ANS agent"})
		return false
	}
	if err != nil {
		r.add(Check{Name: CheckRegistered, Verdict: Fail, Reason: "badge DNS lookup failed: " + err.Error()})
		return false
	}
	if !r.env.onLog(rec.URL) {
		r.add(Check{Name: CheckRegistered, Verdict: Fail, Reason: "badge URL " + rec.URL + " is not on the " + r.res.Env + " transparency log"})
		return false
	}
	badge, err := ansverify.NewHTTPTransparencyLogClient().WithHTTPClient(r.env.http).FetchBadge(ctx, rec.URL)
	if err != nil {
		r.add(Check{Name: CheckRegistered, Verdict: Fail, Reason: "badge fetch failed: " + err.Error()})
		return false
	}
	if badge.AgentID() == "" || !strings.EqualFold(badge.AgentHost(), r.res.Host) {
		r.add(Check{Name: CheckRegistered, Verdict: Fail, Reason: fmt.Sprintf("badge is for host %q, not %q", badge.AgentHost(), r.res.Host)})
		return false
	}
	r.badge = badge
	r.res.AgentID, r.res.ANSName = badge.AgentID(), badge.AgentName()
	r.add(Check{Name: CheckRegistered, Verdict: Pass, Detail: map[string]string{
		"agent_id": badge.AgentID(), "ans_name": badge.AgentName(), "badge_url": rec.URL}})
	return true
}

func (r *peerRun) badgeStatus() {
	s := r.badge.Status
	c := Check{Name: CheckBadge, Detail: map[string]string{"status": string(s), "schema": r.badge.SchemaVersion}}
	switch {
	case s == models.BadgeStatusActive:
		c.Verdict = Pass
	case s.IsValidForConnection():
		c.Verdict, c.Reason = Warn, "badge status is "+string(s)
	default:
		c.Verdict, c.Reason = Fail, "badge status is "+string(s)
	}
	r.add(c)
}

func (r *peerRun) receipt(ctx context.Context) {
	raw, err := r.env.scitt.FetchReceipt(ctx, r.res.AgentID)
	if err != nil {
		r.add(Check{Name: CheckReceipt, Verdict: Fail, Reason: "receipt fetch failed: " + err.Error()})
		return
	}
	vr, err := scitt.VerifyReceipt(raw, r.env.keys)
	if err != nil {
		r.add(Check{Name: CheckReceipt, Verdict: Fail, Reason: "receipt does not verify against the log's root keys: " + err.Error()})
		return
	}
	r.receiptOK = true
	r.add(Check{Name: CheckReceipt, Verdict: Pass, Detail: map[string]string{
		"tree_size": fmt.Sprint(vr.TreeSize), "leaf_index": fmt.Sprint(vr.LeafIndex)}})
}

func (r *peerRun) statusToken(ctx context.Context) bool {
	d := r.v.keeper[r.res.Env].Check(ctx, r.res.AgentID)
	c := Check{Name: CheckStatusToken}
	if d.Token != nil {
		c.Detail = map[string]string{"status": string(d.Token.Status), "iat": d.Token.Iat.UTC().Format(time.RFC3339)}
		r.res.TokenIat = d.Token.Iat
	}
	switch {
	case !d.Allow:
		c.Verdict, c.Reason = Fail, d.Reason
	case !hostMatchesANSName(d.Token.ANSName, r.res.Host):
		c.Verdict, c.Reason = Fail, fmt.Sprintf("status token ANS name %q is not for host %q", d.Token.ANSName, r.res.Host)
	case d.Warning != "":
		c.Verdict, c.Reason = Warn, d.Warning
	default:
		c.Verdict = Pass
	}
	r.add(c)
	// A token that verified but failed policy (say, WARNING or REVOKED)
	// still attests certificates, so the later checks run and the Result is
	// complete; the peer has already failed.
	if d.Token == nil || !hostMatchesANSName(d.Token.ANSName, r.res.Host) {
		return false
	}
	r.token = d.Token
	return true
}

// hostMatchesANSName checks ans://v<semver>.<host>.
func hostMatchesANSName(ansName, host string) bool {
	n, err := ansverify.ParseAnsName(ansName)
	return err == nil && strings.EqualFold(n.Host, host)
}

// certChain connects to the peer and requires its server leaf to be one the
// status token attests and to cover the host. The TLS chain itself is not
// trusted: the transparency log's attestation is the pin.
func (r *peerRun) certChain(ctx context.Context) bool {
	leaf, err := r.dialLeaf(ctx)
	if err != nil {
		r.add(Check{Name: CheckCertChain, Verdict: Fail, Reason: "TLS connect failed: " + err.Error()})
		return false
	}
	sum := sha256.Sum256(leaf.Raw)
	fp := hex.EncodeToString(sum[:])
	var attested []string
	for _, e := range r.token.Payload.ValidServerCerts {
		attested = append(attested, hex.EncodeToString(e.Fingerprint[:]))
	}
	c := Check{Name: CheckCertChain, Detail: map[string]string{"server_sha256": fp, "subject": leaf.Subject.CommonName,
		"attested_server_sha256": strings.Join(attested, ",")}}
	switch {
	case !scitt.MatchesServerCert(&r.token.Payload, sum):
		c.Verdict, c.Reason = Fail, "server certificate is not attested in the status token"
	case leaf.VerifyHostname(r.res.Host) != nil:
		c.Verdict, c.Reason = Fail, "server certificate does not cover "+r.res.Host
	case time.Now().After(leaf.NotAfter):
		c.Verdict, c.Reason = Fail, "server certificate expired"
	default:
		c.Verdict = Pass
	}
	r.add(c)
	if c.Verdict != Pass {
		return false
	}
	r.leaf, r.pin = leaf, fp
	return true
}

func (r *peerRun) dialAddr() string {
	if r.peer.Dial != "" {
		return r.peer.Dial
	}
	port := r.url.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(r.res.Host, port)
}

func (r *peerRun) dialLeaf(ctx context.Context) (*x509.Certificate, error) {
	raw, err := r.v.dialer(ctx, "tcp", r.dialAddr())
	if err != nil {
		return nil, err
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{ServerName: r.res.Host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // pinned to the log-attested fingerprint below
	_ = conn.SetDeadline(time.Now().Add(httpTimeout))
	if err := conn.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("no server certificate")
	}
	return certs[0], nil
}

// tlsa checks DANE with the SDK. ANS publishes TLSA at _443._tcp.
func (r *peerRun) tlsa(ctx context.Context) {
	out := ansverify.NewDANEVerifier(r.env.dane).Verify(ctx, r.fqdn, 443, ansverify.CertIdentityFromX509(r.leaf))
	c := Check{Name: CheckTLSA, Detail: map[string]string{"outcome": out.Type.String()}}
	switch out.Type {
	case ansverify.DANEVerified:
		c.Verdict = Pass
	case ansverify.DANENoRecords:
		c.Verdict, c.Reason = Warn, "no TLSA record published"
	case ansverify.DANESkipped:
		c.Verdict, c.Reason = Warn, "TLSA records present but not DNSSEC-validated, so not relied on"
	default:
		c.Verdict, c.Reason = Fail, "TLSA "+out.Type.String()
		if out.Error != nil {
			c.Reason += ": " + out.Error.Error()
		}
	}
	r.add(c)
}

// client is an HTTPS client pinned to the attested server leaf. One
// transport serves the whole run and is closed when VerifyPeer returns.
func (r *peerRun) client() *http.Client {
	if r.tr == nil {
		r.tr = r.pinnedTransport()
	}
	return &http.Client{Timeout: httpTimeout, Transport: r.tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (r *peerRun) pinnedTransport() *http.Transport {
	pin := r.pin
	return &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return r.v.dialer(ctx, network, r.dialAddr())
		},
		TLSClientConfig: &tls.Config{ServerName: r.res.Host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, //nolint:gosec // pinned below
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return errors.New("no server certificate")
				}
				sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
				if hex.EncodeToString(sum[:]) != pin {
					return errors.New("server certificate changed since it was attested")
				}
				return nil
			}},
		ForceAttemptHTTP2: true,
		IdleConnTimeout:   30 * time.Second,
	}
}

// get fetches a URL on the peer's own origin only.
func (r *peerRun) get(ctx context.Context, target string) ([]byte, error) {
	t, err := url.Parse(target)
	if err != nil || !strings.EqualFold(t.Host, r.url.Host) || t.Scheme != "https" {
		return nil, fmt.Errorf("refusing to fetch %q: not on %s", target, r.url.Host)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", target, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCardBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxCardBytes {
		return nil, fmt.Errorf("GET %s: body over %d bytes", target, maxCardBytes)
	}
	return body, nil
}

func (r *peerRun) fetchCard(ctx context.Context) bool {
	card, err := r.get(ctx, strings.TrimSuffix(r.peer.URL, "/")+wellknown.PathCard)
	if err != nil {
		r.add(Check{Name: CheckCardHash, Verdict: Fail, Reason: "agent card fetch failed: " + err.Error()})
		r.skip(CheckCardSignature, "no agent card")
		return false
	}
	r.card = card
	return true
}

// ReasonCardHashNotRegistered is the card_hash Warn shown by name in
// --verify output and on the dashboard.
const ReasonCardHashNotRegistered = "card hash: not registered; card bound by signature to log-attested key"

// cardHash compares the served card with the metaDataHash the log attests.
// ans-cli v0.1.18 registers no metaDataHash, so "none registered" is a Warn
// only when (1) the log entry was fetched and its SCITT receipt verified and
// (2) card_signature passed (jku pinned to this host, kid in the trust card,
// x5c leaf matching a log-attested identity cert). Otherwise it is a Fail: an
// unavailable log or an unsigned card never downgrades a Fail to a Warn, and
// a registered hash that does not match is always a Fail.
func (r *peerRun) cardHash(signatureOK bool) {
	sum := sha256.Sum256(r.card)
	got := hex.EncodeToString(sum[:])
	c := Check{Name: CheckCardHash, Detail: map[string]string{"card_sha256": got}}
	hashes := r.token.Payload.MetadataHashes
	switch {
	case len(hashes) == 0 && r.receiptOK && signatureOK:
		c.Verdict, c.Reason = Warn, ReasonCardHashNotRegistered
	case len(hashes) == 0 && !r.receiptOK:
		c.Verdict, c.Reason = Fail, "no metaDataHash registered and the log entry's receipt did not verify"
	case len(hashes) == 0:
		c.Verdict, c.Reason = Fail, "no metaDataHash registered and the card signature does not bind it to a log-attested key"
	case matchesAny(got, hashes):
		c.Verdict = Pass
	default:
		c.Verdict, c.Reason = Fail, "served card does not match the registered metaDataHash (card drift)"
	}
	r.add(c)
}

func matchesAny(hexSum string, hashes map[string]string) bool {
	for _, h := range hashes {
		h = strings.TrimPrefix(strings.TrimPrefix(h, "SHA256:"), "sha256:")
		if strings.EqualFold(h, hexSum) {
			return true
		}
	}
	return false
}

// cardSignature returns the card_signature check; the caller records it.
func (r *peerRun) cardSignature(ctx context.Context) Check {
	var ids []string
	for _, e := range r.token.Payload.ValidIdentityCerts {
		ids = append(ids, hex.EncodeToString(e.Fingerprint[:]))
	}
	err := wellknown.VerifyCard(r.card, wellknown.Expect{Host: r.url.Host, LeafSHA256s: ids}, func(u string) ([]byte, error) {
		return r.get(ctx, u)
	})
	c := Check{Name: CheckCardSignature, Verdict: Pass}
	if err != nil {
		c.Verdict, c.Reason = Fail, err.Error()
		var e *errs.Error
		if errors.As(err, &e) {
			c.Detail = map[string]string{"code": string(e.Code)}
		}
	}
	if len(ids) == 0 && err == nil {
		c.Verdict, c.Reason = Fail, "status token attests no identity certificate"
	}
	return c
}
