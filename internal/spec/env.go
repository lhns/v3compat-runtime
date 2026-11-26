package spec

import (
	"fmt"
	"slices"
	"strings"
)

// envKey is the name part of a "KEY=VALUE" entry; an entry without '=' is all name.
func envKey(e string) string {
	k, _, _ := strings.Cut(e, "=")
	return k
}

// envEntries reads process.env; a missing key is an empty list, a non-string entry an error.
func envEntries(m map[string]any) ([]string, error) {
	raw, _ := m["env"].([]any)
	out := make([]string, len(raw))
	for i, e := range raw {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("process.env has a non-string entry")
		}
		out[i] = s
	}
	return out, nil
}

func putEnv(m map[string]any, entries []string) {
	out := make([]any, len(entries))
	for i, s := range entries {
		out[i] = s
	}
	m["env"] = out
}

// removeEnv drops every entry whose key is exactly one of names, duplicates included, and returns
// the names that were present, in the order given.
func removeEnv(m map[string]any, names ...string) ([]string, error) {
	entries, err := envEntries(m)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, n := range names {
		var kept []string
		found := false
		for _, e := range entries {
			if envKey(e) == n {
				found = true
			} else {
				kept = append(kept, e)
			}
		}
		if found {
			removed = append(removed, n)
			entries = kept
		}
	}
	if removed != nil {
		putEnv(m, entries)
	}
	return removed, nil
}

// setEnv replaces every entry for key with a single KEY=VALUE at the end. A lone entry that
// already has the value stays where it is, so a repeated edit changes nothing.
func setEnv(m map[string]any, key, value string) error {
	entries, err := envEntries(m)
	if err != nil {
		return err
	}
	own := slices.DeleteFunc(slices.Clone(entries), func(e string) bool { return envKey(e) != key })
	if len(own) == 1 && own[0] == key+"="+value {
		return nil
	}
	if _, err := removeEnv(m, key); err != nil {
		return err
	}
	entries, _ = envEntries(m)
	putEnv(m, append(entries, key+"="+value))
	return nil
}

// setEnvIfAbsent appends KEY=VALUE unless an entry for key exists.
func setEnvIfAbsent(m map[string]any, key, value string) error {
	entries, err := envEntries(m)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if envKey(e) == key {
			return nil
		}
	}
	putEnv(m, append(entries, key+"="+value))
	return nil
}
