package install

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const wantPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>dev.kevnnard.watts</string>
	<key>ProgramArguments</key>
	<array>
		<string>/Users/k/bin/us&amp;age</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>/Users/k/Library/Logs/watts.log</string>
	<key>StandardErrorPath</key>
	<string>/Users/k/Library/Logs/watts.log</string>
</dict>
</plist>
`

const wantUnit = `[Unit]
Description=watts energy sampler (watts daemon)

[Service]
ExecStart="/home/k/my bin/watts" daemon
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`

func TestPlanFor(t *testing.T) {
	tests := []struct {
		name string
		o    Options
		want Plan
		err  string
	}{
		{"macOS LaunchAgent", Options{GOOS: "darwin", Binary: "/Users/k/bin/us&age", Home: "/Users/k", UID: 501}, Plan{
			Path:    "/Users/k/Library/LaunchAgents/dev.kevnnard.watts.plist",
			Content: wantPlist,
			Commands: []Command{
				{Args: []string{"launchctl", "bootout", "gui/501/dev.kevnnard.watts"}, IgnoreError: true},
				{Args: []string{"launchctl", "bootstrap", "gui/501", "/Users/k/Library/LaunchAgents/dev.kevnnard.watts.plist"}},
			},
			Log: "/Users/k/Library/Logs/watts.log",
		}, ""},
		{"Linux systemd user unit", Options{GOOS: "linux", Binary: "/home/k/my bin/watts", Home: "/home/k"}, Plan{
			Path:    "/home/k/.config/systemd/user/watts.service",
			Content: wantUnit,
			Commands: []Command{
				{Args: []string{"systemctl", "--user", "daemon-reload"}},
				{Args: []string{"systemctl", "--user", "enable", "--now", "watts.service"}},
				{Args: []string{"systemctl", "--user", "restart", "watts.service"}},
			},
			Log: "journalctl --user -u watts.service",
		}, ""},
		{"relative binary", Options{GOOS: "linux", Binary: "watts", Home: "/home/k"}, Plan{}, `install: binary path "watts" must be absolute`},
		{"relative home", Options{GOOS: "linux", Binary: "/usr/local/bin/watts", Home: "home"}, Plan{}, `install: home "home" must be absolute`},
		{"unsupported OS", Options{GOOS: "windows", Binary: "/x/watts", Home: "/x"}, Plan{}, "install: unsupported OS windows (macOS and Linux only)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanFor(tt.o)
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Content != tt.want.Content {
				t.Errorf("content =\n%s\nwant\n%s", got.Content, tt.want.Content)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("plan =\n %+v\nwant\n %+v", got, tt.want)
			}
		})
	}
}

func TestDryRun(t *testing.T) {
	p, err := PlanFor(Options{GOOS: "linux", Binary: "/home/k/.local/bin/watts", Home: "/home/k"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	p.DryRun(&out)
	want := "Would write /home/k/.config/systemd/user/watts.service:\n" +
		strings.Replace(wantUnit, `"/home/k/my bin/watts"`, "/home/k/.local/bin/watts", 1) +
		"\nWould run:\n" +
		"  systemctl --user daemon-reload\n" +
		"  systemctl --user enable --now watts.service\n" +
		"  systemctl --user restart watts.service\n"
	if out.String() != want {
		t.Errorf("DryRun =\n%s\nwant\n%s", out.String(), want)
	}

	mac, _ := PlanFor(Options{GOOS: "darwin", Binary: "/Users/k/bin/us&age", Home: "/Users/k", UID: 501})
	out.Reset()
	mac.DryRun(&out)
	want = "Would write /Users/k/Library/LaunchAgents/dev.kevnnard.watts.plist:\n" + wantPlist +
		"\nWould run:\n" +
		"  launchctl bootout gui/501/dev.kevnnard.watts  (a failure is ignored: nothing loaded yet)\n" +
		"  launchctl bootstrap gui/501 /Users/k/Library/LaunchAgents/dev.kevnnard.watts.plist\n"
	if out.String() != want {
		t.Errorf("DryRun =\n%s\nwant\n%s", out.String(), want)
	}
}

type fake struct {
	writes   map[string]string
	ran      [][]string
	fail     map[string]error // by args[1]
	writeErr error
}

func (f *fake) write(path string, data []byte) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writes[path] = string(data)
	return nil
}

func (f *fake) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.ran = append(f.ran, append([]string{name}, args...))
	if err := f.fail[args[0]]; err != nil {
		return []byte("Bootstrap failed: 5: Input/output error\n"), err
	}
	return nil, nil
}

func TestApply(t *testing.T) {
	p, err := PlanFor(Options{GOOS: "darwin", Binary: "/Users/k/bin/watts", Home: "/Users/k", UID: 501})
	if err != nil {
		t.Fatal(err)
	}
	boot := []string{"launchctl", "bootstrap", "gui/501", p.Path}
	out := []string{"launchctl", "bootout", "gui/501/dev.kevnnard.watts"}
	tests := []struct {
		name   string
		f      *fake
		ran    [][]string
		writes int
		stdout string
		err    string
	}{
		{"ok", &fake{}, [][]string{out, boot}, 1,
			"wrote " + p.Path + "\nran launchctl bootout gui/501/dev.kevnnard.watts\nran launchctl bootstrap gui/501 " + p.Path + "\n", ""},
		{"bootout failure is ignored", &fake{fail: map[string]error{"bootout": errors.New("exit status 3")}}, [][]string{out, boot}, 1,
			"wrote " + p.Path + "\nran launchctl bootout gui/501/dev.kevnnard.watts (ignored: exit status 3)\nran launchctl bootstrap gui/501 " + p.Path + "\n", ""},
		{"bootstrap failure stops", &fake{fail: map[string]error{"bootstrap": errors.New("exit status 5")}}, [][]string{out, boot}, 1,
			"wrote " + p.Path + "\nran launchctl bootout gui/501/dev.kevnnard.watts\n",
			"install: launchctl bootstrap gui/501 " + p.Path + ": exit status 5: Bootstrap failed: 5: Input/output error"},
		{"write failure runs nothing", &fake{writeErr: errors.New("permission denied")}, nil, 0, "",
			"install: write " + p.Path + ": permission denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.f.writes = map[string]string{}
			var stdout bytes.Buffer
			err := p.Apply(context.Background(), tt.f.write, tt.f.run, &stdout)
			if (err == nil) != (tt.err == "") || (err != nil && err.Error() != tt.err) {
				t.Errorf("err = %v, want %q", err, tt.err)
			}
			if !reflect.DeepEqual(tt.f.ran, tt.ran) {
				t.Errorf("ran %q, want %q", tt.f.ran, tt.ran)
			}
			if len(tt.f.writes) != tt.writes || (tt.writes == 1 && tt.f.writes[p.Path] != p.Content) {
				t.Errorf("writes = %v", tt.f.writes)
			}
			if stdout.String() != tt.stdout {
				t.Errorf("stdout =\n%q\nwant\n%q", stdout.String(), tt.stdout)
			}
		})
	}
}

func TestTemporaryBinary(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/var/folders/x/T/go-build123/b001/exe/watts", true},
		{"/tmp/go-build99/exe/watts", true},
		{"/Users/k/.local/bin/watts", false},
		{"/Users/k/go-builder/watts", false},
	}
	for _, tt := range tests {
		if got := TemporaryBinary(tt.path); got != tt.want {
			t.Errorf("TemporaryBinary(%q) = %v", tt.path, got)
		}
	}
}
