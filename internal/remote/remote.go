// Package remote moves store data between machines (S9). `watts export
// --json` prints an Export of the local store; on the Mac, Client runs that
// command on the Linux PC over SSH and Merge adds it to the local data, so
// the dashboard and `watts status` see both machines. An unreachable host is
// an explicit offline state, never zero data.
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/energy"
	"github.com/kevnnard/watts/internal/store"
)

// Version is the export format version. Both machines must agree.
const Version = 1

// DefaultCommand is the watts binary on the remote host (see
// config.DefaultRemoteCommand).
const DefaultCommand = config.DefaultRemoteCommand

// CommandEnv overrides the remote command and the config's remote.command
// (e.g. "/opt/bin/watts").
const CommandEnv = "WATTS_REMOTE_COMMAND"

// DefaultTimeout bounds a whole fetch; SSH itself gives up connecting after 4 s.
const DefaultTimeout = 15 * time.Second

// Export is the JSON printed by `watts export --json`: the samples with
// From < ts <= To, the daily aggregates of the local days From..To and every
// toggle before To, plus the DDC display readings (S19) and the network
// readings (S20) with From < ts <= To. Displays and Network are omitted when
// empty and were added within version 1, so an older reader ignores them
// and an older export decodes with none.
type Export struct {
	Version  int                    `json:"version"`
	From     time.Time              `json:"from"`
	To       time.Time              `json:"to"`
	Samples  []store.Sample         `json:"samples"`
	Daily    []store.DailyAggregate `json:"daily"`
	Toggles  []store.Toggle         `json:"toggles"`
	Displays []store.Display        `json:"displays,omitempty"`
	Network  []store.NetworkReading `json:"network,omitempty"`
}

// Build exports the local store for (from, to].
func Build(st *store.Store, from, to time.Time) (Export, error) {
	if !from.Before(to) {
		return Export{}, errors.New("export: --from must be before --to")
	}
	e := Export{Version: Version, From: from, To: to,
		Samples: []store.Sample{}, Daily: []store.DailyAggregate{}, Toggles: []store.Toggle{}}
	xs, err := st.Samples("", from, to)
	if err != nil {
		return Export{}, fmt.Errorf("export: %w", err)
	}
	e.Samples = append(e.Samples, xs...)
	as, err := st.Daily(from.In(time.Local).Format(store.DayLayout), to.In(time.Local).Format(store.DayLayout))
	if err != nil {
		return Export{}, fmt.Errorf("export: %w", err)
	}
	e.Daily = append(e.Daily, as...)
	if e.Displays, err = st.Displays("", from, to); err != nil {
		return Export{}, fmt.Errorf("export: %w", err)
	}
	if e.Network, err = st.NetworkReadings("", from, to); err != nil {
		return Export{}, fmt.Errorf("export: %w", err)
	}
	ts, err := st.Toggles(to)
	if err != nil {
		return Export{}, fmt.Errorf("export: %w", err)
	}
	for _, list := range ts {
		e.Toggles = append(e.Toggles, list...)
	}
	sort.Slice(e.Toggles, func(i, j int) bool {
		a, b := e.Toggles[i], e.Toggles[j]
		if !a.TS.Equal(b.TS) {
			return a.TS.Before(b.TS)
		}
		return a.Item < b.Item
	})
	return e, nil
}

// ErrOffline matches (errors.Is) a fetch that could not reach the host.
var ErrOffline = errors.New("offline")

// OfflineError is a host that SSH could not reach (or that timed out).
type OfflineError struct {
	Host   string
	Reason string
}

func (e *OfflineError) Error() string        { return e.Host + " offline: " + e.Reason }
func (e *OfflineError) Is(target error) bool { return target == ErrOffline }

// Runner runs a command, returning stdout and stderr.
type Runner func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

