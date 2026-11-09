// Package store is the local SQLite store (pure Go, no cgo) in the data dir
// (config.StorePath). It holds raw power samples written by the daemon,
// daily aggregates imported from the old consumo ledgers (S12) and manual
// item toggle events, the monitor's DDC readings (S19) and the power-state
// queries of network items (S20). Estimated items are not stored; package
// energy derives them at read time (S7).
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DayLayout is the format of DailyAggregate.Day.
const DayLayout = "2006-01-02"

// Sample is one daemon reading. It covers [TS − Seconds, TS): Seconds is the
// real time elapsed since the previous sample of the same source (the
// interval for the first one). The S7 gap rule is applied when reading, in
// package energy, so the raw elapsed time is kept.
type Sample struct {
	TS      time.Time `json:"ts"`
	Source  string    `json:"source"` // "mac" or "linux"
	Watts   float64   `json:"watts"`  // whole-device watts (0 for a Mac on battery)
	Seconds float64   `json:"seconds"`
}

// DailyAggregate is one imported day for one source. When a (day, source)
// aggregate exists, energy uses it for that whole local day and ignores the
// samples of that source on that day.
type DailyAggregate struct {
	Day            string  `json:"day"`        // YYYY-MM-DD, local date
	Source         string  `json:"source"`     // "mac" or "linux"
	Wh             float64 `json:"wh"`         // whole-device energy for the day
	OnSeconds      float64 `json:"on_seconds"` // seconds the device was on; drives follows items
	MonitorSeconds float64 `json:"monitor_seconds,omitempty"`
	GPUWh          float64 `json:"gpu_wh,omitempty"`
	CPUWh          float64 `json:"cpu_wh,omitempty"`
	Origin         string  `json:"origin,omitempty"` // e.g. "consumo"
}

// Display is one DDC reading of the monitor by the daemon of Source (S19):
// brightness (VCP 0x10) out of MaxBrightness, and whether the power mode
// (VCP 0xD6) is on. It covers time from TS forward; package energy decides
// how far.
type Display struct {
	TS            time.Time `json:"ts"`
	Source        string    `json:"source"` // "linux"
	Brightness    int       `json:"brightness"`
	MaxBrightness int       `json:"max_brightness"`
	On            bool      `json:"on"` // DPM on; false is standby or off
}

// Toggle is a manual item switched on or off at TS.
type Toggle struct {
	Item string    `json:"item"`
	TS   time.Time `json:"ts"`
	On   bool      `json:"on"`
}

// Data is everything energy needs for a time window, keyed by source
// (Samples, Daily) or item name (Toggles, Network), each sorted by time.
// Displays holds every source, sorted by time then source.
type Data struct {
	Samples  map[string][]Sample
	Daily    map[string][]DailyAggregate
	Toggles  map[string][]Toggle
	Displays []Display
	Network  map[string][]NetworkReading
}

// Store is an open database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS samples (
	ts      INTEGER NOT NULL, -- unix seconds
	source  TEXT    NOT NULL,
	watts   REAL    NOT NULL,
	seconds REAL    NOT NULL,
	PRIMARY KEY (source, ts)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS samples_ts ON samples (ts);
