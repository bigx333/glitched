package acp

import (
	"bytes"
	"encoding/json"
	"slices"
	"sync"
)

const legacyModelConfigID = "glitched-legacy-model"

// legacy bridges agents that list a session's models in its "models"
// field and switch them with session/set_model, as Gemini CLI 0.71.1 and
// older OpenCode do, to the SDK, which knows only config options. It
// edits the lines between the agent and the SDK: the agent's session
// answers gain a model option, and a set of that option goes out as
// session/set_model. The wire capture and the tap keep the lines the
// agent really sent and got.
type legacy struct {
	mu             sync.Mutex
	openingByReqID map[string]string
	settingByReqID map[string]legacySet
	sessions       map[string]*legacySession
}

type legacySet struct{ session, model string }

type legacySession struct {
	current string
	models  []json.RawMessage
	others  []json.RawMessage
}

type legacyModel struct {
	ModelID     string  `json:"modelId"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

type rpc struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func canonicalID(id json.RawMessage) string {
	var v any
	if json.Unmarshal(id, &v) != nil {
		return string(id)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func mayEditToAgent(line []byte) bool {
	return bytes.Contains(line, []byte(`"session/new"`)) ||
		bytes.Contains(line, []byte(`"session/load"`)) ||
		bytes.Contains(line, []byte(`"session/resume"`)) ||
		bytes.Contains(line, []byte(legacyModelConfigID))
}

func mayEditFromAgent(line []byte) bool {
	return bytes.Contains(line, []byte(`"result"`)) ||
		bytes.Contains(line, []byte(`"error"`)) ||
		bytes.Contains(line, []byte(`config_option_update`))
}

func (l *legacy) toAgent(line []byte) []byte {
	if !mayEditToAgent(line) {
		return line
	}
	var m rpc
	if json.Unmarshal(line, &m) != nil || m.Method == "" || len(m.ID) == 0 {
		return line
	}
	switch m.Method {
	case "session/new", "session/load", "session/resume":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		json.Unmarshal(m.Params, &p)
		l.mu.Lock()
		if l.openingByReqID == nil {
			l.openingByReqID = map[string]string{}
		}
		l.openingByReqID[canonicalID(m.ID)] = p.SessionID
		l.mu.Unlock()
	case "session/set_config_option":
		var p struct {
			SessionID string `json:"sessionId"`
			ConfigID  string `json:"configId"`
			Value     string `json:"value"`
		}
		if json.Unmarshal(m.Params, &p) != nil || p.ConfigID != legacyModelConfigID {
			return line
		}
		params, _ := json.Marshal(map[string]string{"sessionId": p.SessionID, "modelId": p.Value})
		out, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "method": "session/set_model", "params": json.RawMessage(params)})
		if err != nil {
			return line
		}
		l.mu.Lock()
		if l.settingByReqID == nil {
			l.settingByReqID = map[string]legacySet{}
		}
		l.settingByReqID[canonicalID(m.ID)] = legacySet{p.SessionID, p.Value}
		l.mu.Unlock()
		return append(out, '\n')
	}
	return line
}

func (l *legacy) fromAgent(line []byte) []byte {
	if !mayEditFromAgent(line) {
		return line
	}
	var m rpc
	if json.Unmarshal(line, &m) != nil {
		return line
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if m.Method == "session/update" {
		return l.updatedLocked(line, m.Params)
	}
	if m.Method != "" || len(m.ID) == 0 {
		return line
	}
	key := canonicalID(m.ID)
	if session, ok := l.openingByReqID[key]; ok {
		delete(l.openingByReqID, key)
		return l.openedLocked(line, session, m.Result)
	}
	if set, ok := l.settingByReqID[key]; ok {
		delete(l.settingByReqID, key)
		s := l.sessions[set.session]
		if len(m.Error) > 0 || len(m.Result) == 0 || s == nil {
			return line
		}
		s.current = set.model
		return answer(line, map[string]json.RawMessage{"configOptions": s.options()})
	}
	return line
}

func (l *legacy) openedLocked(line []byte, session string, raw json.RawMessage) []byte {
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return line
	}
	var models struct {
		CurrentModelID  string            `json:"currentModelId"`
		AvailableModels []json.RawMessage `json:"availableModels"`
	}
	if json.Unmarshal(result["models"], &models) != nil || len(models.AvailableModels) == 0 {
		return line
	}
	var options []json.RawMessage
	json.Unmarshal(result["configOptions"], &options)
	if slices.ContainsFunc(options, func(o json.RawMessage) bool {
		var c struct{ Category string }
		json.Unmarshal(o, &c)
		return c.Category == "model"
	}) {
		return line
	}
	if session == "" {
		json.Unmarshal(result["sessionId"], &session)
	}
	if session == "" {
		return line
	}
	s := &legacySession{current: models.CurrentModelID, models: models.AvailableModels, others: options}
	if l.sessions == nil {
		l.sessions = map[string]*legacySession{}
	}
	l.sessions[session] = s
	b, _ := json.Marshal(s.options())
	result["configOptions"] = b
	return answer(line, result)
}

func (l *legacy) updatedLocked(line []byte, raw json.RawMessage) []byte {
	var n struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind          string            `json:"sessionUpdate"`
			ConfigOptions []json.RawMessage `json:"configOptions"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &n) != nil || n.Update.Kind != "config_option_update" {
		return line
	}
	s := l.sessions[n.SessionID]
	if s == nil {
		return line
	}
	s.others = slices.DeleteFunc(n.Update.ConfigOptions, func(o json.RawMessage) bool {
		var c struct{ ID string }
		json.Unmarshal(o, &c)
		return c.ID == legacyModelConfigID
	})
	var m map[string]json.RawMessage
	var params, update map[string]json.RawMessage
	if json.Unmarshal(line, &m) != nil || json.Unmarshal(m["params"], &params) != nil || json.Unmarshal(params["update"], &update) != nil {
		return line
	}
	update["configOptions"] = s.options()
	params["update"], _ = json.Marshal(update)
	m["params"], _ = json.Marshal(params)
	out, err := json.Marshal(m)
	if err != nil {
		return line
	}
	return append(out, '\n')
}

func (s *legacySession) options() json.RawMessage {
	choices := []map[string]any{}
	for _, raw := range s.models {
		var m legacyModel
		if json.Unmarshal(raw, &m) != nil || m.ModelID == "" {
			continue
		}
		c := map[string]any{"value": m.ModelID, "name": m.Name}
		if m.Name == "" {
			c["name"] = m.ModelID
		}
		if m.Description != nil {
			c["description"] = *m.Description
		}
		choices = append(choices, c)
	}
	model, _ := json.Marshal(map[string]any{
		"id": legacyModelConfigID, "name": "Model", "category": "model", "type": "select",
		"currentValue": s.current, "options": choices,
	})
	b, _ := json.Marshal(append(slices.Clone(s.others), model))
	return b
}

func answer(line []byte, result any) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(line, &m) != nil {
		return line
	}
	b, err := json.Marshal(result)
	if err != nil {
		return line
	}
	m["result"] = b
	out, err := json.Marshal(m)
	if err != nil {
		return line
	}
	if bytes.HasSuffix(line, []byte("\n")) {
		out = append(out, '\n')
	}
	return out
}
