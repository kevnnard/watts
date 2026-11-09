// Package install writes the service that keeps `watts daemon` running:
// a LaunchAgent on macOS, a systemd user unit on Linux (S11). PlanFor only
// renders; Apply writes and runs through injected functions so tests never
// touch launchd or systemd.
package install

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// Label is the LaunchAgent label; Unit is the systemd unit name.
const (
	Label = "dev.kevnnard.watts"
	Unit  = "watts.service"
)

// Options describe the target machine.
type Options struct {
	GOOS   string // "darwin" or "linux"
	Binary string // absolute path of the watts binary
	Home   string // absolute home directory
	UID    int    // macOS: the launchd gui/<uid> domain
}

// Command is one command to run. IgnoreError is for cleanup steps that fail
// when nothing is installed yet.
type Command struct {
	Args        []string
	IgnoreError bool
}

func (c Command) String() string { return strings.Join(c.Args, " ") }

// Plan is the file to write and the commands that load it.
type Plan struct {
	Path     string
	Content  string
	Commands []Command
	Log      string // where the daemon logs go
}

// PlanFor renders the service for o.
func PlanFor(o Options) (Plan, error) {
	if !filepath.IsAbs(o.Binary) {
		return Plan{}, fmt.Errorf("install: binary path %q must be absolute", o.Binary)
	}
	if !filepath.IsAbs(o.Home) {
		return Plan{}, fmt.Errorf("install: home %q must be absolute", o.Home)
	}
	switch o.GOOS {
	case "darwin":
		return launchAgent(o), nil
	case "linux":
		return systemdUnit(o), nil
	}
	return Plan{}, fmt.Errorf("install: unsupported OS %s (macOS and Linux only)", o.GOOS)
}

func launchAgent(o Options) Plan {
	path := filepath.Join(o.Home, "Library", "LaunchAgents", Label+".plist")
	log := filepath.Join(o.Home, "Library", "Logs", "watts.log")
	esc := func(s string) string {
		var b bytes.Buffer
		xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc(o.Binary) + `</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + esc(log) + `</string>
	<key>StandardErrorPath</key>
	<string>` + esc(log) + `</string>
</dict>
</plist>
`
	domain := fmt.Sprintf("gui/%d", o.UID)
	return Plan{
		Path:    path,
		Content: content,
		Commands: []Command{
			// Unload a previous install first; bootstrap fails if it is loaded.
			{Args: []string{"launchctl", "bootout", domain + "/" + Label}, IgnoreError: true},
			{Args: []string{"launchctl", "bootstrap", domain, path}},
		},
		Log: log,
	}
}

func systemdUnit(o Options) Plan {
	bin := o.Binary
	if strings.ContainsAny(bin, " \t\"\\") {
		bin = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(bin) + `"`
	}
	content := `[Unit]
Description=watts energy sampler (watts daemon)

[Service]
ExecStart=` + bin + ` daemon
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`
	return Plan{
		Path:    filepath.Join(o.Home, ".config", "systemd", "user", Unit),
		Content: content,
		Commands: []Command{
			{Args: []string{"systemctl", "--user", "daemon-reload"}},
			{Args: []string{"systemctl", "--user", "enable", "--now", Unit}},
			// enable --now leaves a running old binary alone; restart picks up this one.
			{Args: []string{"systemctl", "--user", "restart", Unit}},
		},
		Log: "journalctl --user -u " + Unit,
	}
}

// DryRun prints the file and the commands without doing anything.
func (p Plan) DryRun(w io.Writer) {
	fmt.Fprintf(w, "Would write %s:\n%s\nWould run:\n", p.Path, p.Content)
	for _, c := range p.Commands {
		if c.IgnoreError {
			fmt.Fprintf(w, "  %s  (a failure is ignored: nothing loaded yet)\n", c)
			continue
		}
		fmt.Fprintf(w, "  %s\n", c)
	}
}

// Runner runs a command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Exec runs the command for real.
func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Apply writes the file, then runs the commands in order, reporting each to
// w. It stops at the first failing command that is not IgnoreError.
func (p Plan) Apply(ctx context.Context, write func(path string, data []byte) error, run Runner, w io.Writer) error {
	if err := write(p.Path, []byte(p.Content)); err != nil {
		return fmt.Errorf("install: write %s: %w", p.Path, err)
	}
	fmt.Fprintf(w, "wrote %s\n", p.Path)
	for _, c := range p.Commands {
		out, err := run(ctx, c.Args[0], c.Args[1:]...)
		switch {
		case err == nil:
			fmt.Fprintf(w, "ran %s\n", c)
		case c.IgnoreError:
			fmt.Fprintf(w, "ran %s (ignored: %v)\n", c, err)
		default:
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				return fmt.Errorf("install: %s: %w: %s", c, err, msg)
			}
			return fmt.Errorf("install: %s: %w", c, err)
		}
	}
	return nil
}

// TemporaryBinary reports whether path is a `go run` / `go test` build,
// which disappears and must not be installed as a service.
func TemporaryBinary(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.HasPrefix(part, "go-build") && len(part) > len("go-build") && strings.Trim(part[len("go-build"):], "0123456789") == "" {
			return true
		}
	}
	return false
}
