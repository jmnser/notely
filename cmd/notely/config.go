//go:build !wasm

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

// config is the client's configuration file.
type config struct {
	Server struct {
		URL  string `toml:"url"`
		User string `toml:"user"`
		// PasswordCommand prints the password, so it can stay in the system
		// keychain or a password manager instead of this file.
		PasswordCommand string `toml:"password_command"`
	} `toml:"server"`
	Daemon struct {
		Dir string `toml:"dir"` // synced by -daemon; ~/ is the home directory
	} `toml:"daemon"`
}

const windows = "windows" // runtime.GOOS

// validUser matches the server's user names; they go into shell commands.
var validUser = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// configDir is $XDG_CONFIG_HOME/notely, by default ~/.config/notely, on
// every OS, as for most terminal tools.
func configDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) { // relative values are invalid
		return filepath.Join(dir, "notely"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "notely"), nil
}

// configPath is $NOTELY_CONFIG, else config.toml in configDir.
func configPath() (string, error) {
	if path := os.Getenv("NOTELY_CONFIG"); path != "" {
		return path, nil
	}
	dir, err := configDir()
	return filepath.Join(dir, "config.toml"), err
}

// loadConfig reads the configuration; a missing file is an empty one.
func loadConfig(path string) (config, error) {
	var c config
	_, err := toml.DecodeFile(path, &c)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// initConfig writes a starter configuration for server and user, reading the
// password from the system keychain, and returns how to store it there.
func initConfig(path, server, user string) (store string, err error) {
	if !validUser.MatchString(user) {
		return "", fmt.Errorf("user %q: use letters, digits, - and _; set it with -user", user)
	}
	lookup, store := keychain(user)
	text := fmt.Sprintf(`# notely configuration, written by notely -init.

[server]
url = %q
user = %q
# Prints the password, which stays in the system keychain.
password_command = %q
`, server, user, lookup)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the user's config path
	if errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("%s exists; edit or remove it", path)
	} else if err != nil {
		return "", err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close() //nolint:errcheck,gosec // the write error matters
		return "", err
	}
	return store, f.Close()
}

// keychain returns the commands that look up and store user's password in
// the system keychain. Windows has no such command; lookup is then empty.
func keychain(user string) (lookup, store string) {
	switch runtime.GOOS {
	case "darwin":
		return "security find-generic-password -s notely -a " + user + " -w",
			"security add-generic-password -s notely -a " + user + " -w"
	case windows:
		return "", ""
	}
	return "secret-tool lookup service notely user " + user, // libsecret: GNOME Keyring, KWallet, KeePassXC
		"secret-tool store --label=Notely service notely user " + user
}

// passwordCommand runs cmdline in the shell, writing its output to stdout.
func passwordCommand(cmdline string, stdout *bytes.Buffer) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-c", cmdline) //nolint:gosec // from the user's own config
	if runtime.GOOS == windows {
		cmd = exec.Command("cmd", "/C", cmdline) //nolint:gosec // as above
	}
	cmd.Stdout = stdout
	return cmd
}

// firstLine is a password command's output without the line ending.
func firstLine(out string) (string, error) {
	line, _, _ := strings.Cut(out, "\n")
	if line = strings.TrimRight(line, "\r"); line == "" {
		return "", errors.New("password_command printed no password")
	}
	return line, nil
}
