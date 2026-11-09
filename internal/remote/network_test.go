package remote

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kevnnard/watts/internal/store"
)

func TestNetworkExportAndMerge(t *testing.T) {
	st := openStore(t)
	if err := st.AddNetwork([]store.NetworkReading{
		{TS: min_(0), Item: "TV", State: store.NetworkOn, Host: "192.0.2.16"},
		{TS: min_(1), Item: "TV", State: store.NetworkStandby, Host: "192.0.2.16", ID: "uuid:abc", Verified: true},
		{TS: min_(2), Item: "PS5", State: store.NetworkUnknown, Host: "192.0.2.40"},
	}); err != nil {
		t.Fatal(err)
	}
	e, err := Build(st, min_(0), min_(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Network) != 2 || e.Network[0].State != store.NetworkStandby || e.Network[1].State != store.NetworkUnknown {
		t.Fatalf("network = %+v, want (from, to] with the unknown reading kept", e.Network)
	}
	b, _ := json.Marshal(e)
	var back Export
	if err := json.Unmarshal(b, &back); err != nil || len(back.Network) != 2 || !back.Network[0].Verified ||
		back.Network[0].ID != "uuid:abc" || !back.Network[1].TS.Equal(min_(2)) {
		t.Fatalf("round trip = %+v, %v", back.Network, err)
	}
	// An empty store omits the key, so the export is what v0.2.0 prints.
	empty, _ := Build(openStore(t), min_(0), min_(2))
	if b, _ := json.Marshal(empty); strings.Contains(string(b), "network") {
		t.Errorf("empty export has network: %s", b)
	}

	d := store.Data{Network: map[string][]store.NetworkReading{"TV": {
		{TS: min_(1), Item: "TV", State: store.NetworkOn},
		{TS: min_(5), Item: "TV", State: store.NetworkOn},
	}}}
	Merge(&d, e)
	Merge(&d, e) // merging twice is the same as once
	want := map[string][]store.NetworkReading{
		"TV": {
			{TS: min_(1), Item: "TV", State: store.NetworkStandby, Host: "192.0.2.16", ID: "uuid:abc", Verified: true}, // remote wins
			{TS: min_(5), Item: "TV", State: store.NetworkOn},
		},
		"PS5": {{TS: min_(2), Item: "PS5", State: store.NetworkUnknown, Host: "192.0.2.40"}},
	}
	for _, xs := range d.Network {
		for i := range xs {
			xs[i].TS = xs[i].TS.In(t0.Location())
		}
	}
	if !reflect.DeepEqual(d.Network, want) {
		t.Errorf("merged network =\n %+v\nwant\n %+v", d.Network, want)
	}
}

// A v1 export from v0.2.0, without network readings, still decodes and
// merges without touching the local ones.
func TestFetchExportWithoutNetwork(t *testing.T) {
	old := []byte(`{"version":1,"from":"2026-11-08T10:00:00Z","to":"2026-11-08T10:05:00Z",` +
		`"samples":[{"ts":"2026-11-08T10:01:00Z","source":"linux","watts":150,"seconds":60}],"daily":[],"toggles":[],` +
		`"displays":[{"ts":"2026-11-08T10:01:00Z","source":"linux","brightness":65,"max_brightness":100,"on":true}]}`)
	c := &Client{Host: "kevnnard-station", Run: func(context.Context, string, ...string) ([]byte, []byte, error) { return old, nil, nil }}
	e, err := c.Fetch(context.Background(), min_(0), min_(5))
	if err != nil || len(e.Samples) != 1 || len(e.Displays) != 1 || e.Network != nil {
		t.Fatalf("Fetch v0.2.0 export = %+v, %v", e, err)
	}
	local := map[string][]store.NetworkReading{"TV": {{TS: min_(1), Item: "TV", State: store.NetworkOn}}}
	d := store.Data{Network: local}
	Merge(&d, e)
	if !reflect.DeepEqual(d.Network, local) {
		t.Errorf("merge without network changed it: %+v", d.Network)
	}
	d = store.Data{}
	Merge(&d, e)
	if d.Network != nil {
		t.Errorf("merge without network made %+v", d.Network)
	}
}
