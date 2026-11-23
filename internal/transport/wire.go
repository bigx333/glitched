// Package transport carries the Endpoint and hub calls over one WebSocket per
// client, on whatever network a Transport provides.
//
// The upgrade's request and response each carry Glitched-Protocol, the
// side's api.Protocol; a side that sends none speaks protocol 1. The server
// closes the socket of a client of another protocol at once with 4001 and a
// reason that says so. The client refuses a server of another protocol with
// an api.ProtocolError before it sends a call: at that close, or once the
// server has proved its key, if it must.
//
// On a Tailnet socket the client and server first prove to each other that
// they hold the key, the SHA-256 of the client's pairing token, without
// sending it or the token. The upgrade carries no credential. Then, with
// nonces of 32 random bytes and HMAC-SHA256 under the key:
//
//	server: {"nonce":nS}
//	client: {"nonce":nC,"mac":HMAC("glitched-client\x00" || nS || nC)}
//	server: {"mac":HMAC("glitched-server\x00" || nC || nS)}
//
// Byte fields are base64. The server tries the client's MAC against every
// key Server.Authorize knows and closes the socket with 1008, policy
// violation, when none matches. Any server can send 1008, so the client
// reports it as ErrRefused, not as a wrong token. The client checks the
// server's MAC before it sends a call, and drops a server whose MAC is wrong
// with ErrNotTheServer. Each side gives the other 5s, and the server reads
// no message over 1 KiB until the client has proved its key. A unix socket
// skips the handshake: its file mode is the gate.
//
// Then the client sends text messages, one JSON object each:
//
//	{"id":1,"method":"submit","params":{...}}  start a call or a stream
//	{"id":1,"cancel":true}                    abandon a call, or end a stream
//	{"id":1,"ack":1048576}                    the reader took this many bytes of items
//
// The server answers with messages carrying the request's id:
//
//	{"id":1,"result":{...}}  a call's answer
//	{"id":1,"open":true}     a stream is open; its items follow
//	{"id":1,"item":{...}}    one stream item
//	{"id":1,"error":{...}}   why a call or an open failed, or why a stream ended
//
// Ids are the client's and unique per socket. Calls and streams interleave
// freely. A stream ends only with an error, because nothing upstream ends
// cleanly. The server closes a socket that sends a message over 64 MiB, so
// the client refuses to send one.
//
// The server reads a stream's source only while fewer than 4 MiB of the
// items it sent are unacked, counting each item's JSON. The next item goes
// out whatever its size, so a stream holds at most the window plus one item
// on the client, and an item larger than the window is never stuck. A
// reader that falls behind thus holds its source back, and the source's own
// bound on a lagging reader applies; the client never drops an item.
//
// Each side pings every 15s and drops a socket whose pong is 10s late.
package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/coder/websocket"

	"glitched/internal/api"
	"glitched/internal/hub"
	"glitched/internal/provider"
)

const protocolHeader = "Glitched-Protocol"

const statusProtocol websocket.StatusCode = 4001

// protocolOf is the protocol a peer's upgrade announces.
func protocolOf(h http.Header) int {
	v := h.Get(protocolHeader)
	if v == "" {
		return 1
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

type request struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Cancel bool            `json:"cancel,omitempty"`
	Ack    int64           `json:"ack,omitempty"`
}

// The uploader caps a batch at 8 MiB; this leaves room for JSON overhead.
const maxMessage = 64 << 20

var errTooLarge = errors.New("transport: request over the server's 64 MiB limit")

// streamWindow is how many bytes of a stream's items the server sends ahead
// of the client's acks.
const streamWindow = 4 << 20

type response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Open   bool            `json:"open,omitempty"`
	Item   json.RawMessage `json:"item,omitempty"`
	Error  *wireError      `json:"error,omitempty"`
}

// wireError is an error as it crosses the wire. Code names the error kind a
// caller branches on; the other fields carry the typed errors' values.
type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	To      api.ID `json:"to,omitzero"`
	Epoch   uint64 `json:"epoch,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Acked   uint64 `json:"acked,omitempty"`
}

var sentinels = []struct {
	code string
	err  error
}{
	{"conflict", api.ErrConflict},
	{"revision", api.ErrRevision},
	{"expired", api.ErrExpired},
	{"not_found", api.ErrNotFound},
	{"stale_epoch", api.ErrStaleEpoch},
	{"already_answered", api.ErrAlreadyAnswered},
	{"overflow", api.ErrOverflow},
	{"protocol", api.ErrProtocol},
	{"foreign_log", hub.ErrForeignLog},
	{"rewound", hub.ErrRewound},
	{"unsupported", provider.ErrUnsupported},
	{"unavailable", provider.ErrUnavailable},
}

func toWire(err error) *wireError {
	we := &wireError{Message: err.Error()}
	var moved *api.MovedError
	var rejected *api.RejectedError
	var gap *hub.GapError
	switch {
	case errors.As(err, &moved):
		we.Code, we.To, we.Epoch = "moved", moved.To, moved.Epoch
		return we
	case errors.As(err, &rejected):
		we.Code, we.Reason = "rejected", rejected.Reason
		return we
	case errors.As(err, &gap):
		we.Code, we.Acked = "gap", gap.Acked
		return we
	}
	for _, s := range sentinels {
		if errors.Is(err, s.err) {
			we.Code = s.code
			return we
		}
	}
	we.Code = "internal"
	return we
}

func (we *wireError) err() error {
	switch we.Code {
	case "moved":
		return &api.MovedError{To: we.To, Epoch: we.Epoch}
	case "rejected":
		return &api.RejectedError{Reason: we.Reason}
	case "gap":
		return &hub.GapError{Acked: we.Acked}
	}
	for _, s := range sentinels {
		if we.Code == s.code {
			return s.err
		}
	}
	return fmt.Errorf("transport: remote %s: %s", we.Code, we.Message)
}
