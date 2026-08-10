package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AppendProvider writes one canonical provider line to the config file,
// creating the file if needed (used by `maestro provider add`).
func AppendProvider(path, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content := string(data)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += line + "\n"
	return writeConfigFile(path, []byte(content))
}

// RemoveProvider removes a `provider add <name>` block — the declaration
// line plus any backslash-continuation lines — from the config file.
func RemoveProvider(path, name string) error {
	if err := ValidateProviderID(name); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(data), "\n")
	prefix := "provider add " + name + " "
	var out []string
	skip := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !skip && strings.HasPrefix(trimmed, prefix) {
			skip = true
			if strings.HasSuffix(trimmed, "\\") {
				continue // continuation lines below are dropped with it
			}
			skip = false
			continue
		}
		if skip {
			// keep dropping continuation lines until one without a backslash
			if strings.HasSuffix(trimmed, "\\") {
				continue
			}
			skip = false
			continue
		}
		out = append(out, line)
	}
	return writeConfigFile(path, []byte(strings.Join(out, "\n")))
}

// ProviderLine renders a validated single-line provider declaration.
func ProviderLine(p Provider) (string, error) {
	if err := ValidateProvider(p); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "provider add %s", p.Name)
	if p.Type != "" {
		fmt.Fprintf(&b, " --type %s", p.Type)
	}
	if p.BaseURL != "" {
		fmt.Fprintf(&b, " --base-url %q", p.BaseURL)
	}
	if p.Disabled {
		b.WriteString(" --disable")
	}
	if p.DiscoverModels {
		b.WriteString(" --discover-models")
	}
	return b.String(), nil
}

// writeConfigFile replaces a config atomically and keeps it private. Config
// files may contain custom authorization headers or environment references,
// so they must never inherit a permissive umask or retain an old 0644 mode.
func writeConfigFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".maestrorc-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
