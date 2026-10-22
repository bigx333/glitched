package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"glitched/internal/api"
	"glitched/internal/hub"
)

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
	code   string
	err    error
	status int
}{
	{"conflict", api.ErrConflict, http.StatusConflict},
	{"revision", api.ErrRevision, http.StatusConflict},
	{"expired", api.ErrExpired, http.StatusGone},
	{"not_found", api.ErrNotFound, http.StatusNotFound},
	{"stale_epoch", api.ErrStaleEpoch, http.StatusConflict},
	{"already_answered", api.ErrAlreadyAnswered, http.StatusConflict},
	{"foreign_log", hub.ErrForeignLog, http.StatusConflict},
}

func toWire(err error) (wireError, int) {
	we := wireError{Message: err.Error()}
	var moved *api.MovedError
	var rejected *api.RejectedError
	var gap *hub.GapError
	switch {
	case errors.As(err, &moved):
		we.Code, we.To, we.Epoch = "moved", moved.To, moved.Epoch
		return we, http.StatusMisdirectedRequest
	case errors.As(err, &rejected):
		we.Code, we.Reason = "rejected", rejected.Reason
		return we, http.StatusUnprocessableEntity
	case errors.As(err, &gap):
		we.Code, we.Acked = "gap", gap.Acked
		return we, http.StatusConflict
	}
	for _, s := range sentinels {
		if errors.Is(err, s.err) {
			we.Code = s.code
			return we, s.status
		}
	}
	we.Code = "internal"
	return we, http.StatusInternalServerError
}

func (we wireError) err() error {
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

func writeError(w http.ResponseWriter, err error) {
	we, status := toWire(err)
	writeJSON(w, status, we)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// readError turns a non-200 response into the error the server sent.
func readError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var we wireError
	if json.Unmarshal(body, &we) != nil || we.Code == "" {
		return fmt.Errorf("transport: %s: %s", resp.Status, body)
	}
	return we.err()
}

// frame is one NDJSON line of a stream: an item, or the error that ended it.
type frame[T any] struct {
	Item  *T         `json:"item,omitempty"`
	Error *wireError `json:"error,omitempty"`
}
