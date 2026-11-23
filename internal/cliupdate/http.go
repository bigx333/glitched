package cliupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTP is the real Lookup: a GET of the source's URL that says nothing of
// who asks, no cookies and no identifiers.
type HTTP struct{}

const (
	lookupTimeout = 10 * time.Second
	maxBody       = 1 << 20
)

func (HTTP) Latest(ctx context.Context, s Source) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "glitched")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %s", s.URL, resp.Status)
	}
	var v any
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&v); err != nil {
		return "", fmt.Errorf("%s: %w", s.URL, err)
	}
	for _, key := range strings.Split(s.Field, ".") {
		m, _ := v.(map[string]any)
		v = m[key]
	}
	latest, ok := v.(string)
	if !ok || latest == "" {
		return "", fmt.Errorf("%s has no %s", s.URL, s.Field)
	}
	return strings.TrimPrefix(latest, s.Trim), nil
}
