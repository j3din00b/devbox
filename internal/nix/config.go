package nix

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"

	"go.jetify.com/devbox/internal/redact"
	"go.jetify.com/devbox/nix"
)

// Config is a parsed Nix configuration.
type Config struct {
	ExperimentalFeatures ConfigField[[]string] `json:"experimental-features"`
	Substitute           ConfigField[bool]     `json:"substitute"`
	Substituters         ConfigField[[]string] `json:"substituters"`
	System               ConfigField[string]   `json:"system"`
	TrustedSubstituters  ConfigField[[]string] `json:"trusted-substituters"`
	TrustedUsers         ConfigField[[]string] `json:"trusted-users"`
}

// ConfigField is a Nix configuration setting.
type ConfigField[T any] struct {
	Value T `json:"value"`
}

// CurrentConfig reads the current Nix configuration.
func CurrentConfig(ctx context.Context) (Config, error) {
	// `nix show-config` is deprecated in favor of `nix config show`, but we
	// want to remain compatible with older Nix versions.
	cmd := Command("show-config", "--json")
	out, err := cmd.Output(ctx)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) != 0 {
		return Config{}, redact.Errorf("command %s: %v: %s", redact.Safe(cmd), err, exitErr.Stderr)
	}
	if err != nil {
		return Config{}, redact.Errorf("command %s: %v", cmd, err)
	}
	cfg := Config{}
	if err := json.Unmarshal(out, &cfg); err != nil {
		return Config{}, redact.Errorf("unmarshal JSON output from %s: %v", redact.Safe(cmd), err)
	}
	return cfg, nil
}

// IsUserTrusted reports if the current OS user is in the trusted-users list. If
// there are any groups in the list, it also checks if the user belongs to any
// of them.
func (c Config) IsUserTrusted(ctx context.Context, username string) (bool, error) {
	trusted := c.TrustedUsers.Value
	if len(trusted) == 0 {
		return false, nil
	}

	current, err := user.Lookup(username)
	if err != nil {
		return false, redact.Errorf("lookup user: %v", err)
	}
	if slices.Contains(trusted, current.Username) {
		return true, nil
	}

	// trusted-user entries that start with an @ are group names
	// (for example, @wheel). Lookup each group ID to see if the user
	// belongs to a trusted group.
	var currentGids []string
	for i := range trusted {
		groupName := strings.TrimPrefix(trusted[i], "@")
		if groupName == trusted[i] || groupName == "" {
			continue
		}

		group, err := user.LookupGroup(groupName)
		var unknownErr user.UnknownGroupError
		if errors.As(err, &unknownErr) {
			slog.Debug("skipping unknown trusted-user group found in nix.conf", "group", groupName)
			continue
		}
		if err != nil {
			return false, redact.Errorf("lookup trusted-user group from nix.conf: %v", err)
		}

		// Be lazy about looking up the current user's groups until we
		// encounter one in the trusted-users list.
		if currentGids == nil {
			currentGids, err = current.GroupIds()
			if err != nil {
				return false, redact.Errorf("lookup current user group IDs: %v", err)
			}
		}
		if slices.Contains(currentGids, group.Gid) {
			return true, nil
		}
	}
	return false, nil
}

func IncludeDevboxConfig(ctx context.Context, username string) error {
	info, _ := nix.Default.Info()
	path := cmp.Or(info.SystemConfig, "/etc/nix/nix.conf")
	includePath := filepath.Join(filepath.Dir(path), "devbox-nix.conf")
	b := fmt.Appendf(nil, "# This config was auto-generated by Devbox.\n\nextra-trusted-users = %s\n", username)
	if err := os.WriteFile(includePath, b, 0o664); err != nil {
		return redact.Errorf("write devbox nix.conf: %v", err)
	}

	appended, err := appendConfigInclude(path, includePath)
	if err != nil {
		return err
	}
	if appended {
		return restartDaemon(ctx)
	}
	return nil
}

func appendConfigInclude(srcPath, includePath string) (appended bool, err error) {
	nixConf, err := os.OpenFile(srcPath, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer nixConf.Close()

	confb, err := io.ReadAll(nixConf)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(confb), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			// <whitespace>
			continue
		}
		if strings.HasPrefix(line, "#") {
			// # comment
			continue
		}

		path := strings.TrimSpace(strings.TrimPrefix(line, "include"))
		if path == includePath {
			// include devbox-nix.conf
			return false, nil
		}
		path = strings.TrimSpace(strings.TrimPrefix(line, "!include"))
		if path == includePath {
			// !include devbox-nix.conf
			return false, nil
		}
	}

	include := "\ninclude " + includePath + "\n"
	if _, err := nixConf.WriteString(include); err != nil {
		return false, redact.Errorf("append %q to %s: %v", redact.Safe(include), srcPath, err)
	}
	if err := nixConf.Close(); err != nil {
		return false, redact.Errorf("append %q to %s: %v", redact.Safe(include), srcPath, err)
	}
	return true, nil
}
