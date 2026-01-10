package compose

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
)

func splitEnv(kv string) (key, value string, ok bool) {
	idx := strings.IndexByte(kv, '=')
	if idx < 0 {
		return "", "", false
	}
	return kv[:idx], kv[idx+1:], true
}

func mergeEnv(base []string, add []string) []string {
	m := make(map[string]string)
	order := make([]string, 0, len(base)+len(add))
	seen := make(map[string]bool)

	addKV := func(kv string) {
		k, v, ok := splitEnv(kv)
		if !ok {
			return
		}
		if !seen[k] {
			order = append(order, k)
			seen[k] = true
		}
		m[k] = v
	}

	for _, kv := range base {
		addKV(kv)
	}
	for _, kv := range add {
		addKV(kv)
	}

	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+m[k])
	}
	return out
}

func randSuffix(nbytes int) (string, error) {
	if nbytes <= 0 {
		return "", errors.New("nbytes must be positive")
	}
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
