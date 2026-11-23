package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestParseCredentialsRefusesWhatACardCantShow(t *testing.T) {
	for _, body := range []string{
		`{"reason":"","fields":[{"name":"A"}]}`,
		`{"reason":"r","fields":[]}`,
		`{"reason":"r","fields":[{"name":"1A"}]}`,
		`{"reason":"r","fields":[{"name":"env"}]}`,
		`{"reason":"r","fields":[{"name":"KEY"},{"name":"key"}]}`,
		`{"reason":"r","fields":[{"name":"A"},{"name":"A"}]}`,
		`{"reason":"r","fields":[{"name":"A","url":"javascript:alert(1)"}]}`,
		`{"reason":"r","fields":[{"name":"A"},{"name":"B"},{"name":"C"},{"name":"D"},{"name":"E"},{"name":"F"},{"name":"G"},{"name":"H"},{"name":"I"}]}`,
	} {
		if _, err := ParseCredentials(json.RawMessage(body)); err == nil {
			t.Errorf("%s parsed", body)
		}
	}
	c, err := ParseCredentials(json.RawMessage(`{"reason":"r","fields":[{"name":"OPENAI_API_KEY","label":"OpenAI","url":"https://platform.openai.com/api-keys"}]}`))
	if err != nil || c.Names()[0] != "OPENAI_API_KEY" {
		t.Fatalf("%+v, %v", c, err)
	}
}

func TestProvidedTakesNamesAlone(t *testing.T) {
	c := Credentials{Reason: "r", Fields: []Field{{Name: "A"}, {Name: "B"}}}
	for raw, fits := range map[string]bool{
		`{}`:                         true,
		`{"provided":["B","A"]}`:     true,
		`{"provided":["A"]}`:         false,
		`{"provided":["A","B","C"]}`: false,
		`{"provided":["A","B"],"values":{"A":"x"}}`: false,
		`null`: false,
	} {
		p, err := ParseProvided(json.RawMessage(raw))
		if err == nil {
			err = p.Fit(c)
		}
		if (err == nil) != fits {
			t.Errorf("%s: %v, want fits %v", raw, err, fits)
		}
	}
}

func TestProvidePrintsNoValue(t *testing.T) {
	p := Provide{Values: map[string]string{"A": "probe-value-0123"}}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		if s := fmt.Sprintf(verb, p); strings.Contains(s, "probe-value") {
			t.Errorf("%s prints %s", verb, s)
		}
	}
}
