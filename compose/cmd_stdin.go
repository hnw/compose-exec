package compose

import (
	"io"
	"strings"
)

func stdinEnabled(r io.Reader) bool {
	if r == nil {
		return false
	}
	if sr, ok := r.(*strings.Reader); ok && sr.Len() == 0 {
		return false
	}
	return true
}
