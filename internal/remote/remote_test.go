package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/store"
)

var t0 = time.Date(2026, 11, 8, 10, 0, 0, 0, time.Local)

func min_(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "watts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seed(t *testing.T, st *store.Store, source string, watts float64, mins ...int) {
	t.Helper()
	for _, m := range mins {
		if err := st.AddSample(store.Sample{TS: min_(m), Source: source, Watts: watts, Seconds: 60}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuild(t *testing.T) {
	st := openStore(t)
	seed(t, st, "linux", 150, 0, 1, 2, 3)
	st.UpsertDaily(store.DailyAggregate{Day: "2026-11-07", Source: "linux", Wh: 900, OnSeconds: 7200, Origin: "consumo"})
	st.UpsertDaily(store.DailyAggregate{Day: "2026-11-01", Source: "linux", Wh: 1})
	st.AddToggle(store.Toggle{Item: "Fan", TS: min_(-600), On: true})
	st.AddToggle(store.Toggle{Item: "Fan", TS: min_(10), On: false})

	e, err := Build(st, min_(-24*60), min_(2))
	if err != nil {
		t.Fatal(err)
	}
	if e.Version != Version || !e.From.Equal(min_(-24*60)) || !e.To.Equal(min_(2)) {
		t.Errorf("header = %+v", e)
	}
	if len(e.Samples) != 3 || !e.Samples[0].TS.Equal(min_(0)) || !e.Samples[2].TS.Equal(min_(2)) {
		t.Errorf("samples = %+v, want (from, to]", e.Samples)
	}
	if len(e.Daily) != 1 || e.Daily[0].Day != "2026-11-07" || e.Daily[0].Wh != 900 {
		t.Errorf("daily = %+v", e.Daily)
	}
	if len(e.Toggles) != 1 || !e.Toggles[0].On {
		t.Errorf("toggles = %+v, want only those before to", e.Toggles)
	}

	empty, err := Build(openStore(t), min_(100), min_(200))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(empty)
	if !strings.Contains(string(b), `"samples":[]`) || !strings.Contains(string(b), `"daily":[]`) || !strings.Contains(string(b), `"toggles":[]`) {
		t.Errorf("empty export must use [] not null: %s", b)
	}
	if _, err := Build(st, min_(2), min_(2)); err == nil || err.Error() != "export: --from must be before --to" {
		t.Errorf("Build(from == to) err = %v", err)
	}
}

type exitErr int

func (e exitErr) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitErr) ExitCode() int { return int(e) }

func TestFetch(t *testing.T) {
	good := Export{Version: Version, From: min_(0), To: min_(5),
		Samples: []store.Sample{{TS: min_(1), Source: "linux", Watts: 150, Seconds: 60}},
		Daily:   []store.DailyAggregate{}, Toggles: []store.Toggle{}}
	goodJSON, _ := json.Marshal(good)
	tests := []struct {
		name    string
		cmd     string
		stdout  []byte
		stderr  []byte
		err     error
		ctxErr  bool
		offline bool
		werr    string
	}{
		{name: "online", stdout: goodJSON},
		{name: "custom command", cmd: "/opt/watts/bin/watts", stdout: goodJSON},
		{name: "ssh cannot connect", stderr: []byte("ssh: connect to host kevnnard-station port 22: Operation timed out\n"), err: exitErr(255),
			offline: true, werr: "kevnnard-station offline: ssh: connect to host kevnnard-station port 22: Operation timed out"},
		{name: "timeout", ctxErr: true, err: errors.New("signal: killed"), offline: true, werr: "kevnnard-station offline: context deadline exceeded"},
		{name: "ssh missing", err: errors.New(`exec: "ssh": executable file not found in $PATH`), offline: true,
			werr: `kevnnard-station offline: exec: "ssh": executable file not found in $PATH`},
		{name: "remote watts missing", stderr: []byte("zsh:1: no such file or directory: ~/.local/bin/watts\n"), err: exitErr(127),
			werr: "kevnnard-station: remote command failed (exit 127): zsh:1: no such file or directory: ~/.local/bin/watts"},
		{name: "empty output", stdout: nil, werr: "kevnnard-station: bad export: unexpected end of JSON input"},
		{name: "wrong version", stdout: []byte(`{"version":9,"samples":[]}`), werr: "kevnnard-station: export version 9, want 1 (update watts on both machines)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var argv []string
			c := &Client{Host: "kevnnard-station", Command: tt.cmd,
				Run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
					argv = append([]string{name}, args...)
					return tt.stdout, tt.stderr, tt.err
				}}
			ctx := context.Background()
			if tt.ctxErr {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
			}
			got, err := c.Fetch(ctx, min_(0), min_(5))
			cmd := tt.cmd
			if cmd == "" {
				cmd = DefaultCommand
			}
			want := []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=4", "kevnnard-station",
				cmd, "export", "--json", "--from", min_(0).Format(time.RFC3339), "--to", min_(5).Format(time.RFC3339)}
			if !reflect.DeepEqual(argv, want) {
				t.Errorf("argv = %q\nwant %q", argv, want)
			}
			if tt.werr != "" {
				if err == nil || err.Error() != tt.werr {
					t.Fatalf("err = %v, want %q", err, tt.werr)
				}
				if errors.Is(err, ErrOffline) != tt.offline {
					t.Errorf("errors.Is(err, ErrOffline) = %v, want %v", !tt.offline, tt.offline)
				}
				if !reflect.DeepEqual(got, Export{}) {
					t.Errorf("failed fetch returned data %+v", got)
				}
				return
			}
			if err != nil || len(got.Samples) != 1 || got.Samples[0].Watts != 150 {
				t.Fatalf("Fetch = %+v, %v", got, err)
			}
		})
	}
	if _, err := (&Client{}).Fetch(context.Background(), min_(0), min_(1)); err == nil || err.Error() != "remote: no host configured" {
		t.Errorf("no host: %v", err)
	}
}

