// Command topos-plugin-proton: this file implements the IMAP
// connection layer against Proton Mail Bridge (reached through a LAN
// forwarder — see 03-RESEARCH.md Pitfall 2). See plugin.go for the
// toposv1.SourcePlugin adapter and main.go for the subprocess
// entrypoint.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	imapclient "github.com/emersion/go-imap/client"
)

// ErrNotFound is returned when a mailbox or message that Match/Fetch
// expected to find has vanished between listing/scanning and the
// follow-up read (e.g. deleted between LIST and SELECT, or between Match
// and a later Fetch) — distinct from a transport/protocol failure so
// callers can map it to "safe to skip this one item" rather than "fail
// the whole call".
var ErrNotFound = errors.New("proton: not found")

// ErrForeignHost is returned when the outbound host allowlist refuses a
// dial target that is neither the configured Bridge forwarder host nor a
// loopback address. This is the enforcement half of the prohibition that
// plugin outbound traffic MUST NOT reach any host other than the user's
// own configured source instance and the loopback interface (PROJECT.md
// Constraints), adapted from plugins/silverbullet/client.go's allowHost
// to go-imap's Dialer interface instead of http.Transport.DialContext.
var ErrForeignHost = errors.New("proton: foreign host refused")

// ErrNoMessageID is returned when a message's ENVELOPE carries no
// Message-Id header — such a message cannot be safely deduped (03-
// RESEARCH.md Pitfall 6) or later re-resolved by Fetch's UID SEARCH
// HEADER Message-Id step, so it is skipped rather than indexed under an
// empty-string key.
var ErrNoMessageID = errors.New("proton: message has no Message-Id header")

// bridgeCertServerName is the hostname Proton Mail Bridge's self-signed
// certificate is always issued for, regardless of which LAN forwarder
// address this client actually dials — Bridge only ever binds and issues
// a certificate for its own loopback interface (03-RESEARCH.md Pattern
// 4). Confirmed live for this deployment (Task 1): the exported
// certificate's Subject Alternative Name is exactly "IP Address:
// 127.0.0.1", with no LAN hostname entry. Setting TLS ServerName to this
// fixed value (rather than the dialed forwarder host) is what makes
// hostname verification succeed while still verifying the full chain via
// the pinned RootCAs pool below — this is never a substitute for
// verification: certificate verification is never disabled anywhere in
// this file.
const bridgeCertServerName = "127.0.0.1"

const (
	// syncDialTimeout bounds a single connection attempt during Match/Fetch
	// (sync-time and item-open work). Nothing in the kernel wraps these
	// RPCs in its own timeout, so the plugin must bound itself.
	syncDialTimeout = 10 * time.Second
	// healthDialTimeout bounds Health's own connection attempt — shorter
	// than syncDialTimeout so an unreachable Bridge is reported quickly
	// rather than after a long hang (SRC-01 success criterion 5).
	healthDialTimeout = 5 * time.Second
)

// Client is a host-pinned, TLS-verified IMAP connection factory for one
// configured Proton Mail Bridge instance. Every connection this client
// opens is checked against allowHost before any bytes leave the process,
// and every connection completes TLS (either implicit via imaps:// or
// mandatory STARTTLS via imap://) before Login is ever called — there is
// no plaintext fallback path.
type Client struct {
	dialAddr    string // host:port to dial, from base_url
	allowedHost string // lowercased hostname allowHost permits (plus localhost/loopback)
	implicitTLS bool   // true for imaps://, false for imap:// (mandatory StartTLS)
	username    string
	password    string
	tlsConfig   *tls.Config

	// dial is the connection-establishment step, exposed as an unexported
	// field of function type (rather than inlined in connect) so plan
	// 03-02's transcript test can substitute a plaintext dial to a local
	// fake IMAP server without changing any production code path. Defaults
	// to realDial.
	dial func(timeout time.Duration) (*imapclient.Client, error)
}

