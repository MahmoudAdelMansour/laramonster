package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// EnvFile is what the .env editor works with.
type EnvFile struct {
	Path         string `json:"path"`
	Exists       bool   `json:"exists"`
	Content      string `json:"content"`
	Example      bool   `json:"example"`       // .env.example exists
	ConfigCached bool   `json:"config_cached"` // Laravel won't read .env until the cache is cleared
}

func EnvPath(dir string) string { return filepath.Join(dir, ".env") }

func configCache(dir string) string { return filepath.Join(dir, "bootstrap", "cache", "config.php") }

func ReadEnv(dir string) (EnvFile, error) {
	f := EnvFile{Path: EnvPath(dir), Example: isFile(filepath.Join(dir, ".env.example")), ConfigCached: isFile(configCache(dir))}
	b, err := os.ReadFile(f.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return f, nil
	case err != nil:
		return f, err
	}
	f.Exists, f.Content = true, string(b)
	return f, nil
}

// WriteEnv replaces .env atomically and keeps its permissions. The previous
// version is kept as .env.backup.
func WriteEnv(dir, content string) error {
	p := EnvPath(dir)
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(p); err == nil {
		mode = fi.Mode().Perm()
		if old, err := os.ReadFile(p); err == nil && string(old) != content {
			_ = os.WriteFile(p+".backup", old, mode)
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	tmp := p + ".laramonster.tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// CreateEnv starts .env from .env.example (or empty).
func CreateEnv(dir string) error {
	if isFile(EnvPath(dir)) {
		return errors.New(".env already exists")
	}
	b, err := os.ReadFile(filepath.Join(dir, ".env.example"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return WriteEnv(dir, string(b))
}

// ClearConfigCache is `php artisan config:clear` without needing PHP.
func ClearConfigCache(dir string) error {
	err := os.Remove(configCache(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// EnvValue returns a key's unquoted value.
func EnvValue(content, key string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		if k, v, ok := envLine(line); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// SetEnvValue sets one key, keeping every other line as it is.
func SetEnvValue(content, key, value string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if k, _, ok := envLine(line); ok && k == key {
			lines[i] = key + "=" + envQuote(value)
			return strings.Join(lines, "\n")
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + key + "=" + envQuote(value) + "\n"
}

func envLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	key, value, ok = strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	key, value = strings.TrimSpace(key), strings.TrimSpace(value)
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && strings.LastIndexByte(value, value[0]) > 0 {
		value = value[1:strings.LastIndexByte(value, value[0])]
	} else if i := strings.Index(value, " #"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	return key, value, true
}

func envQuote(v string) string {
	if v == "" || !strings.ContainsAny(v, " #\"'=$\\`") {
		return v
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

// SetEnvVars sets keys in a project's .env (uncommenting "# KEY=…" lines),
// keeping a .env.backup of the previous file.
func SetEnvVars(dir string, vars map[string]string) error {
	p := EnvPath(dir)
	if old, err := os.ReadFile(p); err == nil {
		_ = os.WriteFile(p+".backup", old, 0o600)
	}
	return setEnv(p, vars)
}

// EnvValues parses a .env into key → value (quotes removed, comments skipped).
func EnvValues(dir string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(EnvPath(dir))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}
