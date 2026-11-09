package guard

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// MaxHosts caps a plugin's named hosts, so the consent sheet stays
// readable.
const MaxHosts = 64

// place is where an address is, as a plugin may reach it.
type place uint8

const (
	public place = iota
	// private is this device or a network the user is on: reached only
	// through a host named exactly.
	private
	// never is link-local space (where clouds keep instance metadata and
	// credentials), addresses no host has, and multicast: never reached.
	never
)

func placeOf(ip netip.Addr) place {
	ip = ip.WithZone("").Unmap()
	if ip.Is4() {
		return place4(ip.As4())
	}
	b := ip.As16()
	// An IPv4 address carried inside an IPv6 one goes where the IPv4 does:
	// IPv4-compatible (::a.b.c.d), NAT64 (64:ff9b::/96), 6to4 (2002::/16).
	switch {
	case [12]byte(b[:12]) == [12]byte{} && !ip.IsLoopback() && !ip.IsUnspecified():
		return place4([4]byte(b[12:]))
	case [12]byte(b[:12]) == [12]byte{0, 0x64, 0xff, 0x9b}:
		return place4([4]byte(b[12:]))
	case b[0] == 0x20 && b[1] == 0x02:
		return place4([4]byte(b[2:6]))
	case ip.IsUnspecified() || ip.IsMulticast():
		return never
	// fe80::/10 link-local, fec0::/10 old site-local.
	case b[0] == 0xfe && (b[1]&0xc0 == 0x80 || b[1]&0xc0 == 0xc0):
		return never
	// 2001:db8::/32 documentation; 2001::/32 Teredo hides where it goes.
	case b[0] == 0x20 && b[1] == 0x01 && (b[2] == 0x0d && b[3] == 0xb8 || b[2] == 0 && b[3] == 0):
		return never
	case ip.IsLoopback() || b[0]&0xfe == 0xfc:
		return private
	}
	return public
}

func place4(o [4]byte) place {
	a, b, c := o[0], o[1], o[2]
	switch {
	case a == 0, a == 169 && b == 254, a >= 224: // this network, link-local, multicast, 240/4, broadcast
		return never
	case a == 192 && b == 0 && c == 2, a == 198 && b == 51 && c == 100, a == 203 && b == 0 && c == 113: // documentation
		return never
	case a == 198 && b&0xfe == 18, a == 192 && b == 0 && c == 0: // benchmarking, IETF protocol
		return never
	case o == [4]byte{100, 100, 100, 200}: // Alibaba's metadata, inside carrier space
		return never
	case a == 127, a == 10, a == 172 && b&0xf0 == 16, a == 192 && b == 168, a == 100 && b&0xc0 == 64:
		return private
	}
	return public
}

// hostRule is one named host: exact, or "*." and a domain, with a port or
// any port.
type hostRule struct {
	host     string // lowercase; an IPv6 address without brackets
	wildcard bool
	port     int // 0: any
}

func parseHostRule(entry string) (hostRule, error) {
	bad := fmt.Errorf("%q isn't a host (api.example.com, *.example.org, localhost:8080, [::1]:3000)", entry)
	entry = strings.TrimSpace(entry)
	if entry == "" || len(entry) > 253 || strings.ContainsAny(entry, "/@?# ") {
		return hostRule{}, bad
	}
	host, portText, hasPort := entry, "", false
	if rest, ok := strings.CutPrefix(entry, "["); ok {
		h, after, ok := strings.Cut(rest, "]")
		if !ok {
			return hostRule{}, bad
		}
		if ip, err := netip.ParseAddr(h); err != nil || !ip.Is6() {
			return hostRule{}, bad
		}
		host = h
		if after != "" {
			p, ok := strings.CutPrefix(after, ":")
			if !ok {
				return hostRule{}, bad
			}
			portText, hasPort = p, true
		}
	} else if i := strings.LastIndexByte(entry, ':'); i >= 0 {
		if strings.Count(entry, ":") == 1 {
			host, portText, hasPort = entry[:i], entry[i+1:], true
		} else if _, err := netip.ParseAddr(entry); err != nil {
			return hostRule{}, bad
		}
	}
	port := 0
	if hasPort {
		p, err := strconv.Atoi(portText)
		if err != nil || p < 1 || p > 65535 {
			return hostRule{}, bad
		}
		port = p
	}
	host = strings.ToLower(host)
	name, wildcard := strings.CutPrefix(host, "*.")
	_, ipErr := netip.ParseAddr(name)
	isIP := ipErr == nil
	labels := !isIP && !slices.ContainsFunc(strings.Split(name, "."), func(l string) bool { return !validLabel(l) })
	if !isIP && !labels || wildcard && (isIP || !strings.Contains(name, ".")) {
		return hostRule{}, bad
	}
	return hostRule{host: name, wildcard: wildcard, port: port}, nil
}

func validLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for i := range len(l) {
		if c := l[i]; !isAlnum(c) && c != '-' {
			return false
		}
	}
	return true
}

func (r hostRule) matches(host string, port int) bool {
	return r.matchesHost(host) && (r.port == 0 || r.port == port)
}

func (r hostRule) matchesHost(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if r.wildcard {
		return strings.HasSuffix(host, "."+r.host)
	}
	return host == r.host
}

