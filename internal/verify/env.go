// Package verify decides whether a peer agent may be talked to, and says why.
// It composes ans-sdk-go (verify, verify/scitt, pop) rather than
// reimplementing it: DNS badge and TLSA lookups, SCITT receipt and status
// token verification, and DPoP all come from the SDK. What is ours: the
// order of checks, fail-closed decisions, the status-token age policy, card
// hash and card signature checks, and one event per check.
package verify

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	ansverify "github.com/agentnameservice/ans-sdk-go/verify"
	"github.com/agentnameservice/ans-sdk-go/verify/scitt"

	"github.com/arjitsama/overpass/internal/config"
)

const (
	httpTimeout = 10 * time.Second
	clockSkew   = 60 * time.Second
)

// env is one ANS deployment: its log client, trusted root keys and resolvers.
type env struct {
	name  string
	cfg   config.Environment
	http  *http.Client
	keys  *scitt.KeyStore
	scitt *scitt.HTTPClient
	dns   ansverify.DNSResolver
	dane  ansverify.DANEResolver
}

func newEnv(name string, c config.Environment) (*env, error) {
	keys, err := scitt.NewKeyStore(c.RootKeys)
	if err != nil {
		return nil, fmt.Errorf("environment %s root_keys: %w", name, err)
	}
	rt, err := newRewrite(c.LogPublicURL, c.LogURL)
	if err != nil {
		return nil, fmt.Errorf("environment %s: %w", name, err)
	}
	hc := &http.Client{Timeout: httpTimeout, Transport: rt,
		// Logs and Finders answer directly; a redirect could leave the origin.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	opts := []scitt.HTTPClientOption{scitt.WithHTTPClient(hc)}
	if c.InsecureHTTP {
		opts = append(opts, scitt.WithAllowInsecureTransport())
	}
	sc, err := scitt.NewHTTPClient(strings.TrimSuffix(c.LogURL, "/"), opts...)
	if err != nil {
		return nil, fmt.Errorf("environment %s log_url: %w", name, err)
	}
	e := &env{name: name, cfg: c, http: hc, keys: keys, scitt: sc}
	dnsRes := ansverify.NewStandardDNSResolver()
	var daneOpts []ansverify.DANEResolverOption
	if c.DNSServer != "" {
		server := c.DNSServer
		dnsRes = dnsRes.WithResolver(&net.Resolver{PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, server)
			}})
		daneOpts = append(daneOpts, ansverify.WithDANEServer(server))
	}
	e.dns = dnsRes
	e.dane = ansverify.NewStandardDANEResolver(daneOpts...)
	return e, nil
}

// onLog reports whether u points at this environment's transparency log, so
// a TXT record cannot send the verifier to an attacker's "log".
func (e *env) onLog(u string) bool {
	for _, base := range []string{e.cfg.LogURL, e.cfg.LogPublicURL} {
		if base == "" {
			continue
		}
		b, err1 := url.Parse(base)
		p, err2 := url.Parse(u)
		if err1 == nil && err2 == nil && strings.EqualFold(b.Scheme, p.Scheme) && strings.EqualFold(b.Host, p.Host) &&
			p.User == nil && underPath(p.EscapedPath(), b.EscapedPath()) && p.EscapedPath() != strings.TrimSuffix(b.EscapedPath(), "/") {
			return true
		}
	}
	return false
}

// rewriteTransport maps URLs a log advertises (log_public_url) to where it is
// actually reached (log_url). The local reference stack advertises https but
// serves plain HTTP. Matching is on parsed scheme and host, and on the path
// at a "/" boundary.
type rewriteTransport struct {
	from, to *url.URL
	next     http.RoundTripper
}

func newRewrite(from, to string) (*rewriteTransport, error) {
	t := &rewriteTransport{next: http.DefaultTransport}
	if from == "" {
		return t, nil
	}
	f, err1 := url.Parse(from)
	d, err2 := url.Parse(to)
	if err1 != nil || err2 != nil || f.Host == "" || d.Host == "" {
		return nil, fmt.Errorf("log_public_url %q / log_url %q are not absolute URLs", from, to)
	}
	t.from, t.to = f, d
	return t, nil
}

func (t *rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.from != nil && strings.EqualFold(r.URL.Scheme, t.from.Scheme) && strings.EqualFold(r.URL.Host, t.from.Host) &&
		r.URL.User == nil && underPath(r.URL.EscapedPath(), t.from.EscapedPath()) {
		u := *r.URL
		u.Scheme, u.Host = t.to.Scheme, t.to.Host
		u.Path = strings.TrimSuffix(t.to.Path, "/") + strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(t.from.Path, "/"))
		u.RawPath = ""
		r = r.Clone(r.Context())
		r.URL, r.Host = &u, u.Host
	}
	return t.next.RoundTrip(r)
}

// underPath reports whether p is base or below it at a "/" boundary.
func underPath(p, base string) bool {
	base = strings.TrimSuffix(base, "/")
	return base == "" || p == base || strings.HasPrefix(p, base+"/")
}
