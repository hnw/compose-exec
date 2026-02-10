package compose

import (
	"strconv"
	"strings"
	"unicode"
)

// String returns a human-friendly representation of the command.
//
// When Args is empty, it returns "<default>" to indicate that Docker Engine/image
// defaults (or YAML service.command via resolution) will be used.
func (c *Cmd) String() string {
	if len(c.Args) == 0 {
		return "<default>"
	}
	parts := make([]string, 0, len(c.Args))
	for _, a := range c.Args {
		if needsQuoting(a) {
			parts = append(parts, strconv.Quote(a))
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if unicode.IsSpace(r) || r == '"' || r == '\\' {
			return true
		}
	}
	return false
}
