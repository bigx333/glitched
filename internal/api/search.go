package api

import "time"

// SearchQuery searches indexed user and assistant transcript messages.
// When Thread is zero, results span all non-deleted threads (at most one
// best hit per thread).
type SearchQuery struct {
	Query  string `json:"query"`
	Thread ID     `json:"thread,omitzero"`
	Limit  int    `json:"limit,omitempty"`
}

// SearchHit is one full-text transcript match.
type SearchHit struct {
	Thread    ID        `json:"thread"`
	Title     string    `json:"title"`
	Workspace string    `json:"workspace"`
	Project   string    `json:"project,omitempty"`
	Seq       uint64    `json:"seq"`
	Role      string    `json:"role"`
	Snippet   string    `json:"snippet"`
	Score     float64   `json:"score,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SearchResult holds matching transcript hits ordered by relevance/recency.
type SearchResult struct {
	Hits []SearchHit `json:"hits"`
}
