package providerlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// state is a new state directory, where the system can keep a log.
func state(t *testing.T) string {
	t.Helper()
	if !Supported {
		t.Skip("this system keeps no provider log")
	}
	return t.TempDir()
}

// on is a log in a new state directory, on, and its folder.
func on(t *testing.T) (*Log, string) {
	t.Helper()
	state := state(t)
	l, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	l.SetOn(true)
	t.Cleanup(l.Close)
	return l, Under(state)
}

// held is a held func handing Write values, all of them.
func held(values ...string) func() ([]string, bool) {
	return func() ([]string, bool) { return values, true }
}

// write has l take line, whole, for thread.
func write(l *Log, thread api.ID, proto provider.Protocol, pipe provider.Pipe, line string, held func() ([]string, bool)) {
	l.Write(thread, proto, pipe, []byte(line), len(line), held)
}

// diag writes a stderr line for thread.
func diag(l *Log, thread api.ID, line string) {
	write(l, thread, provider.ClaudeStreamJSON, provider.Stderr, line, nil)
}

// records are the records in the file at path, decoded.
func records(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rs []map[string]any
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(nil, 1<<20)
	for s.Scan() {
		d := json.NewDecoder(bytes.NewReader(s.Bytes()))
		d.UseNumber()
		var r map[string]any
		if err := d.Decode(&r); err != nil {
			t.Fatalf("%s holds %q, not a record: %v", path, s.Bytes(), err)
		}
		if _, err := time.Parse(time.RFC3339Nano, r["time"].(string)); err != nil {
			t.Fatalf("a record with no time: %s", s.Bytes())
		}
		rs = append(rs, r)
	}
	return rs
}

// shown is v as JSON, as the log writes it, but for a record's time.
func shown(v any) string {
	if r, ok := v.(map[string]any); ok {
		delete(r, "time")
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

// only is the record of line, written alone for a thread that has held
// values, but for its length, which it checks.
func only(t *testing.T, proto provider.Protocol, pipe provider.Pipe, values []string, line string) string {
	t.Helper()
	l, dir := on(t)
	thread := api.NewID()
	write(l, thread, proto, pipe, line, held(values...))
	l.Flush()
	rs := records(t, File(dir, thread))
	if len(rs) != 1 {
		t.Fatalf("%d records, want 1", len(rs))
	}
	if n := rs[0]["bytes"]; n != json.Number(strconv.Itoa(len(line))) {
		t.Errorf("a record of a line of %v bytes, want %d", n, len(line))
	}
	delete(rs[0], "bytes")
	return shown(rs[0])
}

// A record keeps a line's pipe and length, its kind where its protocol's
// table names it, and, for JSON, its skeleton: keys, and each value as
// its type and size, but for identifier tokens in the fields that say what
// a message is.
func TestARecordIsTheShapeOfItsLine(t *testing.T) {
	claude, codex, acp := provider.ClaudeStreamJSON, provider.CodexAppServer, provider.ACP
	stdin, stdout := provider.Stdin, provider.Stdout
	for _, c := range []struct {
		name  string
		proto provider.Protocol
		pipe  provider.Pipe
		line  string
		want  string
	}{
		{"a Claude prompt", claude, stdin, `{"type":"user","message":{"role":"user","content":"my key"},"session_id":"","n":3,"ok":false,"none":null}`,
			`{"json":{"message":{"content":"<str:6>","role":"user"},"n":"<num>","none":null,"ok":"<bool>","session_id":"<str:0>","type":"user"},"kind":"user","pipe":"stdin"}`},
		{"Claude's init", claude, stdout, `{"type":"system","subtype":"init","model":"m"}`,
			`{"json":{"model":"<str:1>","subtype":"init","type":"system"},"kind":"system/init","pipe":"stdout"}`},
		{"a Claude text piece", claude, stdout, `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"sk"}}}`,
			`{"json":{"event":{"delta":{"text":"<str:2>","type":"<str:10>"},"type":"<str:19>"},"type":"stream_event"},"kind":"stream_event/content_block_delta","pipe":"stdout"}`},
		{"a Claude kind of no table's", claude, stdout, `{"type":"novel","text":"x"}`, `{"json":{"text":"<str:1>","type":"novel"},"pipe":"stdout"}`},
		{"a token no identifier", claude, stdout, `{"type":"two words","status":"` + strings.Repeat("s", 65) + `"}`, `{"json":{"status":"<str:65>","type":"<str:9>"},"pipe":"stdout"}`},
		{"a Codex request", codex, stdin, `{"id":3,"method":"turn/start","params":{"input":[{"type":"text","text":"sk-1"}]}}`,
			`{"json":{"id":3,"method":"turn/start","params":{"input":[{"text":"<str:4>","type":"<str:4>"}]}},"kind":"turn/start","pipe":"stdin"}`},
		{"a Codex response", codex, stdout, `{"id":7,"result":{"thread":{"id":"t"}}}`,
			`{"json":{"id":7,"result":{"thread":{"id":"<str:1>"}}},"kind":"response","pipe":"stdout"}`},
		{"an id no integer", codex, stdout, `{"id":7.5,"result":null}`, `{"json":{"id":"<num>","result":null},"kind":"response","pipe":"stdout"}`},
		{"a Codex method of no table's", codex, stdout, `{"method":"novel","params":{}}`, `{"json":{"method":"novel","params":{}},"pipe":"stdout"}`},
		{"an ACP message piece", acp, stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"sk"}}}}`,
			`{"json":{"jsonrpc":"<str:3>","method":"session/update","params":{"update":{"content":{"text":"<str:2>","type":"<str:4>"},"sessionUpdate":"agent_message_chunk"}}},"kind":"session/update/agent_message_chunk","pipe":"stdout"}`},
		{"an array", claude, stdout, `["a",1,[true]]`, `{"json":["<str:1>","<num>",["<bool>"]],"pipe":"stdout"}`},
		{"malformed JSON", claude, stdout, `{"type":"user","text":"sk\u002d1`, `{"pipe":"stdout"}`},
		{"two JSON values", claude, stdout, `{"type":"user"} {"type":"user"}`, `{"pipe":"stdout"}`},
		{"not JSON", codex, stdout, "Signed in as sk-1", `{"pipe":"stdout"}`},
		{"a protocol of no table's", "", stdout, `{"type":"user"}`, `{"json":{"type":"user"},"pipe":"stdout"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := only(t, c.proto, c.pipe, nil, c.line); got != c.want {
				t.Errorf("record %s, want %s", got, c.want)
			}
		})
	}
}

// A skeleton keeps keys that are names, and puts each other key's length
// and place among its object's in its stead, so none take another's;
// nests up to depthCap deep, and keeps up to itemCap items of an array,
// then counts the rest.
func TestASkeletonKeepsToItsBounds(t *testing.T) {
	deep := "1"
	for range depthCap + 1 {
		deep = "[" + deep + "]"
	}
	long := strings.Repeat("k", 64)
	for _, c := range []struct{ name, json, want string }{
		{"keys no names", `{"` + long + `":1,"` + long + `k":2,"` + long + `j":"a","two words":true,"1st":null,"<key:65#1>":1}`,
			`{"<key:10#5>":"<num>","<key:3#4>":null,"<key:65#1>":"<num>","<key:65#2>":"<str:1>","<key:9#3>":"<bool>","` + long + `":"<num>"}`},
		{"a duplicate key", `{"a":1,"a":"x"}`, `{"a":"<str:1>"}`},
		{"deep nesting", deep, strings.Repeat("[", depthCap) + `"<arr:1>"` + strings.Repeat("]", depthCap)},
		{"deep objects", strings.Repeat(`{"a":`, depthCap) + `{"b":1,"c":[2]}` + strings.Repeat("}", depthCap), strings.Repeat(`{"a":`, depthCap) + `"<obj:2>"` + strings.Repeat("}", depthCap)},
		{"a long array", "[" + strings.Repeat("0,", itemCap) + "[0,{}]]", "[" + strings.Repeat(`"<num>",`, itemCap) + `"<arr:65>"]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, v := shape("", []byte(c.json), nil); shown(v) != c.want {
				t.Errorf("skeleton %s, want %s", shown(v), c.want)
			}
		})
	}
}

