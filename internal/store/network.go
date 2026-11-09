package store

import (
	"fmt"
	"time"
)

// Network reading states. The values are stored and exported, so they must
// not change; they are discover.StateOn, StateStandby and StateUnknown.
const (
	NetworkOn      = "on"
	NetworkStandby = "standby"
	// NetworkUnknown is a query that got no usable answer: no answer, an
	// odd answer or another device at the address. It is not off.
	NetworkUnknown = "unknown"
)

// NetworkReading is one power-state query of a network item by the daemon
// (S20), keyed by the item's name. It covers time from TS forward; package
// energy decides how far. Time no reading covers is missing, which is
// different from an unknown reading: then nobody asked.
type NetworkReading struct {
	TS    time.Time `json:"ts"`
	Item  string    `json:"item"`
	State string    `json:"state"`          // NetworkOn, NetworkStandby or NetworkUnknown
	Host  string    `json:"host,omitempty"` // the address asked
	ID    string    `json:"id,omitempty"`   // the identity the device reported
	// Verified is set when the item has an id and the device reported it.
	Verified bool `json:"verified,omitempty"`
}

const networkSchema = `
CREATE TABLE IF NOT EXISTS network (
	item     TEXT    NOT NULL,
	ts       INTEGER NOT NULL, -- unix seconds
	state    TEXT    NOT NULL, -- on, standby or unknown
	host     TEXT    NOT NULL DEFAULT '',
	id       TEXT    NOT NULL DEFAULT '',
	verified INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (item, ts)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS network_ts ON network (ts);
`

func (x NetworkReading) check() error {
	switch {
	case x.Item == "":
		return fmt.Errorf("store: network reading at %s has no item", x.TS.Format(time.RFC3339))
	case x.State != NetworkOn && x.State != NetworkStandby && x.State != NetworkUnknown:
		return fmt.Errorf("store: network reading of %q has state %q, want on, standby or unknown", x.Item, x.State)
	}
	return nil
}

// AddNetwork inserts or replaces readings by (item, ts) in one
// transaction. A bad reading rejects the whole batch.
func (s *Store) AddNetwork(xs []NetworkReading) error {
	for _, x := range xs {
		if err := x.check(); err != nil {
			return err
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(`INSERT OR REPLACE INTO network (item, ts, state, host, id, verified) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, x := range xs {
		if _, err := st.Exec(x.Item, x.TS.Unix(), x.State, x.Host, x.ID, x.Verified); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NetworkReadings returns readings with from < ts <= to, oldest first. An
// empty item means every item.
func (s *Store) NetworkReadings(item string, from, to time.Time) ([]NetworkReading, error) {
	q := `SELECT item, ts, state, host, id, verified FROM network WHERE ts > ? AND ts <= ?`
	args := []any{from.Unix(), to.Unix()}
	if item != "" {
		q += ` AND item = ?`
		args = append(args, item)
	}
	rows, err := s.db.Query(q+` ORDER BY ts, item`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NetworkReading
	for rows.Next() {
		var x NetworkReading
		var ts int64
		if err := rows.Scan(&x.Item, &ts, &x.State, &x.Host, &x.ID, &x.Verified); err != nil {
			return nil, err
		}
		x.TS = time.Unix(ts, 0)
		out = append(out, x)
	}
	return out, rows.Err()
}
