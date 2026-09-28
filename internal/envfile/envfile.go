// Package envfile edits dotenv files in place without disturbing what it
// does not know: comments, blank lines and foreign keys pass through
// untouched, known keys get replaced in their original line position, and new
// keys land at the end. Writes are atomic (temp file + rename) and always end
// 0600 — these files carry API keys.
package envfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Update sets each entry of kv in the file at path and returns which keys
// were added rather than replaced. A missing file is created. Lines that are
// comments, blanks, or carry other keys are preserved byte for byte; a
// `export KEY=` prefix survives the rewrite.
func Update(path string, kv map[string]string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	// A trailing empty line is the artefact of the final newline, not a real
	// blank row — drop it so appends don't leave a hole, restore at write.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	seen := make(map[string]bool, len(kv))
	for i, line := range lines {
		name, ok := lineKey(line)
		if !ok {
			continue
		}
		if v, want := kv[name]; want && !seen[name] {
			prefix := ""
			if strings.HasPrefix(strings.TrimSpace(line), "export ") {
				prefix = "export "
			}
			lines[i] = prefix + name + "=" + v
			seen[name] = true
		}
	}
	var added []string
	for name, v := range kv {
		if !seen[name] {
			added = append(added, name+"="+v)
			seen[name] = true
		}
	}
	lines = append(lines, added...)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	body := strings.Join(lines, "\n") + "\n"
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	// Chmod again after rename: an existing file with looser permissions must
	// end tight too.
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Chmod(path, 0o600)
}

// Load returns the KEY=value pairs of the file at path. Comments and blank
// lines are skipped; a missing file yields an empty map, not an error.
func Load(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		name, ok := lineKey(line)
		if !ok {
			continue
		}
		_, value, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export ")), "=")
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		out[name] = value
	}
	return out, nil
}

// lineKey extracts the variable name from a KEY=value line (optionally
// `export`-prefixed), or reports the line is not an assignment.
func lineKey(line string) (string, bool) {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
	name, _, ok := strings.Cut(s, "=")
	name = strings.TrimSpace(name)
	if !ok || name == "" || strings.HasPrefix(name, "#") {
		return "", false
	}
	for _, r := range name {
		if !(r == '_' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return "", false
		}
	}
	return name, true
}

// ValidateValue refuses values that would corrupt the file layout — a
// settings dialog must never let a newline slip a second line in.
func ValidateValue(v string) error {
	if strings.ContainsAny(v, "\r\n") {
		return fmt.Errorf("value must be a single line")
	}
	return nil
}

// KeysSorted lists map keys in a stable order — handy when building dialog
// text.
func KeysSorted(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