// NetPolicy is where a plugin's requests may go. The zero value is an
// open plugin: public addresses only.
type NetPolicy struct {
	hosts []hostRule
	// resolve replaces DNS in tests, so they stay off the network.
	resolve func(ctx context.Context, host string) ([]netip.Addr, error)
}

// NewNetPolicy builds a plugin's policy from the hosts it names. Naming
// any narrows it to those, and naming a local host exactly is the only way
// to reach one.
func NewNetPolicy(hosts []string) (NetPolicy, error) {
	if len(hosts) > MaxHosts {
		return NetPolicy{}, fmt.Errorf("a plugin names %d hosts at most", MaxHosts)
	}
	var p NetPolicy
	for _, h := range hosts {
		r, err := parseHostRule(h)
		if err != nil {
			return NetPolicy{}, err
		}
		p.hosts = append(p.hosts, r)
	}
	return p, nil
}

func (p NetPolicy) named(host string, port int, exact bool) bool {
	return slices.ContainsFunc(p.hosts, func(r hostRule) bool { return (!exact || !r.wildcard) && r.matches(host, port) })
}

// AllowHost checks the host and port a request asks for, before any
// lookup.
func (p NetPolicy) AllowHost(host string, port int) error {
	host = unbracket(host)
	if len(p.hosts) == 0 || p.named(host, port, false) {
		return nil
	}
	return refuse("this plugin reaches only the hosts it names: add %q to network hosts", entryFor(host, port))
}

// AllowAddr checks an address host:port is at (resolved, or the host
// itself), before connecting. A wildcard never opens a private address,
// so a public name can't be rebound onto the user's network.
func (p NetPolicy) AllowAddr(host string, port int, ip netip.Addr) error {
	host = unbracket(host)
	switch placeOf(ip) {
	case never:
		return refuse("%s is at %s, which no plugin may reach", host, ip)
	case private:
		if !p.named(host, port, true) {
			return refuse("%s is at %s, on this device or your network: add %q to network hosts", host, ip, entryFor(host, port))
		}
	}
	return nil
}

// AllowHeader checks a header a request sends to host:port. Headers that
// shape the connection are Go's to set, and credentials go only to a host
// the user saw named, never to whatever an open plugin reaches.
func (p NetPolicy) AllowHeader(host string, port int, name string) error {
	switch lower := strings.ToLower(name); {
	case strings.HasPrefix(lower, "proxy-") || slices.Contains([]string{"host", "connection", "content-length",
		"transfer-encoding", "te", "trailer", "upgrade", "keep-alive", "expect"}, lower):
		return refuse("a plugin can't set the %s header", name)
	case lower == "authorization" || lower == "cookie":
		host = unbracket(host)
		if !p.named(host, port, false) {
			return refuse("a plugin sends %s only to hosts it names: add %q to network hosts", name, entryFor(host, port))
		}
	}
	return nil
}

// HTTPClient is an http.Client that holds every request, redirect and
// connection to p. It never uses a proxy: a proxy's address would stand in
// for where the request really goes, and the checks would see only it.
func HTTPClient(p NetPolicy, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	t := &http.Transport{
		Proxy:                 nil,
		DialContext:           p.dial(dialer),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: guarded{p, t},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return p.checkRequest(req)
		},
		Timeout: timeout,
	}
}

// guarded checks each request it sends, the first and every redirect, as
// the client hands them over.
type guarded struct {
	p NetPolicy
	t http.RoundTripper
}

func (g guarded) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := g.p.checkRequest(req); err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	return g.t.RoundTrip(req)
}

func (p NetPolicy) checkRequest(req *http.Request) error {
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return refuse("a plugin's requests are http or https, not %q", req.URL.Scheme)
	}
	if req.Host != "" && req.Host != req.URL.Host {
		return refuse("a request's Host is its URL's host")
	}
	host := req.URL.Hostname()
	port := 443
	if req.URL.Scheme == "http" {
		port = 80
	}
	if text := req.URL.Port(); text != "" {
		n, err := strconv.Atoi(text)
		if err != nil {
			return refuse("%q isn't a port", text)
		}
		port = n
	}
	if err := p.AllowHost(host, port); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(req.Header)) {
		if err := p.AllowHeader(host, port, name); err != nil {
			return err
		}
	}
	return nil
}

// dial resolves the name itself, checks every address it gets, and then
// connects to those exact addresses, so a second lookup can't hand the
// connection somewhere the check never saw.
func (p NetPolicy) dial(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, portText, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			return nil, err
		}
		if err := p.AllowHost(host, port); err != nil {
			return nil, err
		}
		ips, err := p.lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("%s has no address", host)
		}
		for _, ip := range ips {
			if err := p.AllowAddr(host, port, ip); err != nil {
				return nil, err
			}
		}
		var first error
		for _, ip := range ips {
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), portText))
			if err == nil {
				return c, nil
			}
			if first == nil {
				first = err
			}
		}
		return nil, first
	}
}

func (p NetPolicy) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	if p.resolve != nil {
		return p.resolve(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func unbracket(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
}

// entryFor is the network hosts entry a refusal tells the plugin to add:
// what its URL says, with the port unless it is a scheme's default.
func entryFor(host string, port int) string {
	if port == 80 || port == 443 {
		return host
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + strconv.Itoa(port)
	}
	return host + ":" + strconv.Itoa(port)
}