// NewClient builds a Client for baseURL (an "imap://host:port" or
// "imaps://host:port" URL — the scheme selects mandatory STARTTLS versus
// implicit TLS). username and password authenticate the IMAP LOGIN
// command; password is Bridge's own generated Bridge-scoped password,
// never the user's real Proton account password (T-03-03). caCertPath is
// an optional path to a PEM-encoded CA certificate to trust in addition
// to (by replacing, per Go's tls.Config.RootCAs semantics) the system
// trust store — required in practice, since Bridge's certificate is
// self-signed (03-RESEARCH.md Pattern 4). An unreadable or unparsable
// caCertPath falls through to the system trust store rather than failing
// construction, matching plugins/silverbullet/client.go's NewClient
// precedent: verification then fails at request time with a clear
// Unavailable/Health error, the correct place for a config-fixable
// problem to surface.
func NewClient(baseURL, username, password, caCertPath string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("proton: parse base_url: %w", err)
	}

	var implicitTLS bool
	switch u.Scheme {
	case "imaps":
		implicitTLS = true
	case "imap":
		implicitTLS = false
	default:
		return nil, fmt.Errorf("proton: unsupported base_url scheme %q (must be \"imap\" or \"imaps\")", u.Scheme)
	}

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil, fmt.Errorf("proton: base_url has no host")
	}

	dialAddr := u.Host
	if u.Port() == "" {
		defaultPort := "143"
		if implicitTLS {
			defaultPort = "993"
		}
		dialAddr = net.JoinHostPort(host, defaultPort)
	}

	tlsConfig := &tls.Config{ServerName: bridgeCertServerName}
	if caCertPath != "" {
		if pemBytes, err := os.ReadFile(caCertPath); err == nil {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(pemBytes) {
				tlsConfig.RootCAs = pool
			}
			// An unparsable PEM falls through to the system trust store
			// (tlsConfig.RootCAs stays nil) rather than failing
			// construction — see doc comment above.
		}
		// A missing/unreadable ca_cert path is treated the same way.
	}

	c := &Client{
		dialAddr:    dialAddr,
		allowedHost: host,
		implicitTLS: implicitTLS,
		username:    username,
		password:    password,
		tlsConfig:   tlsConfig,
	}
	c.dial = c.realDial
	return c, nil
}

// realDial is the production dial step: a host-pinned TCP dial (via
// pinnedDialer/allowHost) followed by either implicit TLS
// (DialWithDialerTLS, imaps://) or a plaintext connect immediately
// followed by a mandatory StartTLS whose failure aborts the connection
// (imap://) — there is no code path that calls Login on a connection
// that did not complete TLS.
func (c *Client) realDial(timeout time.Duration) (*imapclient.Client, error) {
	dialer := &pinnedDialer{client: c, inner: &net.Dialer{Timeout: timeout}}

	if c.implicitTLS {
		conn, err := imapclient.DialWithDialerTLS(dialer, c.dialAddr, c.tlsConfig)
		if err != nil {
			return nil, fmt.Errorf("proton: dial (implicit tls): %w", err)
		}
		return conn, nil
	}

	conn, err := imapclient.DialWithDialer(dialer, c.dialAddr)
	if err != nil {
		return nil, fmt.Errorf("proton: dial: %w", err)
	}
	if err := conn.StartTLS(c.tlsConfig); err != nil {
		conn.Close()
		return nil, fmt.Errorf("proton: starttls: %w", err)
	}
	return conn, nil
}

// connect dials (via c.dial, so a test can substitute a fake server),
// applies timeout as the connection's command timeout, and logs in.
// Login failures are wrapped with the operation name and the server's
// own message only — the password is never included in an error string,
// a log line, or a HealthResponse.LastError (T-03-03). When the
// configured token's shape rules it out as a Bridge-generated app
// password, credentials.go's shape-warning helper appends a fixed-text
// warning after the server's own message; the warning is a compile-time
// constant carrying no runtime data, so this does not weaken the
// guarantee above — it only ever adds words to a login that has already
// failed.
func (c *Client) connect(timeout time.Duration) (*imapclient.Client, error) {
	conn, err := c.dial(timeout)
	if err != nil {
		return nil, err
	}
	conn.Timeout = timeout

	if err := conn.Login(c.username, c.password); err != nil {
		conn.Close()
		if warning := bridgeTokenShapeWarning(c.password); warning != "" {
			return nil, fmt.Errorf("proton: login: %v — %s", err, warning)
		}
		return nil, fmt.Errorf("proton: login: %v", err)
	}
	return conn, nil
}

// allowHost is the outbound host allowlist predicate. It strips any port
// and IPv6 brackets from hostport, lowercases it, and permits the value
// when it equals this client's configured Bridge forwarder hostname,
// when net.ParseIP reports a loopback address, or when it is the literal
// "localhost"; otherwise it returns an error wrapping ErrForeignHost and
// naming the refused host.
//
// Port is deliberately outside the comparison: the configured host is
// the user's own Bridge forwarder, and a reverse proxy in front of it
// may legitimately move between ports on that same host.
func (c *Client) allowHost(hostport string) error {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))

	if host != "" && c.allowedHost != "" && host == c.allowedHost {
		return nil
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrForeignHost, host)
}

// pinnedDialer adapts allowHost to go-imap v1's client.Dialer interface
// (Dial(network, addr string) (net.Conn, error) — same shape as
// net.Dialer's own method), refusing any host that is not the configured
// Bridge host, localhost, or a loopback address before the inner
// net.Dialer opens anything.
type pinnedDialer struct {
	client *Client
	inner  *net.Dialer
}

func (d *pinnedDialer) Dial(network, addr string) (net.Conn, error) {
	if err := d.client.allowHost(addr); err != nil {
		return nil, err
	}
	return d.inner.Dial(network, addr)
}
