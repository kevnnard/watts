package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNetworkReadings(t *testing.T) {
	s := open(t)
	xs := []NetworkReading{
		{TS: t0.Add(time.Minute), Item: "TV", State: NetworkOn, Host: "192.0.2.16", ID: "uuid:abc", Verified: true},
		{TS: t0.Add(time.Minute), Item: "PS5", State: NetworkUnknown, Host: "192.0.2.40"},
		{TS: t0.Add(2 * time.Minute), Item: "TV", State: NetworkStandby, Host: "192.0.2.16", ID: "uuid:abc", Verified: true},
		{TS: t0.Add(3 * time.Minute), Item: "TV", State: NetworkUnknown, Host: "192.0.2.16"},
		{TS: t0.Add(time.Hour), Item: "TV", State: NetworkOn, Host: "192.0.2.17", ID: "uuid:abc", Verified: true},
	}
	if err := s.AddNetwork(xs); err != nil {
		t.Fatal(err)
	}
	// Writing the same rows again changes nothing: (item, ts) is the key.
	if err := s.AddNetwork(xs[:2]); err != nil {
		t.Fatal(err)
	}
	got, err := s.NetworkReadings("TV", t0.Add(time.Minute), t0.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for i := range got {
		got[i].TS = got[i].TS.In(time.Local)
	}
	if want := xs[2:4]; !reflect.DeepEqual(got, want) {
		t.Fatalf("NetworkReadings (from, to] = %+v, want %+v", got, want)
	}
	all, err := s.NetworkReadings("", t0, t0.Add(2*time.Hour))
	if err != nil || len(all) != 5 {
		t.Fatalf("all readings = %+v, %v", all, err)
	}
	// Load reaches back by the margin, because a reading covers the time
	// after it, and keeps unknown readings: they are not missing ones.
	d, err := s.Load(t0.Add(2*time.Minute+30*time.Second), t0.Add(30*time.Minute), 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if tv := d.Network["TV"]; len(tv) != 2 || tv[0].State != NetworkStandby || tv[1].State != NetworkUnknown {
		t.Errorf("Load TV = %+v", tv)
	}
	if ps := d.Network["PS5"]; len(ps) != 0 {
		t.Errorf("Load PS5 = %+v, want none: it is before from − margin", ps)
	}
	d, err = s.Load(t0.Add(2*time.Minute), t0.Add(30*time.Minute), 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if ps := d.Network["PS5"]; len(ps) != 1 || ps[0].State != NetworkUnknown {
		t.Errorf("Load PS5 = %+v", ps)
	}
}

func TestAddNetworkRejectsBadRowsWithoutWriting(t *testing.T) {
	s := open(t)
	for _, bad := range []NetworkReading{
		{TS: t0, Item: "TV", State: "off"},
		{TS: t0, Item: "TV", State: ""},
		{TS: t0, Item: "", State: NetworkOn},
	} {
		err := s.AddNetwork([]NetworkReading{{TS: t0.Add(-time.Minute), Item: "TV", State: NetworkOn}, bad})
		if err == nil || !strings.HasPrefix(err.Error(), "store: network reading") {
			t.Errorf("AddNetwork(%+v) = %v", bad, err)
		}
	}
	if got, _ := s.NetworkReadings("", t0.Add(-time.Hour), t0.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("a rejected batch wrote %+v", got)
	}
}

// A store from v0.2.0, before the network table, opens with every row and
// table as it was and takes network readings; opening again is a no-op.
func TestOpenStoreBeforeNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watts.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	const v020 = `
CREATE TABLE samples (ts INTEGER NOT NULL, source TEXT NOT NULL, watts REAL NOT NULL, seconds REAL NOT NULL, PRIMARY KEY (source, ts)) WITHOUT ROWID;
CREATE INDEX samples_ts ON samples (ts);
CREATE TABLE daily (day TEXT NOT NULL, source TEXT NOT NULL, wh REAL NOT NULL DEFAULT 0, on_seconds REAL NOT NULL DEFAULT 0,
	monitor_seconds REAL NOT NULL DEFAULT 0, gpu_wh REAL NOT NULL DEFAULT 0, cpu_wh REAL NOT NULL DEFAULT 0, origin TEXT NOT NULL DEFAULT '', PRIMARY KEY (day, source));
CREATE TABLE toggles (item TEXT NOT NULL, ts INTEGER NOT NULL, on_ INTEGER NOT NULL, PRIMARY KEY (item, ts));
CREATE TABLE displays (source TEXT NOT NULL, ts INTEGER NOT NULL, brightness INTEGER NOT NULL, max_brightness INTEGER NOT NULL, on_ INTEGER NOT NULL, PRIMARY KEY (source, ts)) WITHOUT ROWID;
CREATE INDEX displays_ts ON displays (ts);
INSERT INTO samples VALUES (1794150060, 'linux', 70.5, 60);
INSERT INTO daily (day, source, wh) VALUES ('2026-11-01', 'linux', 1500);
INSERT INTO toggles VALUES ('Fan', 1794150000, 1);
INSERT INTO displays VALUES ('linux', 1794150060, 65, 100, 1);`
	if _, err := db.Exec(v020); err != nil {
		t.Fatal(err)
	}
	dump := func() string {
		t.Helper()
		db, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var b strings.Builder
		for _, q := range []string{
			`SELECT ts || source || watts || seconds FROM samples`,
			`SELECT day || source || wh || on_seconds || origin FROM daily`,
			`SELECT item || ts || on_ FROM toggles`,
			`SELECT source || ts || brightness || max_brightness || on_ FROM displays`,
			`SELECT name || sql FROM sqlite_master WHERE name NOT LIKE 'network%' ORDER BY name`,
		} {
			rows, err := db.Query(q)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var s string
				rows.Scan(&s)
				b.WriteString(s + "\n")
			}
			rows.Close()
		}
		return b.String()
	}
	before := dump()
	db.Close()

	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open v0.2.0 db: %v", err)
		}
		r := NetworkReading{TS: time.Unix(1794150060, 0), Item: "TV", State: NetworkStandby, Host: "192.0.2.16"}
		if err := s.AddNetwork([]NetworkReading{r}); err != nil {
			t.Fatalf("AddNetwork on v0.2.0 db: %v", err)
		}
		if got, _ := s.NetworkReadings("", time.Unix(0, 0), r.TS); len(got) != 1 {
			t.Errorf("network readings = %+v", got)
		}
		s.Close()
		if after := dump(); after != before {
			t.Fatalf("opening changed the old tables:\nbefore %s\nafter %s", before, after)
		}
	}
}