// fixtures are lines the providers wrote, as the adapters' tests hold
// them: a Claude message, a Codex message as it completes, and an ACP
// tool call with its input.
func fixtures(t *testing.T) map[provider.Protocol]string {
	t.Helper()
	line := func(path string, n int) string {
		b, err := os.ReadFile(filepath.Join("..", "provider", path))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(string(b), "\n")[n-1]
	}
	var claude struct{ Recv json.RawMessage }
	if err := json.Unmarshal([]byte(line("claude/testdata/hi.jsonl", 11)), &claude); err != nil {
		t.Fatal(err)
	}
	return map[provider.Protocol]string{
		provider.ClaudeStreamJSON: string(claude.Recv),
		provider.CodexAppServer:   strings.TrimPrefix(line("codex/testdata/codex-0.171.0-turns.wire", 39), "<< "),
		provider.ACP:              strings.TrimPrefix(line("acp/testdata/agy-1.5.14-hi.wire", 11), "<< "),
	}
}

// A real provider's line keeps its shape and nothing it said: no string
// of it reaches the record but as a key or an envelope's identifier
// token, and no number but an integer id.
func TestARealLineKeepsNothingItSaid(t *testing.T) {
	for proto, want := range map[provider.Protocol]string{
		provider.ClaudeStreamJSON: `{"json":{"message":{"container":null,"content":[{"text":"<str:17>","type":"text"}],"context_management":null,"diagnostics":null,"id":"<str:28>","model":"<str:16>","role":"assistant","stop_details":null,"stop_reason":null,"stop_sequence":null,"type":"<str:7>","usage":{"cache_creation":{"ephemeral_1h_input_tokens":"<num>","ephemeral_5m_input_tokens":"<num>"},"cache_creation_input_tokens":"<num>","cache_read_input_tokens":"<num>","inference_geo":"<str:6>","input_tokens":"<num>","output_tokens":"<num>","service_tier":"<str:8>","speed":"<str:8>"}},"parent_tool_use_id":null,"session_id":"<str:36>","timestamp":"<str:24>","type":"assistant","uuid":"<str:36>"},"kind":"assistant","pipe":"stdout"}`,
		provider.CodexAppServer:   `{"json":{"emittedAtMs":"<num>","method":"item/completed","params":{"completedAtMs":"<num>","contentTokenCount":"<num>","item":{"completedAtMs":"<num>","delivery":null,"id":"<str:54>","memoryCitation":null,"moderation":null,"phase":"<str:12>","questions":null,"startedAtMs":"<num>","text":"<str:67>","type":"agentMessage"},"threadId":"<str:36>","turnId":"<str:36>"}},"kind":"item/completed","pipe":"stdout"}`,
		provider.ACP:              `{"json":{"jsonrpc":"<str:3>","method":"session/update","params":{"sessionId":"<str:36>","update":{"args":{"CommandLine":"<str:6>","WaitMsBeforeAsync":"<num>","toolSummary":"<str:23>"},"kind":"<str:7>","name":"<str:11>","rawInput":{"CommandLine":"<str:6>","WaitMsBeforeAsync":"<num>","toolSummary":"<str:23>"},"sessionUpdate":"tool_call","status":"in_progress","title":"<str:6>","toolCallId":"<str:24>"}}},"kind":"session/update/tool_call","pipe":"stdout"}`,
	} {
		t.Run(string(proto), func(t *testing.T) {
			line := fixtures(t)[proto]
			got := only(t, proto, provider.Stdout, nil, line)
			if got != want {
				t.Errorf("record %s, want %s", got, want)
			}
			placeholder := regexp.MustCompile(`^<(str|arr|obj|key):[0-9]+>$|^<(num|bool)>$`)
			envelope := protocols[proto].tokens
			var leaves func(v any, path string, said bool)
			leaves = func(v any, path string, said bool) {
				switch v := v.(type) {
				case map[string]any:
					for k, x := range v {
						leaves(x, strings.TrimPrefix(path+"."+k, "."), said)
					}
				case []any:
					for _, x := range v {
						leaves(x, path+"[]", said)
					}
				case string:
					if q, _ := json.Marshal(v); said && !envelope[path] && strings.Contains(strings.ReplaceAll(got, string(q)+":", ""), string(q)) {
						t.Errorf("the record keeps %s, of %s", q, path)
					}
					if !said && !placeholder.MatchString(v) && !(envelope[path] && token.MatchString(v)) {
						t.Errorf("the record holds %q, of %s, no placeholder or envelope token", v, path)
					}
				case json.Number:
					if !said && !(path == "id" && integer.MatchString(string(v))) {
						t.Errorf("the record holds the number %s, of %s", v, path)
					}
				}
			}
			for text, said := range map[string]bool{line: true, got: false} {
				d := json.NewDecoder(strings.NewReader(text))
				d.UseNumber()
				var v any
				if err := d.Decode(&v); err != nil {
					t.Fatal(err)
				}
				if !said {
					v = v.(map[string]any)["json"]
				}
				leaves(v, "", said)
			}
		})
	}
}