func TestMerge(t *testing.T) {
	d := store.Data{
		Samples: map[string][]store.Sample{
			"mac":   {{TS: min_(1), Source: "mac", Watts: 20, Seconds: 60}},
			"linux": {{TS: min_(1), Source: "linux", Watts: 1, Seconds: 60}, {TS: min_(9), Source: "linux", Watts: 9, Seconds: 60}},
		},
		Daily:   map[string][]store.DailyAggregate{"linux": {{Day: "2026-11-07", Source: "linux", Wh: 1}}},
		Toggles: map[string][]store.Toggle{"Fan": {{Item: "Fan", TS: min_(0), On: true}}},
	}
	e := Export{Version: Version,
		Samples: []store.Sample{
			{TS: min_(2), Source: "linux", Watts: 150, Seconds: 60},
			{TS: min_(1), Source: "linux", Watts: 140, Seconds: 60},
		},
		Daily:   []store.DailyAggregate{{Day: "2026-11-07", Source: "linux", Wh: 900}, {Day: "2026-11-06", Source: "linux", Wh: 800}},
		Toggles: []store.Toggle{{Item: "Fan", TS: min_(0), On: true}, {Item: "Fan", TS: min_(3), On: false}},
	}
	Merge(&d, e)
	wantLinux := []store.Sample{
		{TS: min_(1), Source: "linux", Watts: 140, Seconds: 60},
		{TS: min_(2), Source: "linux", Watts: 150, Seconds: 60},
		{TS: min_(9), Source: "linux", Watts: 9, Seconds: 60},
	}
	if !reflect.DeepEqual(d.Samples["linux"], wantLinux) {
		t.Errorf("linux samples = %+v", d.Samples["linux"])
	}
	if len(d.Samples["mac"]) != 1 || d.Samples["mac"][0].Watts != 20 {
		t.Errorf("mac samples changed: %+v", d.Samples["mac"])
	}
	if got := d.Daily["linux"]; len(got) != 2 || got[0].Day != "2026-11-06" || got[1].Wh != 900 {
		t.Errorf("daily = %+v", got)
	}
	if got := d.Toggles["Fan"]; len(got) != 2 || got[1].On {
		t.Errorf("toggles = %+v", got)
	}
	var nilData store.Data
	Merge(&nilData, e)
	if len(nilData.Samples["linux"]) != 2 {
		t.Errorf("merge into empty data = %+v", nilData)
	}
}

func TestLoadInput(t *testing.T) {
	cfg := config.Default()
	st := openStore(t)
	seed(t, st, "mac", 20, 1, 2)
	remoteJSON, _ := json.Marshal(Export{Version: Version, Samples: []store.Sample{{TS: min_(2), Source: "linux", Watts: 150, Seconds: 60}}})
	tests := []struct {
		name   string
		client *Client
		state  State
		linux  int
		err    string
	}{
		{"local only", nil, StateNone, 0, ""},
		{"online", &Client{Host: "pc", Run: func(context.Context, string, ...string) ([]byte, []byte, error) {
			return remoteJSON, nil, nil
		}}, StateOnline, 1, ""},
		{"offline", &Client{Host: "pc", Run: func(context.Context, string, ...string) ([]byte, []byte, error) {
			return nil, []byte("ssh: Could not resolve hostname pc\n"), exitErr(255)
		}}, StateOffline, 0, "pc offline: ssh: Could not resolve hostname pc"},
		{"failed", &Client{Host: "pc", Run: func(context.Context, string, ...string) ([]byte, []byte, error) {
			return []byte("not json"), nil, nil
		}}, StateFailed, 0, "pc: bad export: invalid character 'o' in literal null (expecting 'u')"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, status, err := LoadInput(context.Background(), st, cfg, tt.client, min_(0), min_(5))
			if err != nil {
				t.Fatal(err)
			}
			if status.State != tt.state || len(in.Data.Samples["linux"]) != tt.linux || len(in.Data.Samples["mac"]) != 2 {
				t.Errorf("status %+v, linux %d, mac %d", status, len(in.Data.Samples["linux"]), len(in.Data.Samples["mac"]))
			}
			if (status.Err == nil) != (tt.err == "") || (status.Err != nil && status.Err.Error() != tt.err) {
				t.Errorf("status.Err = %v, want %q", status.Err, tt.err)
			}
			if in.Interval != cfg.Interval() || len(in.Items) != len(cfg.Items) {
				t.Errorf("input = %+v", in)
			}
		})
	}
}

