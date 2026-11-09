package guard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func policy(t *testing.T, hosts ...string) NetPolicy {
	t.Helper()
	p, err := NewNetPolicy(hosts)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAddressClasses(t *testing.T) {
	const (
		open    = "public"
		local   = "only named exactly"
		refused = "never"
	)
	for _, c := range []struct{ addr, want string }{
		{"8.8.8.8", open},
		{"1.1.1.1", open},
		{"172.32.0.1", open},
		{"100.128.0.1", open},
		{"2606:4700:4700::1111", open},
		{"127.0.0.1", local},
		{"127.8.9.10", local},
		{"10.1.2.3", local},
		{"172.16.0.1", local},
		{"172.31.255.255", local},
		{"192.168.1.1", local},
		{"100.64.0.1", local},
		{"100.101.102.103", local},
		{"::1", local},
		{"fd00::1", local},
		{"fc00::1", local},
		{"::ffff:127.0.0.1", local},
		{"::127.0.0.1", local},
		{"2002:7f00:1::", local},
		{"169.254.169.254", refused},
		{"169.254.170.2", refused},
		{"100.100.100.200", refused},
		{"0.0.0.0", refused},
		{"0.1.2.3", refused},
		{"255.255.255.255", refused},
		{"224.0.0.1", refused},
		{"239.255.255.250", refused},
		{"240.0.0.1", refused},
		{"192.0.2.1", refused},
		{"198.18.0.1", refused},
		{"::", refused},
		{"fe80::1", refused},
		{"fe80::1%eth0", refused},
		{"fec0::1", refused},
		{"ff02::1", refused},
		{"2001:db8::1", refused},
		{"::ffff:169.254.169.254", refused},
		{"64:ff9b::a9fe:a9fe", refused},
		{"2001:0:4136:e378:8000:63bf:3fff:fdd2", refused},
	} {
		ip := netip.MustParseAddr(c.addr)
		host := ip.WithZone("").String()
		openOK := NetPolicy{}.AllowAddr(host, 8080, ip) == nil
		namedOK := policy(t, entryFor(host, 8080)).AllowAddr(host, 8080, ip) == nil
		got := map[[2]bool]string{{true, true}: open, {false, true}: local, {false, false}: refused}[[2]bool{openOK, namedOK}]
		if got != c.want {
			t.Errorf("%s: %s (open %v, named %v), want %s", c.addr, got, openOK, namedOK, c.want)
		}
	}
}

func TestHostEntries(t *testing.T) {
	for _, ok := range []string{"api.example.com", "*.example.org", "localhost", "localhost:8080", "10.0.0.5", "[::1]:3000", "[::1]", "::1", "Example.COM", "nas.local"} {
		if _, err := NewNetPolicy([]string{ok}); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "http://x.com", "x.com/path", "user@x.com", "*.com", "*", "*.10.0.0.1", "x..com", "-x.com", "x.com:99999", "x.com:0", "x.com:", "a b", "[::1", "[x.com]:80", "[::1]80", "1:2:3"} {
		if _, err := NewNetPolicy([]string{bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	many := make([]string, MaxHosts+1)
	for i := range many {
		many[i] = fmt.Sprintf("h%d.example.com", i)
	}
	if _, err := NewNetPolicy(many); err == nil {
		t.Error("more than MaxHosts accepted")
	}
}

func TestNamedHostsNarrowAndOpenLocalOnesExactly(t *testing.T) {
	p := policy(t, "api.example.com", "*.example.org", "localhost:8080", "10.0.0.5", "[::1]:3000")
	for _, c := range []struct {
		host string
		port int
		ok   bool
	}{
		{"api.example.com", 443, true},
		{"API.example.com.", 443, true},
		{"a.b.example.org", 443, true},
		{"example.org", 443, false},
		{"evilexample.org", 443, false},
		{"evil.com", 443, false},
		{"localhost", 8080, true},
		{"localhost", 22, false},
		{"127.0.0.1", 8080, false},
		{"[::1]", 3000, true},
		{"::1", 3001, false},
	} {
		if err := p.AllowHost(c.host, c.port); (err == nil) != c.ok {
			t.Errorf("AllowHost(%s, %d) = %v", c.host, c.port, err)
		}
	}
	ip := netip.MustParseAddr
	if err := p.AllowAddr("localhost", 8080, ip("127.0.0.1")); err != nil {
		t.Error(err)
	}
	if err := p.AllowAddr("10.0.0.5", 80, ip("10.0.0.5")); err != nil {
		t.Error(err)
	}
	if err := p.AllowAddr("x.example.org", 443, ip("192.168.0.1")); !errors.Is(err, ErrRefused) {
		t.Errorf("a wildcard reached a private address: %v", err)
	}
	if err := policy(t, "169.254.169.254").AllowAddr("169.254.169.254", 80, ip("169.254.169.254")); !errors.Is(err, ErrRefused) {
		t.Errorf("metadata reached when named: %v", err)
	}
}

func TestRefusalsNameWhatToAdd(t *testing.T) {
	ip := netip.MustParseAddr
	for _, c := range []struct {
		err  error
		want string
	}{
		{NetPolicy{}.AllowAddr("localhost", 3000, ip("127.0.0.1")), `add "localhost:3000" to network hosts`},
		{NetPolicy{}.AllowAddr("::1", 3000, ip("::1")), `add "[::1]:3000" to network hosts`},
		{NetPolicy{}.AllowAddr("nas.local", 443, ip("192.168.1.9")), `add "nas.local" to network hosts`},
		{policy(t, "a.example.com").AllowHost("b.example.com", 8443), `add "b.example.com:8443" to network hosts`},
		{NetPolicy{}.AllowHeader("example.com", 443, "Authorization"), `add "example.com" to network hosts`},
	} {
		if c.err == nil || !strings.Contains(c.err.Error(), c.want) {
			t.Errorf("got %v, want it to say %s", c.err, c.want)
		}
	}
}

func TestHeaders(t *testing.T) {
	open := NetPolicy{}
	named := policy(t, "api.example.com")
	ported := policy(t, "localhost:8080")
	for _, c := range []struct {
		p    NetPolicy
		host string
		port int
		name string
		ok   bool
	}{
		{open, "example.com", 443, "Accept", true},
		{open, "example.com", 443, "X-Custom", true},
		{open, "example.com", 443, "Authorization", false},
		{open, "example.com", 443, "Cookie", false},
		{open, "example.com", 443, "Host", false},
		{open, "example.com", 443, "Proxy-Authorization", false},
		{open, "example.com", 443, "Transfer-Encoding", false},
		{open, "example.com", 443, "Connection", false},
		{named, "api.example.com", 443, "Authorization", true},
		{named, "api.example.com", 443, "cookie", true},
		{named, "evil.example.com", 443, "Cookie", false},
		{ported, "localhost", 8080, "Authorization", true},
		{ported, "localhost", 9090, "Authorization", false},
	} {
		if err := c.p.AllowHeader(c.host, c.port, c.name); (err == nil) != c.ok {
			t.Errorf("%s:%d %s: %v", c.host, c.port, c.name, err)
		}
	}
}

// server counts the requests it gets, so a test can show a refused one
// never connected.
type server struct {
	*httptest.Server
	hits atomic.Int32
	port string
}

func newServer(t *testing.T, h http.HandlerFunc) *server {
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL)
	s.port = u.Port()
	return s
}

func echoAuth(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "ok ", r.Header.Get("Authorization"))
}

// fakeDNS answers from a table, so the tests never leave the machine.
func fakeDNS(table map[string][]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, a := range table[strings.TrimSuffix(host, ".")] {
			out = append(out, netip.MustParseAddr(a))
		}
		if out == nil {
			return nil, fmt.Errorf("no such host %s", host)
		}
		return out, nil
	}
}

func get(t *testing.T, p NetPolicy, rawURL string, header ...string) (string, error) {
	t.Helper()
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := HTTPClient(p, 5*time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	return fmt.Sprintf("%d %s", res.StatusCode, body), err
}

var dns = fakeDNS(map[string][]string{
	"api.test":      {"127.0.0.1"},
	"other.test":    {"127.0.0.1"},
	"rebind.test":   {"127.0.0.1"},
	"x.wild.test":   {"127.0.0.1"},
	"mixed.test":    {"93.184.216.34", "127.0.0.1"},
	"metadata.test": {"169.254.169.254"},
})

func TestClientReachesANamedLocalHost(t *testing.T) {
	s := newServer(t, echoAuth)
	p := policy(t, "api.test")
	p.resolve = dns
	got, err := get(t, p, "http://api.test:"+s.port+"/", "Authorization", "Bearer x")
	if err != nil || got != "200 ok Bearer x" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestClientRefusesBeforeConnecting(t *testing.T) {
	s := newServer(t, echoAuth)
	wild := policy(t, "*.wild.test")
	wild.resolve = dns
	named := policy(t, "api.test", "metadata.test")
	named.resolve = dns
	open := NetPolicy{resolve: dns}
	for _, c := range []struct {
		name string
		p    NetPolicy
		url  string
		hdr  []string
	}{
		{"literal loopback, open", open, "http://127.0.0.1:" + s.port + "/", nil},
		{"literal loopback v6, open", open, "http://[::1]:" + s.port + "/", nil},
		{"name resolving to loopback, open", open, "http://rebind.test:" + s.port + "/", nil},
		{"name resolving to loopback under a wildcard", wild, "http://x.wild.test:" + s.port + "/", nil},
		{"one of its addresses is private, open", open, "http://mixed.test:" + s.port + "/", nil},
		{"resolves to metadata, even named", named, "http://metadata.test:" + s.port + "/", nil},
		{"literal metadata, named", named, "http://169.254.169.254/latest/meta-data", nil},
		{"host not named", named, "http://other.test:" + s.port + "/", nil},
		{"Proxy header", named, "http://api.test:" + s.port + "/", []string{"Proxy-Authorization", "x"}},
		{"Host override", named, "http://api.test:" + s.port + "/", []string{"Host", "other.test"}},
	} {
		if _, err := get(t, c.p, c.url, c.hdr...); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: want a refusal, got %v", c.name, err)
		}
	}
	if n := s.hits.Load(); n != 0 {
		t.Errorf("refused requests reached the server %d times", n)
	}
}

func TestClientChecksEveryRedirect(t *testing.T) {
	target := newServer(t, echoAuth)
	to := func(loc string) *server {
		return newServer(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, loc, http.StatusFound) })
	}
	p := policy(t, "api.test", "other.test")
	p.resolve = dns
	for _, c := range []struct {
		name, location string
	}{
		{"to an unnamed loopback address", "http://127.0.0.1:" + target.port + "/"},
		{"to localhost", "http://localhost:" + target.port + "/"},
		{"to metadata", "http://169.254.169.254/latest/meta-data"},
		{"to a private address", "http://10.0.0.7:" + target.port + "/"},
		{"to a name resolving privately", "http://rebind.test:" + target.port + "/"},
	} {
		from := to(c.location)
		if _, err := get(t, p, "http://api.test:"+from.port+"/"); !errors.Is(err, ErrRefused) {
			t.Errorf("redirect %s: want a refusal, got %v", c.name, err)
		}
		if from.hits.Load() != 1 {
			t.Errorf("redirect %s: the first hop didn't run", c.name)
		}
	}
	if n := target.hits.Load(); n != 0 {
		t.Errorf("refused redirects reached the target %d times", n)
	}
	from := to("http://other.test:" + target.port + "/")
	if got, err := get(t, p, "http://api.test:"+from.port+"/"); err != nil || got != "200 ok " {
		t.Fatalf("redirect to a named host: %q, %v", got, err)
	}
}

func TestClientIgnoresProxySettings(t *testing.T) {
	proxy := newServer(t, echoAuth)
	s := newServer(t, echoAuth)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	p := policy(t, "api.test")
	p.resolve = dns
	if _, err := get(t, p, "http://api.test:"+s.port+"/"); err != nil {
		t.Fatal(err)
	}
	if proxy.hits.Load() != 0 || s.hits.Load() != 1 {
		t.Fatalf("proxy %d, server %d", proxy.hits.Load(), s.hits.Load())
	}
}
