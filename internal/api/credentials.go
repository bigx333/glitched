package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// KindCredentials is a request for secrets the user types into a card:
// its body is Credentials, its answer Provided. The values never travel
// in the answer, an event or a command: the user's client hands them to
// the thread's host with Provide, which keeps them in the thread's vault
// and answers the request with the names alone.
const KindCredentials = "credentials"

// MaxCredentials is the most fields one request asks for.
const MaxCredentials = 8

// MaxSecret is the most bytes one value may hold.
const MaxSecret = 16 << 10

// Credentials is a credentials request's body: why the agent asks, and
// one field per value, each an environment variable's name. EnvFile is a
// file in the thread's workspace the values are also written to, "" for
// none.
type Credentials struct {
	Reason  string  `json:"reason"`
	Fields  []Field `json:"fields"`
	EnvFile string  `json:"envFile,omitempty"`
}

// Field is one value asked for: Name is the variable it is given as,
// Label what the card calls it, URL where to get it.
type Field struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	URL   string `json:"url,omitempty"`
}

var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// ValidName is whether s may name a credential: an environment variable's
// name that does not collide (even case-insensitively) with the vault's
// aggregate env script.
func ValidName(s string) bool { return envName.MatchString(s) && !strings.EqualFold(s, "env") }

// ParseCredentials reads a credentials request's body, and refuses one
// with no reason, no fields or more than MaxCredentials, a name that is no
// variable's or given twice, or a URL that isn't https.
func ParseCredentials(body json.RawMessage) (Credentials, error) {
	var c Credentials
	if err := json.Unmarshal(body, &c); err != nil {
		return Credentials{}, fmt.Errorf("credentials: %w", err)
	}
	return c, c.Check()
}

// Check is nil when c is a request a card can show.
func (c Credentials) Check() error {
	if strings.TrimSpace(c.Reason) == "" {
		return errors.New("credentials: no reason")
	}
	if n := len(c.Fields); n < 1 || n > MaxCredentials {
		return fmt.Errorf("credentials: %d fields, want 1 to %d", n, MaxCredentials)
	}
	for i, f := range c.Fields {
		if !ValidName(f.Name) {
			return fmt.Errorf("credentials: field %d's name %q is not an upper-case environment variable's", i+1, f.Name)
		}
		if slices.ContainsFunc(c.Fields[:i], func(g Field) bool { return g.Name == f.Name }) {
			return fmt.Errorf("credentials: %s is asked for twice", f.Name)
		}
		if f.URL != "" {
			if u, err := url.Parse(f.URL); err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("credentials: %s's url is not an https link", f.Name)
			}
		}
	}
	return nil
}

// Names are the variables c asks for, in its order.
func (c Credentials) Names() []string {
	names := make([]string, len(c.Fields))
	for i, f := range c.Fields {
		names[i] = f.Name
	}
	return names
}

// Provided answers a credentials request: the names whose values the
// thread's vault now holds. None declines it.
type Provided struct {
	Names []string `json:"provided,omitempty"`
}

// ParseProvided reads an answer to a credentials request, and refuses
// anything else, values above all: an answer is stored as it is.
func ParseProvided(raw json.RawMessage) (Provided, error) {
	var p *Provided
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return Provided{}, fmt.Errorf("provided: %w", err)
	}
	if p == nil {
		return Provided{}, errors.New("provided: null")
	}
	return *p, nil
}

// Fit is nil when p answers c: no names, or each name c asks for once.
func (p Provided) Fit(c Credentials) error {
	if len(p.Names) == 0 {
		return nil
	}
	want, got := slices.Sorted(slices.Values(c.Names())), slices.Sorted(slices.Values(p.Names))
	if !slices.Equal(want, got) {
		return fmt.Errorf("provided %s, but the request asks for %s", strings.Join(got, ", "), strings.Join(want, ", "))
	}
	return nil
}

// Provide carries the values the user typed for a credentials request to
// the thread's host. It is never a command or an event, and its values
// are never logged.
type Provide struct {
	Thread     ID                `json:"thread"`
	Request    ID                `json:"request"`
	Generation uint64            `json:"generation"`
	Values     map[string]string `json:"values"`
}

// String keeps the values out of anything that prints p.
func (p Provide) String() string {
	return fmt.Sprintf("Provide{%s %s, %d values}", p.Thread, p.Request, len(p.Values))
}

// GoString is String, for %#v.
func (p Provide) GoString() string { return p.String() }

// Check is nil when p gives a value for exactly each name c asks for, each
// a single line of 1 to MaxSecret bytes of UTF-8 without NUL.
func (p Provide) Check(c Credentials) error {
	if len(p.Values) == 0 {
		return errors.New("no values")
	}
	if err := (Provided{Names: slices.Collect(maps.Keys(p.Values))}).Fit(c); err != nil {
		return err
	}
	for name, v := range p.Values {
		switch {
		case v == "":
			return fmt.Errorf("%s is empty", name)
		case len(v) > MaxSecret:
			return fmt.Errorf("%s is longer than %d KiB", name, MaxSecret>>10)
		case strings.ContainsAny(v, "\r\n\x00"):
			return fmt.Errorf("%s must be one line", name)
		case !utf8.ValidString(v):
			return fmt.Errorf("%s is not text", name)
		}
	}
	return nil
}

// Ask is glitched mcp asking its thread's host for credentials, for the
// agent's tool call Call, while the thread's turn runs at Access.
type Ask struct {
	Thread      ID          `json:"thread"`
	Access      Access      `json:"access"`
	Call        string      `json:"call"`
	Credentials Credentials `json:"credentials"`
}

// The ways an Ask ends.
const (
	AskProvided = "provided"
	AskDeclined = "declined"
	AskCanceled = "cancelled"
	AskTimedOut = "timed_out"
)

// Asked is how an Ask ended. With AskProvided, Dir is the thread's vault:
// a file per name, and Env, a shell script that exports them all;
// EnvFile is the workspace file they were also written to, if any.
type Asked struct {
	Status  string            `json:"status"`
	Names   []string          `json:"names,omitempty"`
	Env     string            `json:"env,omitempty"`
	Files   map[string]string `json:"files,omitempty"`
	EnvFile string            `json:"env_file,omitempty"`
}