func TestFromConfig(t *testing.T) {
	cfg := config.Default()
	t.Setenv("WATTS_REMOTE_COMMAND", "")
	if c := FromConfig(cfg); c.Host != "kevnnard-station" || c.Command != DefaultCommand {
		t.Errorf("FromConfig = %+v", c)
	}
	t.Setenv("WATTS_REMOTE_COMMAND", "/usr/local/bin/watts-energy")
	if c := FromConfig(cfg); c.Command != "/usr/local/bin/watts-energy" {
		t.Errorf("FromConfig with env = %+v", c)
	}
	cfg.Remote.Command = "/opt/watts/bin/watts"
	if c := FromConfig(cfg); c.Command != "/usr/local/bin/watts-energy" {
		t.Errorf("env must override remote.command: %+v", c)
	}
	t.Setenv("WATTS_REMOTE_COMMAND", "")
	if c := FromConfig(cfg); c.Command != "/opt/watts/bin/watts" {
		t.Errorf("FromConfig with remote.command = %+v", c)
	}
	cfg.Remote.Command = ""
	if c := FromConfig(cfg); c.Command != DefaultCommand {
		t.Errorf("FromConfig with empty remote.command = %+v", c)
	}
}

func TestDisplaysExportAndMerge(t *testing.T) {
	st := openStore(t)
	st.AddDisplays([]store.Display{
		{TS: min_(0), Source: "linux", Brightness: 10, MaxBrightness: 100, On: true},
		{TS: min_(1), Source: "linux", Brightness: 65, MaxBrightness: 100, On: true},
		{TS: min_(2), Source: "linux", Brightness: 0, MaxBrightness: 100, On: false},
	})
	e, err := Build(st, min_(0), min_(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Displays) != 2 || e.Displays[0].Brightness != 65 || e.Displays[1].On {
		t.Fatalf("displays = %+v, want (from, to]", e.Displays)
	}
	b, _ := json.Marshal(e)
	var back Export
	if err := json.Unmarshal(b, &back); err != nil || len(back.Displays) != 2 || !back.Displays[0].TS.Equal(min_(1)) {
		t.Fatalf("round trip = %+v, %v", back.Displays, err)
	}
	// An empty store omits the key, so the export is what older versions print.
	empty, _ := Build(openStore(t), min_(0), min_(2))
	if b, _ := json.Marshal(empty); strings.Contains(string(b), "displays") {
		t.Errorf("empty export has displays: %s", b)
	}

	d := store.Data{Displays: []store.Display{
		{TS: min_(1), Source: "linux", Brightness: 1, MaxBrightness: 100, On: true},
		{TS: min_(5), Source: "linux", Brightness: 5, MaxBrightness: 100, On: true},
	}}
	Merge(&d, e)
	want := []store.Display{
		{TS: min_(1), Source: "linux", Brightness: 65, MaxBrightness: 100, On: true}, // remote wins
		{TS: min_(2), Source: "linux", Brightness: 0, MaxBrightness: 100, On: false},
		{TS: min_(5), Source: "linux", Brightness: 5, MaxBrightness: 100, On: true},
	}
	if !reflect.DeepEqual(d.Displays, want) {
		t.Errorf("merged displays =\n %+v\nwant\n %+v", d.Displays, want)
	}
}

// An export from a version without displays still decodes and merges.
func TestFetchExportWithoutDisplays(t *testing.T) {
	old := []byte(`{"version":1,"from":"2026-11-08T10:00:00Z","to":"2026-11-08T10:05:00Z",` +
		`"samples":[{"ts":"2026-11-08T10:01:00Z","source":"linux","watts":150,"seconds":60}],"daily":[],"toggles":[]}`)
	c := &Client{Host: "kevnnard-station", Run: func(context.Context, string, ...string) ([]byte, []byte, error) { return old, nil, nil }}
	e, err := c.Fetch(context.Background(), min_(0), min_(5))
	if err != nil || len(e.Samples) != 1 || e.Displays != nil {
		t.Fatalf("Fetch old export = %+v, %v", e, err)
	}
	d := store.Data{Displays: []store.Display{{TS: min_(1), Source: "linux", Brightness: 65, MaxBrightness: 100, On: true}}}
	Merge(&d, e)
	if len(d.Displays) != 1 || d.Displays[0].Brightness != 65 {
		t.Errorf("merge without displays changed them: %+v", d.Displays)
	}
}
