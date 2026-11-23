package skills

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// front is what a SKILL.md's frontmatter says of its skill.
type front struct {
	Name, Description string
}

// frontmatter reads the YAML between a SKILL.md's leading --- lines: a
// name, and a description of at most 1024 characters. Its other keys are
// the agents'.
func frontmatter(md []byte) (front, error) {
	s := strings.TrimPrefix(string(md), "\ufeff")
	first, rest, _ := strings.Cut(s, "\n")
	if strings.TrimSuffix(first, "\r") != "---" {
		return front{}, errors.New("Its SKILL.md doesn't start with frontmatter between --- lines.")
	}
	var block strings.Builder
	closed := false
	for line := range strings.Lines(rest) {
		if strings.TrimRight(line, "\r\n") == "---" {
			closed = true
			break
		}
		block.WriteString(line)
	}
	if !closed {
		return front{}, errors.New("Its SKILL.md's frontmatter has no closing --- line.")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(block.String()), &doc); err != nil {
		return front{}, fmt.Errorf("Its SKILL.md's frontmatter isn't YAML: %v", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return front{}, errors.New("Its SKILL.md's frontmatter isn't a list of keys and values.")
	}
	var f front
	seen := map[string]bool{}
	m := doc.Content[0].Content
	for i := 0; i+1 < len(m); i += 2 {
		if seen[m[i].Value] {
			return front{}, fmt.Errorf("Its SKILL.md's frontmatter gives %s twice.", m[i].Value)
		}
		seen[m[i].Value] = true
		var to *string
		switch m[i].Value {
		case "name":
			to = &f.Name
		case "description":
			to = &f.Description
		default:
			continue
		}
		if m[i+1].Kind != yaml.ScalarNode {
			return front{}, fmt.Errorf("Its SKILL.md's %s isn't text.", m[i].Value)
		}
		if m[i+1].Tag != "!!null" {
			*to = strings.TrimSpace(m[i+1].Value)
		}
	}
	switch {
	case f.Name == "":
		return front{}, errors.New("Its SKILL.md's frontmatter has no name.")
	case f.Description == "":
		return front{}, errors.New("Its SKILL.md's frontmatter has no description.")
	case utf8.RuneCountInString(f.Description) > 1024:
		return front{}, errors.New("Its SKILL.md's description is longer than 1024 characters.")
	}
	return f, nil
}
