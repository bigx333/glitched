package claude

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"glitched/internal/provider"
)

// askModel is the model an ask runs on when it names none: the cheapest
// the account has, by Claude Code's alias for it.
const askModel = "haiku"

// oneShot is what claude -p --output-format json prints, as far as an
// ask reads it. ModelUsage is keyed by the full id of each model that ran.
type oneShot struct {
	Result     string                     `json:"result"`
	IsError    bool                       `json:"is_error"`
	ModelUsage map[string]json.RawMessage `json:"modelUsage"`
}

// Ask runs one claude -p with no tools, no MCP servers and no session
// left behind, the prompt on stdin. It runs in an empty folder of its own
// with the user's settings alone, so no project's CLAUDE.md, settings or
// hooks reach it. The user's settings stay: they may hold how claude signs
// in, which --bare and an empty --setting-sources both drop.
func (a *Adapter) Ask(ctx context.Context, spec provider.AskSpec) (provider.AskResult, error) {
	bin, err := a.binary()
	if err != nil {
		return provider.AskResult{}, fmt.Errorf("%w: %w", provider.ErrUnavailable, err)
	}
	dir, err := os.MkdirTemp("", "glitched-ask-")
	if err != nil {
		return provider.AskResult{}, err
	}
	defer os.RemoveAll(dir)
	model := cmp.Or(modelOf(spec.Model), askModel)
	args := []string{"-p", "--no-session-persistence", "--tools", "", "--strict-mcp-config", "--setting-sources", "user", "--output-format", "json", "--model", model}
	if spec.System != "" {
		args = append(args, "--system-prompt", spec.System)
	}
	cmd := Command(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(spec.Prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var r oneShot
	if jerr := json.Unmarshal(out, &r); jerr != nil {
		if err == nil {
			err = jerr
		}
		if line := strings.TrimSpace(stderr.String()); line != "" {
			err = errors.New(line)
		}
		return provider.AskResult{}, fmt.Errorf("claude: %w", err)
	}
	if r.IsError {
		return provider.AskResult{}, fmt.Errorf("claude: %s", r.Result)
	}
	ran := model
	if keys := slices.Collect(maps.Keys(r.ModelUsage)); len(keys) == 1 {
		ran = keys[0]
	}
	return provider.AskResult{Text: r.Result, Model: ran}, nil
}