CREATE TABLE IF NOT EXISTS daily (
	day             TEXT NOT NULL,
	source          TEXT NOT NULL,
	wh              REAL NOT NULL DEFAULT 0,
	on_seconds      REAL NOT NULL DEFAULT 0,
	monitor_seconds REAL NOT NULL DEFAULT 0,
	gpu_wh          REAL NOT NULL DEFAULT 0,
	cpu_wh          REAL NOT NULL DEFAULT 0,
	origin          TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (day, source)
);
CREATE TABLE IF NOT EXISTS toggles (
	item TEXT    NOT NULL,
	ts   INTEGER NOT NULL, -- unix seconds
	on_  INTEGER NOT NULL,
	PRIMARY KEY (item, ts)
);
CREATE TABLE IF NOT EXISTS displays (
	source         TEXT    NOT NULL,
	ts             INTEGER NOT NULL, -- unix seconds
	brightness     INTEGER NOT NULL,
	max_brightness INTEGER NOT NULL,
	on_            INTEGER NOT NULL, -- DPM on
	PRIMARY KEY (source, ts)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS displays_ts ON displays (ts);
`

// Open opens or creates the store at path, creating its directory.
// WAL mode and a busy timeout let the daemon write while the TUI reads.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store %s: %w", path, err)
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store %s: %w", path, err)
	}
	if _, err := db.Exec(schema + networkSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// AddSample inserts or replaces the sample for (source, ts).
func (s *Store) AddSample(x Sample) error { return s.AddSamples([]Sample{x}) }

// AddSamples inserts or replaces samples in one transaction.
func (s *Store) AddSamples(xs []Sample) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(`INSERT OR REPLACE INTO samples (ts, source, watts, seconds) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, x := range xs {
		if _, err := st.Exec(x.TS.Unix(), x.Source, x.Watts, x.Seconds); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Samples returns samples with from < ts <= to, oldest first. An empty
// source means every source.
func (s *Store) Samples(source string, from, to time.Time) ([]Sample, error) {
	q := `SELECT ts, source, watts, seconds FROM samples WHERE ts > ? AND ts <= ?`
	args := []any{from.Unix(), to.Unix()}
	if source != "" {
		q += ` AND source = ?`
		args = append(args, source)
	}
	rows, err := s.db.Query(q+` ORDER BY ts, source`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var x Sample
		var ts int64
		if err := rows.Scan(&ts, &x.Source, &x.Watts, &x.Seconds); err != nil {
			return nil, err
		}
		x.TS = time.Unix(ts, 0)
		out = append(out, x)
	}
	return out, rows.Err()
}

// LatestSample returns the newest sample of source.
func (s *Store) LatestSample(source string) (Sample, bool, error) {
	var x Sample
	var ts int64
	err := s.db.QueryRow(`SELECT ts, source, watts, seconds FROM samples WHERE source = ? ORDER BY ts DESC LIMIT 1`, source).
		Scan(&ts, &x.Source, &x.Watts, &x.Seconds)
	if err == sql.ErrNoRows {
		return Sample{}, false, nil
	}
	if err != nil {
		return Sample{}, false, err
	}
	x.TS = time.Unix(ts, 0)
	return x, true, nil
}

// Sources lists the sources that have samples, sorted.
func (s *Store) Sources() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT source FROM samples ORDER BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var src string
		if err := rows.Scan(&src); err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// AddDisplay inserts or replaces the reading for (source, ts).
func (s *Store) AddDisplay(x Display) error { return s.AddDisplays([]Display{x}) }

// AddDisplays inserts or replaces readings in one transaction.
func (s *Store) AddDisplays(xs []Display) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(`INSERT OR REPLACE INTO displays (source, ts, brightness, max_brightness, on_) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, x := range xs {
		if _, err := st.Exec(x.Source, x.TS.Unix(), x.Brightness, x.MaxBrightness, x.On); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Displays returns readings with from < ts <= to, oldest first. An empty
// source means every source.
func (s *Store) Displays(source string, from, to time.Time) ([]Display, error) {
	q := `SELECT source, ts, brightness, max_brightness, on_ FROM displays WHERE ts > ? AND ts <= ?`
	args := []any{from.Unix(), to.Unix()}
	if source != "" {
		q += ` AND source = ?`
		args = append(args, source)
	}
	rows, err := s.db.Query(q+` ORDER BY ts, source`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Display
	for rows.Next() {
		var x Display
		var ts int64
		if err := rows.Scan(&x.Source, &ts, &x.Brightness, &x.MaxBrightness, &x.On); err != nil {
			return nil, err
		}
		x.TS = time.Unix(ts, 0)
		out = append(out, x)
	}
	return out, rows.Err()
}

// UpsertDaily inserts or replaces the aggregate for (day, source), so
// re-running an import is idempotent.
func (s *Store) UpsertDaily(a DailyAggregate) error {
	if _, err := time.Parse(DayLayout, a.Day); err != nil {
		return fmt.Errorf("store: day %q must be YYYY-MM-DD", a.Day)
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO daily (day, source, wh, on_seconds, monitor_seconds, gpu_wh, cpu_wh, origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, a.Day, a.Source, a.Wh, a.OnSeconds, a.MonitorSeconds, a.GPUWh, a.CPUWh, a.Origin)
	return err
}

// Daily returns aggregates with fromDay <= day <= toDay (YYYY-MM-DD),
// ordered by day then source.
func (s *Store) Daily(fromDay, toDay string) ([]DailyAggregate, error) {
	rows, err := s.db.Query(`SELECT day, source, wh, on_seconds, monitor_seconds, gpu_wh, cpu_wh, origin
		FROM daily WHERE day >= ? AND day <= ? ORDER BY day, source`, fromDay, toDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyAggregate
	for rows.Next() {
		var a DailyAggregate
		if err := rows.Scan(&a.Day, &a.Source, &a.Wh, &a.OnSeconds, &a.MonitorSeconds, &a.GPUWh, &a.CPUWh, &a.Origin); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddToggle records a manual item switching on or off.
func (s *Store) AddToggle(t Toggle) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO toggles (item, ts, on_) VALUES (?, ?, ?)`, t.Item, t.TS.Unix(), t.On)
	return err
}

// ToggleState returns the latest state of a manual item. ok is false when it
// was never toggled (it is off).
func (s *Store) ToggleState(item string) (on, ok bool, err error) {
	err = s.db.QueryRow(`SELECT on_ FROM toggles WHERE item = ? ORDER BY ts DESC LIMIT 1`, item).Scan(&on)
	if err == sql.ErrNoRows {
		return false, false, nil
	}
	return on, err == nil, err
}

// Toggles returns every toggle with ts < before, oldest first, by item.
func (s *Store) Toggles(before time.Time) (map[string][]Toggle, error) {
	rows, err := s.db.Query(`SELECT item, ts, on_ FROM toggles WHERE ts < ? ORDER BY ts, item`, before.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Toggle{}
	for rows.Next() {
		var t Toggle
		var ts int64
		if err := rows.Scan(&t.Item, &ts, &t.On); err != nil {
			return nil, err
		}
		t.TS = time.Unix(ts, 0)
		out[t.Item] = append(out[t.Item], t)
	}
	return out, rows.Err()
}

// Load returns the data energy needs for [from, to): samples with
// from < ts <= to+margin (pass 3 × the sample interval so samples covering
// the window edge are included), aggregates for the local days touching the
// window, all toggles before to (state carries over from earlier ones), and
// display and network readings with from − margin < ts <= to (a reading
// covers time after it, so one just before from reaches into the window).
func (s *Store) Load(from, to time.Time, margin time.Duration) (Data, error) {
	d := Data{Samples: map[string][]Sample{}, Daily: map[string][]DailyAggregate{}}
	xs, err := s.Samples("", from, to.Add(margin))
	if err != nil {
		return Data{}, err
	}
	for _, x := range xs {
		d.Samples[x.Source] = append(d.Samples[x.Source], x)
	}
	as, err := s.Daily(from.Format(DayLayout), to.Format(DayLayout))
	if err != nil {
		return Data{}, err
	}
	for _, a := range as {
		d.Daily[a.Source] = append(d.Daily[a.Source], a)
	}
	if d.Toggles, err = s.Toggles(to); err != nil {
		return Data{}, err
	}
	if d.Displays, err = s.Displays("", from.Add(-margin), to); err != nil {
		return Data{}, err
	}
	ns, err := s.NetworkReadings("", from.Add(-margin), to)
	if err != nil {
		return Data{}, err
	}
	for _, x := range ns {
		if d.Network == nil {
			d.Network = map[string][]NetworkReading{}
		}
		d.Network[x.Item] = append(d.Network[x.Item], x)
	}
	return d, nil
}
