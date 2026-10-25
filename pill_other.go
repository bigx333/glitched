//go:build !windows

package main

// The Agents pill's label as laid out, its dot on the middle of the text's
// cap height, half a DIP under its line box's middle at 12 DIPs.
const (
	agentsTextLift = 0
	agentsDotDrop  = 0.5
)