// A key or token holding a value the thread has held gives way, as does
// a kind that holds one.
func TestAHeldValueGivesWay(t *testing.T) {
	claude, codex := provider.ClaudeStreamJSON, provider.CodexAppServer
	for _, c := range []struct {
		name  string
		proto provider.Protocol
		value string
		line  string
		want  string
	}{
		{"in a key", claude, "sk-1", `{"type":"user","key_sk-1":1}`, `{"json":{"<key:8#1>":"<num>","type":"user"},"kind":"user","pipe":"stdout"}`},
		{"the whole skeleton", codex, `{"id":"fixture-account"}`, `{"id":"fixture-account"}`, `{"pipe":"stdout"}`},
		{"in a token", claude, "sk-1", `{"type":"sk-1"}`, `{"json":{"type":"<str:4>"},"pipe":"stdout"}`},
		{"as an id", codex, "42", `{"id":42,"result":{}}`, `{"json":{"id":"<num>","result":{}},"kind":"response","pipe":"stdout"}`},
		{"as a kind", claude, "assistant", `{"type":"assistant"}`, `{"json":{"type":"<str:9>"},"pipe":"stdout"}`},
		{"in a kind", claude, "message_stop", `{"type":"stream_event","event":{"type":"message_stop"}}`, `{"json":{"event":{"type":"<str:12>"},"type":"stream_event"},"pipe":"stdout"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := only(t, c.proto, provider.Stdout, []string{c.value}, c.line); got != c.want {
				t.Errorf("record %s, want %s", got, c.want)
			}
		})
	}
}

// A line keeps its pipe and length alone where the thread's values aren't
// all known, where the log saw only part of it, and on stderr, JSON or not.
func TestALineKeepsItsLengthAlone(t *testing.T) {
	line := `{"type":"user","message":{"content":"x"}}`
	for _, c := range []struct {
		name string
		pipe provider.Pipe
		size int
		held func() ([]string, bool)
		want string
	}{
		{"of a vault not all read", provider.Stdin, len(line), func() ([]string, bool) { return []string{"y"}, false }, `{"bytes":41,"pipe":"stdin"}`},
		{"seen in part", provider.Stdout, provider.TapCap + 1, nil, `{"bytes":65537,"pipe":"stdout"}`},
		{"on stderr", provider.Stderr, len(line), nil, `{"bytes":41,"pipe":"stderr"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			l, dir := on(t)
			thread := api.NewID()
			l.Write(thread, provider.ClaudeStreamJSON, c.pipe, []byte(line), c.size, c.held)
			l.Flush()
			if rs := records(t, File(dir, thread)); len(rs) != 1 || shown(rs[0]) != c.want {
				t.Errorf("records %v, want %s", rs, c.want)
			}
		})
	}
}

