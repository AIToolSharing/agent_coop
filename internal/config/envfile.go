// Package config finds what the coop command needs: the hub address, the tokens, the session
// and the agent name. It mirrors packages/core/src/envfile.ts, packages/mcp/src/config.ts and
// the session command in packages/mcp/src/cli.ts of the TypeScript client (in the history
// before v0.1.0).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"
)

// The credential file holds KEY=VALUE lines. The agent side keeps COOP_URL and COOP_TOKEN in
// it, and the operator side keeps COOP_URL and COOP_OPERATOR_TOKEN.

// DefaultEnvFile returns the path of the credential file, ~/.config/coop/env. When HOME is not
// set, it returns an empty path: ReadEnvFile then gives no values and UpdateEnvFile fails.
func DefaultEnvFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "coop", "env")
}

// ParseEnv reads KEY=VALUE lines. It skips blank lines, lines that start with "#", and lines
// without a key before "=". It trims the key and the value, and removes one pair of quotes
// around the value. When a key occurs on two lines, the last line wins.
func ParseEnv(text string) map[string]string {
	out := map[string]string{}
	for _, raw := range strings.Split(text, "\n") {
		line := trim(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 1 {
			continue
		}
		out[trim(line[:eq])] = unquote(trim(line[eq+1:]))
	}
	return out
}

// FormatEnv writes one KEY=VALUE line for each key, in key order. ParseEnv reads the text back
// to the same map only when each line reads back alone; UpdateEnvFile checks this.
func FormatEnv(values map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(values)) {
		b.WriteString(k + "=" + values[k] + "\n")
	}
	return b.String()
}

// ReadEnvFile reads the credential file at path. The file holds tokens, so when group or other
// users have any permission on it, ReadEnvFile ignores it and calls warn. A missing or
// unreadable file gives no values.
func ReadEnvFile(path string, warn func(string)) map[string]string {
	info, err := os.Stat(path)
	if err != nil {
		return map[string]string{}
	}
	// On Windows, Go gives a fixed mode (0666) that does not show the ACL of the file. The
	// user profile ACL keeps the file private, so this check applies only on other systems.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		warn(path + " is readable by other users; run: chmod 600 " + path)
		return map[string]string{}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	return ParseEnv(string(b))
}

// UpdateEnvFile sets keys in the credential file and keeps the other keys. It creates the
// directory with mode 0700 and gives the file mode 0600. It does not keep comments: the coop
// commands write this file, not a person.
//
// UpdateEnvFile refuses a key or a value that ParseEnv does not read back unchanged. Thus a
// value with a line break cannot add a key. It also refuses a file that exists but that it
// cannot read, because a write would remove the keys of the other side.
func UpdateEnvFile(path string, values map[string]string) error {
	for k, v := range values {
		pair := map[string]string{k: v}
		if !maps.Equal(ParseEnv(FormatEnv(pair)), pair) {
			// The value is not in the message: it can be a token.
			return fmt.Errorf("%s: cannot write key %q: the line does not read back the same", path, k)
		}
	}
	next := map[string]string{}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		next = ParseEnv(string(b))
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	maps.Copy(next, values)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	// The mode of an existing file can be wider. Set the mode before the tokens go in.
	err = f.Chmod(0o600)
	if err == nil {
		_, err = f.WriteString(FormatEnv(next))
	}
	return errors.Join(err, f.Close())
}

// trim removes white space at the two ends of s, as String.prototype.trim does in
// JavaScript. That set includes the byte order mark U+FEFF and does not include U+0085.
// strings.TrimSpace does the opposite for these two characters.
func trim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return r == '\uFEFF' || (r != '\u0085' && unicode.IsSpace(r))
	})
}

// unquote removes one pair of the same quotes around v. The TypeScript pattern is
// /^(['"])(.*)\1$/. In JavaScript, "." does not match a line terminator, so a carriage return,
// U+2028 or U+2029 between the quotes keeps them. A line feed cannot occur: ParseEnv splits
// the text on it.
func unquote(v string) string {
	if len(v) < 2 || (v[0] != '"' && v[0] != '\'') || v[len(v)-1] != v[0] {
		return v
	}
	inner := v[1 : len(v)-1]
	if strings.ContainsAny(inner, "\r\u2028\u2029") {
		return v
	}
	return inner
}
