package mcp

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"glitched/internal/api"
)

// Asker is the engine as request_credentials reaches it.
type Asker interface {
	AskCredentials(ctx context.Context, a api.Ask) (api.Asked, error)
}

const requestCredentials = "request_credentials"

var credentialsTool = map[string]any{
	"name": requestCredentials,
	"description": "Ask the user for secrets (API keys, tokens, passwords) in a private card, instead of asking in chat. " +
		"Batch every value you know you need into one call. The call waits until the user answers. " +
		"You never see the values: each is saved in a private file, and an env script exports them all. " +
		"Never put a value in a command line, a file you write, a log or a reply, and never print the files or the environment.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reason": map[string]any{"type": "string", "minLength": 1, "maxLength": 2000, "description": "What the values are for and where the user gets them, in a sentence or two."},
			"fields": map[string]any{
				"type": "array", "minItems": 1, "maxItems": api.MaxCredentials,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":  map[string]any{"type": "string", "pattern": "^[A-Za-z_][A-Za-z0-9_]*$", "description": "The environment variable the value is given as, such as OPENAI_API_KEY."},
						"label": map[string]any{"type": "string", "maxLength": 120, "description": "What the card calls it, such as \"OpenAI API key\"."},
						"url":   map[string]any{"type": "string", "description": "An https page where the user gets the value."},
					},
					"required":             []string{"name"},
					"additionalProperties": false,
				},
			},
			"env_file":          map[string]any{"type": "string", "description": "Also write the values to this dotenv file in the thread's folder, such as .env.local, for an app that reads it. Git must ignore it."},
			"client_request_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "description": "Reuse it to retry the same request; a new request needs a new one."},
		},
		"required":             []string{"reason", "fields"},
		"additionalProperties": false,
	},
	"_meta": map[string]any{"anthropic/alwaysLoad": true},
}

type credentialsArgs struct {
	Reason    string      `json:"reason"`
	Fields    []api.Field `json:"fields"`
	EnvFile   string      `json:"env_file"`
	RequestID string      `json:"client_request_id"`
}

// credentialsResult is what the agent reads of an answer.
type credentialsResult struct {
	api.Asked
	Note string `json:"note"`
}

// credentials asks the user for the values the call names, for the
// origin's turn, and returns where they are once given.
func (t *Tools) credentials(ctx context.Context, raw json.RawMessage, toolUse string) (credentialsResult, error) {
	if t.isChild(ctx) {
		return credentialsResult{}, errors.New(requestCredentials + ": this thread is a child, which never asks the user for secrets itself. Report the names of the secrets you need and why to your parent as you end your turn; it vets the request, asks the user, and passes you the individual file paths for the approved secrets")
	}
	if t.Secrets == nil {
		return credentialsResult{}, errors.New(requestCredentials + ": this engine keeps no credentials")
	}
	var in credentialsArgs
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return credentialsResult{}, errors.New(requestCredentials + ": " + err.Error())
	}
	asked, err := t.Secrets.AskCredentials(ctx, api.Ask{
		Thread: t.Origin.Thread,
		Access: t.Origin.Access,
		Call:   cmp.Or(in.RequestID, toolUse, api.NewID().String()),
		Credentials: api.Credentials{
			Reason:  strings.TrimSpace(in.Reason),
			Fields:  in.Fields,
			EnvFile: in.EnvFile,
		},
	})
	if err != nil {
		return credentialsResult{}, errors.New(requestCredentials + ": " + refusal(err).Error())
	}
	if asked.Status == api.AskProvided {
		asked.EnvFile = in.EnvFile
	}
	return credentialsResult{Asked: asked, Note: note(asked)}, nil
}

func note(a api.Asked) string {
	switch a.Status {
	case api.AskProvided:
		s := "Claude Code's Bash tool already has them as environment variables. In any other shell, run `. " + a.Env + " && <command>`, or read one value inside a command as \"$(cat <file>)\". Never print them."
		if a.EnvFile != "" {
			s += " They are also in " + a.EnvFile + "; do not read it back."
		}
		return s
	case api.AskDeclined:
		return "The user chose not to give them. Do not ask again in this turn; carry on without them or explain what they would unlock."
	case api.AskCanceled:
		return "The turn ended before the user answered."
	default:
		return "The user did not answer in time. Ask again with a new client_request_id only if you still need them."
	}
}