// No record is longer than recordCap: one that would be keeps its pipe,
// kind and length.
func TestARecordStaysUnderItsCap(t *testing.T) {
	var keys []string
	for i := range 2000 {
		keys = append(keys, `"`+strings.Repeat("k", 50)+strconv.Itoa(i)+`":1`)
	}
	line := `{"type":"assistant",` + strings.Join(keys, ",") + "}"
	l, dir := on(t)
	thread := api.NewID()
	write(l, thread, provider.ClaudeStreamJSON, provider.Stdout, line, nil)
	l.Flush()
	b, _ := os.ReadFile(File(dir, thread))
	if rs := records(t, File(dir, thread)); len(b) > 200 || len(rs) != 1 || shown(rs[0]) != `{"bytes":`+strconv.Itoa(len(line))+`,"kind":"assistant","pipe":"stdout"}` {
		t.Errorf("a record of %d bytes, %.200s, want its pipe, kind and length", len(b), b)
	}
}

// The adversarial reviews' leaks, each as a review found it: none of the
// value, or of its parts, reaches the file.
func TestTheReviewsLeaksStayOut(t *testing.T) {
	claude, codex, acp := provider.ClaudeStreamJSON, provider.CodexAppServer, provider.ACP
	stdin, stdout, stderr := provider.Stdin, provider.Stdout, provider.Stderr
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	type line struct {
		proto provider.Protocol
		pipe  provider.Pipe
		text  string
	}
	for _, c := range []struct {
		name, secret, needle string
		cycle                bool // the log goes off and on after the first line
		lines                []line
	}{
		{"short stdin string", "p1n42", "p1n42", false, []line{{claude, stdin, `{"type":"user","message":{"content":"p1n42"}}`}}},
		{"JSON number", "718290641", "718290641", false, []line{{claude, stdout, `{"type":"user","message":{"content":[{"type":"tool_result","content":718290641}]}}`}}},
		{"JSON duplicate key", "dummy-token-8317", "dummy-token-8317", false, []line{{claude, stdout, `{"type":"assistant","message":{"content":"dummy-token-8317","content":"later"}}`}}},
		{"invalid JSON with escapes", "dummy-token-8317", `dummy\\u002dtoken-8317`, false, []line{{claude, stdout, `{"type":"assistant","text":"dummy\u002dtoken-8317`}}},
		{"stderr fragments", "dummy-token-8317", "dummy-token-", false, []line{{claude, stderr, "chunk: dummy-token-\n"}, {claude, stderr, "chunk: 8317\n"}}},
		{"ACP tool updates", "dummy-token-8317", "dummy-token-", false, []line{
			{acp, stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call_update","toolCallId":"t","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":"dummy-token-"}}]}}}`},
			{acp, stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call_update","toolCallId":"t","status":"completed","content":[{"type":"content","content":{"type":"text","text":"8317"}}]}}}`}}},
		{"cut through known value", "dummy-token-8317", "dummy-token-", false, []line{{claude, stdout, `{"type":"assistant","message":{"content":"` + strings.Repeat("x", recordCap-len(`{"type":"assistant","message":{"content":"`)-len("dummy-token-")) + `dummy-token-8317"}}`}}},
		{"kind holds a value", "sk-secret-1", "sk-secret-1", false, []line{{claude, stdout, `{"type":"sk-secret-1"}`}}},
		{"ACP tool call in progress", "sk-secret-1", "MARKER-", false, []line{{acp, stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"tool_call","toolCallId":"t","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":"MARKER-CONTENT"}}],"rawOutput":"MARKER-OUTPUT"}}}`}}},
		{"ACP pending input", "sk-secret-1", "sk-se", false, []line{{acp, stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"tool_call","toolCallId":"t","status":"pending","rawInput":{"command":"echo sk-se"}}}}`}}},
		{"ACP completed update's content", "sk-secret-1", "MARKER-", false, []line{{acp, stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"tool_call_update","toolCallId":"t","status":"completed","rawOutput":"MARKER-OUTPUT"}}}`}}},
		{"unmatched response", "sk-secret-1", "MARKER", false, []line{{acp, stdout, `{"jsonrpc":"2.0","id":99,"result":{"x":"MARKER"}}`}}},
		{"no RPC fields", "sk-secret-1", "MARKER", false, []line{{acp, stdout, `{"jsonrpc":"2.0","note":"MARKER"}`}}},
		{"Codex started output", "sk-secret-1", "MARKER", false, []line{{codex, stdout, `{"method":"item/started","params":{"item":{"id":"i","type":"commandExecution","aggregatedOutput":"MARKER"}}}`}}},
		{"number spelled otherwise", "718290641", "7.18290641e8", false, []line{{claude, stdout, `{"type":"user","message":{"content":[{"type":"tool_result","content":7.18290641e8}]}}`}}},
		{"JSON inside a string", "sk-secret", `sk\\u002dsecret`, false, []line{{claude, stdout, `{"type":"user","message":{"content":"{\"a\":\"sk\\u002dsecret\"}"}}`}}},
		{"name holding the value", "SECRET1", "SECRET1", false, []line{{claude, stdout, `{"type":"user","message":{"content":"SECRET1"}}`}}},
		{"entire JSON object string", `{"credential":"synthetic-doc-936254"}`, "synthetic-doc-936254", false, []line{{claude, stdout, `{"type":"user","message":{"content":` + quote(`{"credential":"synthetic-doc-936254"}`) + "}}"}}},
		{"entire JSON array string", `["synthetic-array-936255"]`, "synthetic-array-936255", false, []line{{claude, stdout, `{"type":"user","message":{"content":` + quote(`["synthetic-array-936255"]`) + "}}"}}},
		{"JSON object as key", `{"credential":"synthetic-key-936256"}`, "synthetic-key-936256", false, []line{{claude, stdout, `{"type":"user","message":{"content":{` + quote(`{"credential":"synthetic-key-936256"}`) + ":true}}}"}}},
		{"escaped inner JSON scalar", "synthetic-scalar-936257", `synthetic\\u002dscalar-936257`, false, []line{{claude, stdout, `{"type":"user","message":{"content":` + quote(`"synthetic\u002dscalar-936257"`) + "}}"}}},
		{"number past the parser's length", "7392841." + strings.Repeat("0", 1100), "7392841", false, []line{{claude, stdout, `{"type":"user","message":{"content":7392841}}`}}},
		{"number past the parser's exponent", "13e1001", "130e1000", false, []line{{claude, stdout, `{"type":"user","message":{"content":130e1000}}`}}},
		{"boolean", "true", `"content":true`, false, []line{{claude, stdout, `{"type":"user","message":{"content":true}}`}}},
		{"known kind equals the value", "session/prompt", `"kind":"session/prompt"`, false, []line{{acp, stdin, `{"method":"session/prompt","id":1,"params":{}}`}}},
		{"stderr with escaped JSON", "synthetic-stderr-936258", `synthetic\\u002dstderr-936258`, false, []line{{acp, stderr, `{"message":"synthetic\u002dstderr-936258"}`}}},
		{"stderr with invalid UTF-8", "synthetic\ufffdstderr-936259", `synthetic\ufffdstderr-936259`, false, []line{{acp, stderr, "synthetic\xffstderr-936259"}}},
		{"response to a request the other way", "sk-secret-1", "unexpected-payload", false, []line{{codex, stdin, `{"method":"thread/started","id":31}`}, {codex, stdout, `{"id":31,"result":{"opaque":"unexpected-payload"}}`}}},
		{"a method named response", "sk-secret-1", "unexpected-payload", false, []line{{codex, stdin, `{"method":"turn/start","id":31}`}, {codex, stdout, `{"method":"response","id":31,"result":{"opaque":"unexpected-payload"}}`}}},
		{"response in another protocol", "sk-secret-1", "unexpected-payload", false, []line{{codex, stdin, `{"method":"turn/start","id":31}`}, {acp, stdout, `{"id":31,"result":{"opaque":"unexpected-payload"}}`}}},
		{"JSON-valued credential as an object", `{"id":"fixture-account"}`, "fixture-account", false, []line{{claude, stdout, `{"type":"user","message":{"content":{"id":"fixture-account"}}}`}}},
		{"JSON-valued credential as the line", `{"id":"fixture-account"}`, "fixture-account", false, []line{{codex, stdout, `{"id":"fixture-account"}`}}},
		{"an id and status in tool data", "sk-secret-1", "tool-data-77", false, []line{{codex, stdout, `{"method":"item/completed","params":{"item":{"type":"mcpToolCall","status":"completed","result":{"id":"tool-data-77","status":"tool-data-77"}}}}`}}},
		{"a number id in tool data", "sk-secret-1", "8675309", false, []line{{acp, stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"tool_call","rawInput":{"id":8675309}}}}`}}},
		{"response to a request before off", "sk-secret-1", "unexpected-payload", true, []line{{codex, stdin, `{"method":"turn/start","id":31}`}, {codex, stdout, `{"id":31,"result":{"opaque":"unexpected-payload"}}`}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			l, dir := on(t)
			thread := api.NewID()
			for i, line := range c.lines {
				write(l, thread, line.proto, line.pipe, line.text, held(c.secret))
				if c.cycle && i == 0 {
					l.Flush()
					l.SetOn(false)
					l.SetOn(true)
				}
			}
			l.Flush()
			b, err := os.ReadFile(File(dir, thread))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(b, []byte(c.needle)) {
				t.Errorf("the log holds %q:\n%.300s", c.needle, b)
			}
		})
	}
}

// parked writes line for thread, its values held back until the
// returned func is called, and returns once the log has taken the line,
// and that func, which returns once Write has.
func parked(l *Log, thread api.ID, line string) (release func(), asked *bool) {
	took, free, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	asked = new(bool)
	go func() {
		defer close(done)
		write(l, thread, provider.ClaudeStreamJSON, provider.Stderr, line, func() ([]string, bool) {
			*asked = true
			close(took)
			<-free
			return nil, true
		})
	}()
	select {
	case <-took:
	case <-done:
	}
	return func() { close(free); <-done }, asked
}

// A line the log took before it went off, or before its thread was
// forgotten, is never written, however long its values took: nor is
// the count of lines like it a full queue dropped. A forgotten thread's
// line isn't taken at all, so its values aren't asked for.
func TestALineTakenBeforeACutoffIsNeverWritten(t *testing.T) {
	l, dir := on(t)
	offed, forgotten, kept := api.NewID(), api.NewID(), api.NewID()
	release, _ := parked(l, offed, "before off")
	l.SetOn(false)
	l.SetOn(true)
	release()
	release, _ = parked(l, forgotten, "before forget")
	if err := l.Forget(forgotten); err != nil {
		t.Fatal(err)
	}
	release()
	release, asked := parked(l, forgotten, "after forget")
	release()
	if *asked {
		t.Error("the log asked a forgotten thread's values")
	}
	diag(l, kept, "kept")
	l.Flush()
	if entries, _ := os.ReadDir(dir); len(entries) != 1 || entries[0].Name() != name(kept, 0) {
		t.Errorf("the log holds %v, want only the file of the line taken after the cutoffs", entries)
	}
}

// With the queue full, a line taken before a cutoff is counted as
// dropped where it was taken, and that count is never written; a count
// of lines taken since is.
func TestAFullQueueRacingTheCutoffsWritesNoStaleCount(t *testing.T) {
	state := state(t)
	l, err := newLog(state)
	if err != nil {
		t.Fatal(err)
	}
	l.SetOn(true)
	offed, forgotten, later := api.NewID(), api.NewID(), api.NewID()
	for range queueCap {
		diag(l, offed, "queued")
	}
	beforeOff, _ := parked(l, offed, "before off")
	beforeForget, _ := parked(l, forgotten, "before forget")
	l.SetOn(false)
	l.SetOn(true)
	if err := l.Forget(forgotten); err != nil {
		t.Fatal(err)
	}
	beforeOff()
	beforeForget()
	diag(l, later, "dropped too")
	go l.run()
	defer l.Close()
	l.Flush()
	dir := Under(state)
	if entries, _ := os.ReadDir(dir); len(entries) != 1 || entries[0].Name() != name(later, 0) {
		t.Fatalf("the log holds %v, want only the file of the line dropped after the cutoffs", entries)
	}
	if rs := records(t, File(dir, later)); len(rs) != 1 || shown(rs[0]) != `{"dropped":1}` {
		t.Errorf("records %v, want one count of 1 dropped", rs)
	}
}

// A full queue turns lines away, and the log says how many once the
// writer has caught up.
func TestAFullQueueCountsWhatItDrops(t *testing.T) {
	state := state(t)
	l, err := newLog(state)
	if err != nil {
		t.Fatal(err)
	}
	l.SetOn(true)
	thread := api.NewID()
	for range queueCap + 5 {
		diag(l, thread, "busy")
	}
	go l.run()
	defer l.Close()
	l.Flush()
	rs := records(t, File(Under(state), thread))
	if len(rs) != queueCap+1 || shown(rs[queueCap]) != `{"dropped":5}` {
		t.Fatalf("%d records, the last %v, want %d and a count of 5 dropped", len(rs), rs[len(rs)-1], queueCap)
	}
}

// A thread's file rotates once it would pass fileCap, or once its first
// record is older than segmentAge, keeping rotations files besides.
func TestAThreadsFileRotates(t *testing.T) {
	l, dir := on(t)
	l.fileCap = 1000
	thread := api.NewID()
	line := "x" + strings.Repeat("y", 300)
	for range 50 {
		diag(l, thread, line)
	}
	l.Flush()
	for n := range rotations + 1 {
		fi, err := os.Stat(filepath.Join(dir, name(thread, n)))
		if err != nil || fi.Size() == 0 || fi.Size() > l.fileCap {
			t.Errorf("rotation %d: %v, want a file of at most %d bytes", n, err, l.fileCap)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != rotations+1 {
		t.Errorf("the log holds %d files, want %d", len(entries), rotations+1)
	}

	other := api.NewID()
	diag(l, other, "the first day")
	l.Flush()
	l.mu.Lock()
	l.files[name(other, 0)].first = time.Now().Add(-segmentAge - time.Minute)
	l.mu.Unlock()
	diag(l, other, "day two")
	l.Flush()
	if rs := records(t, File(dir, other)); len(rs) != 1 || rs[0]["bytes"] != json.Number("7") {
		t.Errorf("the current file holds %v, want day two's record alone", rs)
	}
	if rs := records(t, filepath.Join(dir, name(other, 1))); len(rs) != 1 || rs[0]["bytes"] != json.Number("13") {
		t.Errorf("the rotated file holds %v, want day one's record", rs)
	}
}

// No append takes the log past totalCap, however many threads share it:
// the oldest files go first.
func TestTheTotalCapHoldsOnEveryAppend(t *testing.T) {
	l, dir := on(t)
	l.totalCap = 300
	var threads []api.ID
	for range 6 {
		thread := api.NewID()
		threads = append(threads, thread)
		diag(l, thread, "ordinary diagnostic")
		l.Flush()
	}
	entries, _ := os.ReadDir(dir)
	var total int64
	for _, e := range entries {
		fi, _ := e.Info()
		total += fi.Size()
	}
	if total > l.totalCap {
		t.Errorf("the log holds %d bytes, past its cap of %d", total, l.totalCap)
	}
	if _, err := os.Stat(File(dir, threads[0])); err == nil {
		t.Error("the oldest thread's file stayed")
	}
	if _, err := os.Stat(File(dir, threads[5])); err != nil {
		t.Errorf("the newest thread's file: %v", err)
	}
}

// stamped is a record of a stderr line of text written at at, as the
// log writes one.
func stamped(at time.Time, text string) []byte {
	return marshal(record{Time: at, Pipe: provider.Stderr, Bytes: len(text)})
}

// No record outlives maxAge: a file goes once its first record would be
// older than maxAge by the next sweep, however new its last is, when the
// log opens, and at the hourly sweep while it is off too.
func TestNoRecordOutlivesMaxAge(t *testing.T) {
	state := state(t)
	dir := Under(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	mixed, closeToIt, young, later := api.NewID(), api.NewID(), api.NewID(), api.NewID()
	for thread, first := range map[api.ID]time.Time{
		mixed:     now.Add(-maxAge - time.Hour),
		closeToIt: now.Add(-maxAge + sweepEvery/2),
		young:     now.Add(-maxAge + 2*sweepEvery),
		later:     now.Add(-maxAge + 2*sweepEvery),
	} {
		if err := os.WriteFile(File(dir, thread), append(stamped(first, "first"), stamped(now, "last")...), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for thread, stays := range map[api.ID]bool{mixed: false, closeToIt: false, young: true, later: true} {
		if _, err := os.Stat(File(dir, thread)); (err == nil) != stays {
			t.Errorf("a file whose first record is %v old: %v, want it kept %v", map[api.ID]string{mixed: "past maxAge", closeToIt: "within a sweep of maxAge", young: "two sweeps short of maxAge", later: "two sweeps short of maxAge"}[thread], err, stays)
		}
	}
	l.sweep(now.Add(2 * sweepEvery))
	if _, err := os.Stat(File(dir, later)); err == nil {
		t.Error("the sweep left a file whose first record is past maxAge while the log is off")
	}
}

// The log keeps to logs/providers of its own state directory: where
// either is a link, as to another engine's, or no folder, it opens no
// log, and changes nothing there.
func TestTheLogKeepsToItsOwnFolder(t *testing.T) {
	if !Supported {
		t.Skip("this system keeps no provider log")
	}
	link := func(other, state string) error {
		return os.Symlink(filepath.Join(other, "logs"), filepath.Join(state, "logs"))
	}
	for _, c := range []struct {
		name   string
		inside bool // the other engine's state directory is in this one's
		alias  func(other, state string) error
	}{
		{"logs a link", false, link},
		{"logs a link within the state directory", true, func(other, state string) error {
			return os.Symlink(filepath.Join("other", "logs"), filepath.Join(state, "logs"))
		}},
		{"providers a link", false, func(other, state string) error {
			os.Mkdir(filepath.Join(state, "logs"), 0o700)
			return os.Symlink(Under(other), Under(state))
		}},
		{"logs a file", false, func(other, state string) error { return os.WriteFile(filepath.Join(state, "logs"), nil, 0o600) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			other, state := t.TempDir(), t.TempDir()
			if c.inside {
				other = filepath.Join(state, "other")
			}
			if err := os.MkdirAll(Under(other), 0o755); err != nil {
				t.Fatal(err)
			}
			thread := api.NewID()
			old := stamped(time.Now().Add(-2*maxAge), "the other engine's")
			if err := os.WriteFile(File(Under(other), thread), old, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := c.alias(other, state); err != nil {
				t.Fatal(err)
			}
			if l, err := Open(state); err == nil {
				l.Close()
				t.Fatal("the log opened")
			}
			b, err := os.ReadFile(File(Under(other), thread))
			fi, _ := os.Stat(Under(other))
			if err != nil || !bytes.Equal(b, old) || fi.Mode().Perm() != 0o755 {
				t.Errorf("the other engine's log changed: %v", err)
			}
		})
	}
}

// Forget deletes every file of the thread, and its later lines are kept
// nowhere.
func TestForgetDeletesTheThreadsFiles(t *testing.T) {
	l, dir := on(t)
	l.fileCap = 100
	thread, other := api.NewID(), api.NewID()
	for range 5 {
		diag(l, thread, strings.Repeat("z", 60))
	}
	diag(l, other, "other")
	l.Flush()
	if err := l.Forget(thread); err != nil {
		t.Fatal(err)
	}
	diag(l, thread, "late")
	l.Flush()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != name(other, 0) {
		t.Errorf("the log holds %v, want only the other thread's file", entries)
	}
	if got := l.Threads(); len(got) != 1 || got[0] != other {
		t.Errorf("the log holds files of %v, want %v", got, other)
	}
}

// What was queued before the log was turned off, or its thread
// forgotten, is never written, and what comes after neither.
func TestOffAndForgetCutOffWhatIsQueued(t *testing.T) {
	state := state(t)
	l, err := newLog(state)
	if err != nil {
		t.Fatal(err)
	}
	l.SetOn(true)
	kept, forgotten, off := api.NewID(), api.NewID(), api.NewID()
	diag(l, forgotten, "queued")
	if err := l.Forget(forgotten); err != nil {
		t.Fatal(err)
	}
	diag(l, forgotten, "later")
	diag(l, off, "queued")
	l.SetOn(false)
	l.SetOn(true)
	diag(l, kept, "kept")
	go l.run()
	defer l.Close()
	l.Flush()
	if entries, _ := os.ReadDir(Under(state)); len(entries) != 1 || entries[0].Name() != name(kept, 0) {
		t.Errorf("the log holds %v, want only the file of the thread written after the log went off and on", entries)
	}
}

// An append keeps to the caps as the file is on disk, however it grew
// since the log last wrote it.
func TestAnAppendKeepsToTheCapsAsTheFileIs(t *testing.T) {
	l, dir := on(t)
	l.totalCap, l.fileCap = 256, 256
	thread := api.NewID()
	diag(l, thread, "first")
	l.Flush()
	path := File(dir, thread)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(stamped(time.Now(), "first"), bytes.Repeat([]byte("\n"), int(l.totalCap-fi.Size()-12))...), 0o600); err != nil {
		t.Fatal(err)
	}
	diag(l, thread, "second")
	l.Flush()
	var total int64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		fi, _ := e.Info()
		total += fi.Size()
	}
	if total > l.totalCap {
		t.Errorf("the log holds %d bytes, past its cap of %d", total, l.totalCap)
	}
}

// A file gone from the folder, as when someone deleted it there, leaves
// the log's count, and the log writes on.
func TestAFileGoneFromTheFolderLeavesTheCount(t *testing.T) {
	l, dir := on(t)
	l.fileCap = 200
	thread := api.NewID()
	for range 3 {
		diag(l, thread, strings.Repeat("z", 60))
	}
	l.Flush()
	for n := range rotations + 1 {
		os.Remove(filepath.Join(dir, name(thread, n)))
	}
	for range 3 {
		diag(l, thread, strings.Repeat("y", 60))
	}
	l.Flush()
	if rs := records(t, File(dir, thread)); len(rs) == 0 {
		t.Error("the log wrote nothing after its files went")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var total int64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		fi, _ := e.Info()
		total += fi.Size()
	}
	if l.total != total {
		t.Errorf("the log counts %d bytes, its folder holds %d", l.total, total)
	}
}

// The log touches nothing of its state directory but logs/providers:
// not the skills staged for runs, nor checkpoints, nor a file in logs or
// the state directory named as one of its own, however old, through its
// startup prune, writes, rotations, sweeps and Forget.
func TestTheLogTouchesNothingOutsideItsFolder(t *testing.T) {
	state := state(t)
	thread := api.NewID()
	old := stamped(time.Now().Add(-2*maxAge), "old")
	for _, file := range []string{
		filepath.Join("run-skills", "set", "skills", "mine", "SKILL.md"),
		filepath.Join("checkpoints", thread.String()),
		filepath.Join("logs", name(thread, 0)),
		filepath.Join("logs", name(thread, 1)),
		name(thread, 0),
	} {
		path := filepath.Join(state, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, old, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outside := func() map[string]string {
		files := map[string]string{}
		filepath.WalkDir(state, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(state, path)
			if rel == filepath.Join("logs", "providers") {
				return filepath.SkipDir
			}
			fi, _ := d.Info()
			b, _ := os.ReadFile(path)
			files[rel] = fmt.Sprintf("%v %q", fi.Mode(), b)
			return nil
		})
		return files
	}
	before := outside()
	l, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	l.SetOn(true)
	l.fileCap = 200
	for range 10 {
		diag(l, thread, strings.Repeat("z", 60))
	}
	l.Flush()
	l.sweep(time.Now().Add(2 * maxAge))
	if err := l.Forget(thread); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if after := outside(); !maps.Equal(before, after) {
		t.Errorf("outside logs/providers, the state directory went from\n%v\nto\n%v", before, after)
	}
}