// Exec runs the command for real.
func Exec(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// Client fetches exports from one host over SSH.
type Client struct {
	Host    string
	Command string        // remote watts binary; "" means DefaultCommand
	Timeout time.Duration // 0 means DefaultTimeout
	Run     Runner        // nil means Exec
}

// FromConfig is the client for cfg.Remote.Host. The remote command is
// $WATTS_REMOTE_COMMAND when set, else cfg.Remote.Command, else
// DefaultCommand.
func FromConfig(cfg *config.Config) *Client {
	cmd := os.Getenv(CommandEnv)
	if cmd == "" {
		cmd = cfg.Remote.Command
	}
	if cmd == "" {
		cmd = DefaultCommand
	}
	return &Client{Host: cfg.Remote.Host, Command: cmd}
}

// Fetch runs `ssh -o BatchMode=yes -o ConnectTimeout=4 <host> <command>
// export --json --from <from> --to <to>`. Errors are an *OfflineError
// (errors.Is(err, ErrOffline)) when the host is unreachable, or a plain
// error when it answered but the command or its output failed. A failed
// fetch returns no data.
func (c *Client) Fetch(ctx context.Context, from, to time.Time) (Export, error) {
	if c.Host == "" {
		return Export{}, errors.New("remote: no host configured")
	}
	cmd, run, timeout := c.Command, c.Run, c.Timeout
	if cmd == "" {
		cmd = DefaultCommand
	}
	if run == nil {
		run = Exec
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr, err := run(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=4", c.Host,
		cmd, "export", "--json", "--from", from.Format(time.RFC3339), "--to", to.Format(time.RFC3339))
	if err != nil {
		return Export{}, c.classify(ctx, stderr, err)
	}
	var e Export
	if err := json.Unmarshal(stdout, &e); err != nil {
		return Export{}, fmt.Errorf("%s: bad export: %w", c.Host, err)
	}
	if e.Version != Version {
		return Export{}, fmt.Errorf("%s: export version %d, want %d (update watts on both machines)", c.Host, e.Version, Version)
	}
	return e, nil
}

func (c *Client) classify(ctx context.Context, stderr []byte, err error) error {
	msg := firstLine(stderr)
	if ctx.Err() != nil {
		return &OfflineError{Host: c.Host, Reason: ctx.Err().Error()}
	}
	var ex interface{ ExitCode() int }
	if !errors.As(err, &ex) {
		// ssh did not start.
		return &OfflineError{Host: c.Host, Reason: err.Error()}
	}
	if msg == "" {
		msg = err.Error()
	}
	if ex.ExitCode() == 255 { // ssh's own failure: no route, auth, DNS
		return &OfflineError{Host: c.Host, Reason: msg}
	}
	return fmt.Errorf("%s: remote command failed (exit %d): %s", c.Host, ex.ExitCode(), msg)
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// Merge adds e to d. Remote rows win over local rows with the same key:
// (source, ts) for samples and displays, (day, source) for aggregates,
// (item, ts) for toggles and network readings. Lists stay sorted by time.
func Merge(d *store.Data, e Export) {
	if d.Samples == nil {
		d.Samples = map[string][]store.Sample{}
	}
	if d.Daily == nil {
		d.Daily = map[string][]store.DailyAggregate{}
	}
	if d.Toggles == nil {
		d.Toggles = map[string][]store.Toggle{}
	}
	for src, xs := range group(e.Samples, func(x store.Sample) string { return x.Source }) {
		d.Samples[src] = union(d.Samples[src], xs, func(x store.Sample) int64 { return x.TS.Unix() })
	}
	for src, as := range group(e.Daily, func(a store.DailyAggregate) string { return a.Source }) {
		d.Daily[src] = union(d.Daily[src], as, func(a store.DailyAggregate) string { return a.Day })
	}
	for item, ts := range group(e.Toggles, func(t store.Toggle) string { return t.Item }) {
		d.Toggles[item] = union(d.Toggles[item], ts, func(t store.Toggle) int64 { return t.TS.Unix() })
	}
	if len(e.Displays) > 0 {
		d.Displays = mergeDisplays(d.Displays, e.Displays)
	}
	if len(e.Network) > 0 && d.Network == nil {
		d.Network = map[string][]store.NetworkReading{}
	}
	for item, xs := range group(e.Network, func(x store.NetworkReading) string { return x.Item }) {
		d.Network[item] = union(d.Network[item], xs, func(x store.NetworkReading) int64 { return x.TS.Unix() })
	}
}

// mergeDisplays unions b into a by (source, ts), b winning, sorted by time
// then source like store.Displays.
func mergeDisplays(a, b []store.Display) []store.Display {
	type key struct {
		src string
		ts  int64
	}
	byKey := make(map[key]store.Display, len(a)+len(b))
	for _, xs := range [][]store.Display{a, b} {
		for _, x := range xs {
			byKey[key{x.Source, x.TS.Unix()}] = x
		}
	}
	out := make([]store.Display, 0, len(byKey))
	for _, x := range byKey {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool {
		if ti, tj := out[i].TS.Unix(), out[j].TS.Unix(); ti != tj {
			return ti < tj
		}
		return out[i].Source < out[j].Source
	})
	return out
}

func group[T any](xs []T, key func(T) string) map[string][]T {
	out := map[string][]T{}
	for _, x := range xs {
		out[key(x)] = append(out[key(x)], x)
	}
	return out
}

// union merges b into a by key (b wins) and sorts by key.
func union[T any, K int64 | string](a, b []T, key func(T) K) []T {
	byKey := make(map[K]T, len(a)+len(b))
	for _, x := range a {
		byKey[key(x)] = x
	}
	for _, x := range b {
		byKey[key(x)] = x
	}
	out := make([]T, 0, len(byKey))
	for _, x := range byKey {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return key(out[i]) < key(out[j]) })
	return out
}

// State is the remote host's state in a Status.
type State string

const (
	StateNone    State = "none"    // no client: local data only
	StateOnline  State = "online"  // fetched and merged
	StateOffline State = "offline" // unreachable: show its items as offline
	StateFailed  State = "error"   // reachable, but the command or its output failed
)

// Status is the outcome of the remote part of LoadInput.
type Status struct {
	Host    string
	State   State
	Err     error // set for StateOffline and StateFailed
	Samples int   // remote samples merged
	// Export is what was merged (StateOnline), so a caller can cache it and
	// re-merge it with Merge instead of fetching again.
	Export Export
}

// LoadInput loads the local store for [from, to) plus the energy margin,
// merges the remote export when c is not nil, and returns the energy input
// for cfg. A remote failure is not an error: the input then holds local
// data only and Status says why. err is only a local store failure.
func LoadInput(ctx context.Context, st *store.Store, cfg *config.Config, c *Client, from, to time.Time) (energy.Input, Status, error) {
	in := energy.Input{Items: cfg.Items, Interval: cfg.Interval()}
	d, err := st.Load(from, to, in.Margin())
	if err != nil {
		return energy.Input{}, Status{}, err
	}
	in.Data = d
	if c == nil {
		return in, Status{State: StateNone}, nil
	}
	s := Status{Host: c.Host}
	e, err := c.Fetch(ctx, from, to.Add(in.Margin()))
	switch {
	case errors.Is(err, ErrOffline):
		s.State, s.Err = StateOffline, err
	case err != nil:
		s.State, s.Err = StateFailed, err
	default:
		Merge(&in.Data, e)
		s.State, s.Samples, s.Export = StateOnline, len(e.Samples), e
	}
	return in, s, nil
}
