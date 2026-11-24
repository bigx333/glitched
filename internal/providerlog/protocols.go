package providerlog

import (
	"maps"

	"glitched/internal/provider"
)

// A protocol is how the log tells the kind of a message in one of the
// protocols adapters speak, and the paths of its envelope's identifier
// tokens, which a skeleton keeps. A kind its table doesn't name is no kind
// the log records.
type protocol struct {
	kind   func(m map[string]any) string
	kinds  map[string]bool
	tokens map[string]bool
}

// envelope is what every message's skeleton keeps tokens at.
var envelope = set("type", "subtype", "method", "id")

var protocols = map[provider.Protocol]protocol{
	provider.ClaudeStreamJSON: {claudeKind, set(
		"user", "assistant", "result", "control_request", "control_response",
		"system/init", "system/status", "system/compact_boundary",
		"stream_event/message_start", "stream_event/content_block_start", "stream_event/content_block_delta",
		"stream_event/content_block_stop", "stream_event/message_delta", "stream_event/message_stop",
	), with(envelope, "message.role", "message.stop_reason", "message.content[].type")},
	provider.CodexAppServer: {rpcKind, set(
		"response", "initialize", "initialized", "account/read", "config/read", "model/list",
		"thread/start", "thread/resume", "turn/start", "turn/steer", "turn/interrupt",
		"thread/started", "turn/started", "turn/completed", "item/started", "item/completed", "error",
		"thread/compacted", "thread/status/changed", "thread/settings/updated", "thread/tokenUsage/updated",
		"mcpServer/startupStatus/updated", "serverRequest/resolved",
		"item/agentMessage/delta", "item/reasoning/textDelta", "item/reasoning/summaryTextDelta",
		"item/commandExecution/outputDelta", "item/fileChange/outputDelta", "command/exec/outputDelta",
		"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval",
		"item/tool/requestUserInput", "mcpServer/elicitation/request",
	), with(envelope, "params.item.type", "params.item.status", "params.turn.status")},
	provider.ACP: {rpcKind, set(
		"response", "initialize", "authenticate", "session/new", "session/load", "session/resume",
		"session/prompt", "session/set_mode", "session/set_config_option", "session/cancel",
		"session/request_permission", "fs/read_text_file", "fs/write_text_file",
		"terminal/create", "terminal/output", "terminal/release", "terminal/wait_for_exit", "terminal/kill",
		"session/update/tool_call", "session/update/tool_call_update", "session/update/plan",
		"session/update/available_commands_update", "session/update/current_mode_update",
		"session/update/agent_message_chunk", "session/update/agent_thought_chunk", "session/update/user_message_chunk",
		"session/update/config_option_update", "session/update/usage_update", "session/update/session_info_update",
	), with(envelope, "params.update.sessionUpdate", "params.update.status")},
}

func set(kinds ...string) map[string]bool {
	m := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		m[k] = true
	}
	return m
}

// with is s and more.
func with(s map[string]bool, more ...string) map[string]bool {
	m := maps.Clone(s)
	for _, k := range more {
		m[k] = true
	}
	return m
}

func str(m map[string]any, key string) string { s, _ := m[key].(string); return s }

func obj(m map[string]any, key string) map[string]any { o, _ := m[key].(map[string]any); return o }

func claudeKind(m map[string]any) string {
	switch t := str(m, "type"); t {
	case "system":
		return t + "/" + str(m, "subtype")
	case "stream_event":
		return t + "/" + str(obj(m, "event"), "type")
	default:
		return t
	}
}

// rpcKind is a JSON-RPC message's kind: its method, an ACP session/update
// by what it updates, or a response.
func rpcKind(m map[string]any) string {
	_, result := m["result"]
	_, failed := m["error"]
	switch method := str(m, "method"); {
	case method == "session/update":
		return method + "/" + str(obj(obj(m, "params"), "update"), "sessionUpdate")
	case method != "":
		return method
	case m["id"] != nil && (result || failed):
		return "response"
	}
	return ""
}
