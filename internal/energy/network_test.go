package energy

import (
	"reflect"
	"testing"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/store"
	"github.com/kevnnard/watts/internal/tariff"
)

// tv is a network item: 63 W on, 0.5 W in standby.
var tv = config.Item{Name: "TV", Mode: config.ModeNetwork, Source: "linux", Probe: config.ProbeSamsung, Host: "192.0.2.16",
	Watts: 63, StandbyW: 0.5, Quantity: 1}

// states returns a reading of item every minute at from, from+1m, … before to.
func states(item string, from, to time.Time, state string) []store.NetworkReading {
	var out []store.NetworkReading
	for t := from; t.Before(to); t = t.Add(time.Minute) {
		out = append(out, store.NetworkReading{TS: t, Item: item, State: state, Host: "192.0.2.16"})
	}
	return out
}

func netData(xs ...[]store.NetworkReading) store.Data {
	d := store.Data{Network: map[string][]store.NetworkReading{}}
	for _, x := range xs {
		for _, r := range x {
			d.Network[r.Item] = append(d.Network[r.Item], r)
		}
	}
	return d
}

func TestNetworkEnergy(t *testing.T) {
	tv2 := tv
	tv2.Quantity = 2
	zeroStandby := tv
	zeroStandby.StandbyW = 0
	tests := []struct {
		name      string
		data      store.Data
		item      config.Item
		from, to  time.Time
		wh        float64
		onHours   float64
		unknownHr float64
	}{
		{"on: watts", netData(states("TV", at(8, 20, 0), at(8, 21, 0), store.NetworkOn)), tv, at(8, 20, 0), at(8, 21, 0), 63, 1, 0},
		{"standby: standby_w, not on", netData(states("TV", at(8, 20, 0), at(8, 21, 0), store.NetworkStandby)), tv, at(8, 20, 0), at(8, 21, 0), 0.5, 0, 0},
		{"standby_w 0 is 0", netData(states("TV", at(8, 20, 0), at(8, 21, 0), store.NetworkStandby)), zeroStandby, at(8, 20, 0), at(8, 21, 0), 0, 0, 0},
		{"quantity multiplies", netData(states("TV", at(8, 20, 0), at(8, 21, 0), store.NetworkOn)), tv2, at(8, 20, 0), at(8, 21, 0), 126, 1, 0},
		{"unknown counts nothing and is unknown, not off",
			netData(states("TV", at(8, 20, 0), at(8, 21, 0), store.NetworkUnknown)), tv, at(8, 20, 0), at(8, 21, 0), 0, 0, 1},
		{"no reading at all: unknown, not zero proven",
			store.Data{}, tv, at(8, 20, 0), at(8, 21, 0), 0, 0, 1},
		{"partial: 30 min on, 30 min unknown, then nothing",
			netData(states("TV", at(8, 20, 0), at(8, 20, 30), store.NetworkOn), states("TV", at(8, 20, 30), at(8, 21, 0), store.NetworkUnknown)),
			tv, at(8, 20, 0), at(8, 22, 0), 31.5, 0.5, 1.5},
		{"a reading covers up to the next one within 3 intervals, else one interval",
			netData([]store.NetworkReading{
				{TS: at(8, 20, 0), Item: "TV", State: store.NetworkOn}, // to 20:03
				{TS: at(8, 20, 3), Item: "TV", State: store.NetworkOn}, // gap 7 min: 1 min
				{TS: at(8, 20, 10), Item: "TV", State: store.NetworkOn},
			}), tv, at(8, 20, 0), at(8, 21, 0), 63 * 5 / 60.0, 5 / 60.0, 55 / 60.0},
		{"clipped to the window",
			netData(states("TV", at(8, 20, 0), at(8, 21, 0), store.NetworkOn)), tv, at(8, 20, 30), at(8, 21, 0), 31.5, 0.5, 0},
		{"a reading before the window reaches into it",
			netData([]store.NetworkReading{{TS: at(8, 19, 59).Add(30 * time.Second), Item: "TV", State: store.NetworkOn}}),
			tv, at(8, 20, 0), at(8, 20, 1), 63 * 0.5 / 60, 0.5 / 60, 0.5 / 60},
		{"an unrecognized state is unknown",
			netData([]store.NetworkReading{{TS: at(8, 20, 0), Item: "TV", State: "off"}}), tv, at(8, 20, 0), at(8, 20, 1), 0, 0, 1 / 60.0},
		{"readings of another item do not count",
			netData(states("PS5", at(8, 20, 0), at(8, 21, 0), store.NetworkOn)), tv, at(8, 20, 0), at(8, 21, 0), 0, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ItemEnergy(input(tt.data, tt.item), tt.item, tt.from, tt.to)
			if !near(r.Wh, tt.wh) || !near(r.OnSeconds, tt.onHours*3600) || !near(r.UnknownSeconds, tt.unknownHr*3600) {
				t.Errorf("ItemEnergy = %+v, want %v Wh, %v h on, %v h unknown", r, tt.wh, tt.onHours, tt.unknownHr)
			}
		})
	}
}

func TestNetworkAcrossMidnight(t *testing.T) {
	data := netData(states("TV", at(8, 23, 30), at(9, 0, 30), store.NetworkOn))
	days := Days(input(data, tv), at(8, 0, 0), at(10, 0, 0), 1000)
	if len(days) != 2 || !near(days[0].Wh, 31.5) || !near(days[1].Wh, 31.5) {
		t.Fatalf("days = %+v", days)
	}
}

func TestOtherModesHaveNoUnknownTime(t *testing.T) {
	data := store.Data{Samples: map[string][]store.Sample{"mac": run("mac", at(8, 10, 0), at(8, 11, 0), 30)}}
	in := input(data, mac, pc, lg, lights, fan)
	tot := Window(in, at(8, 0, 0), at(9, 0, 0), 1000)
	if tot.Incomplete {
		t.Errorf("Window without network items is incomplete: %+v", tot)
	}
	for _, it := range tot.Items {
		if it.UnknownSeconds != 0 {
			t.Errorf("%s has %v unknown seconds", it.Name, it.UnknownSeconds)
		}
	}
}

func TestNowNetwork(t *testing.T) {
	now := at(8, 20, 0)
	tests := []struct {
		name    string
		data    store.Data
		watts   float64
		on      bool
		offline bool
		net     *NetworkState
	}{
		{"fresh on", netData([]store.NetworkReading{
			{TS: at(8, 19, 0), Item: "TV", State: store.NetworkStandby},
			{TS: at(8, 19, 58), Item: "TV", State: store.NetworkOn, Host: "192.0.2.17", Verified: true},
		}), 63, true, false, &NetworkState{State: store.NetworkOn, TS: at(8, 19, 58), Host: "192.0.2.17", Verified: true}},
		{"fresh standby: counted at standby watts", netData([]store.NetworkReading{
			{TS: at(8, 19, 59), Item: "TV", State: store.NetworkStandby, Host: "192.0.2.16"},
		}), 0.5, true, false, &NetworkState{State: store.NetworkStandby, TS: at(8, 19, 59), Host: "192.0.2.16"}},
		{"fresh unknown: no watts, not on, offline with the unknown state", netData([]store.NetworkReading{
			{TS: at(8, 19, 0), Item: "TV", State: store.NetworkOn},
			{TS: at(8, 19, 59), Item: "TV", State: store.NetworkUnknown, Host: "192.0.2.16"},
		}), 0, false, true, &NetworkState{State: store.NetworkUnknown, TS: at(8, 19, 59), Host: "192.0.2.16"}},
		{"stale on: offline, no state", netData([]store.NetworkReading{
			{TS: at(8, 19, 56), Item: "TV", State: store.NetworkOn},
		}), 0, false, true, nil},
		{"no reading: offline", store.Data{}, 0, false, true, nil},
		{"a reading after now is not used", netData([]store.NetworkReading{
			{TS: at(8, 20, 1), Item: "TV", State: store.NetworkOn},
		}), 0, false, true, nil},
	}
	for _, tt := range tests {
		got := Now(input(tt.data, tv), tv, now)
		if !near(got.Watts, tt.watts) || got.On != tt.on || got.Offline != tt.offline || !reflect.DeepEqual(got.Network, tt.net) {
			t.Errorf("%s: Now = %+v (network %+v), want %v W on %v offline %v network %+v",
				tt.name, got, got.Network, tt.watts, tt.on, tt.offline, tt.net)
		}
	}
}

func TestBuildFlagsIncompleteNetwork(t *testing.T) {
	now := at(8, 20, 0)
	data := netData(states("TV", at(8, 19, 0), at(8, 20, 0), store.NetworkOn))
	data.Samples = map[string][]store.Sample{"mac": run("mac", at(8, 19, 0), at(8, 20, 0), 30)}
	r := Build(input(data, mac, tv), tariff.Seed(), now)
	if !near(r.TotalWatts, 30+63) {
		t.Errorf("TotalWatts = %v, want 93", r.TotalWatts)
	}
	// Today's TV is known for one hour of twenty: the totals say so.
	if !r.Today.Incomplete || !r.Bill.Incomplete || !near(r.Today.Items[1].UnknownSeconds, 19*3600) || r.Today.Items[0].UnknownSeconds != 0 {
		t.Errorf("today = %+v", r.Today)
	}
	if !near(r.Today.Wh, 30+63) {
		t.Errorf("today Wh = %v, want 93", r.Today.Wh)
	}
	full := netData(states("TV", at(8, 0, 0), at(8, 20, 0), store.NetworkStandby))
	r = Build(input(full, tv), tariff.Seed(), now)
	if r.Today.Incomplete || r.Today.Items[0].UnknownSeconds != 0 || !near(r.Today.Wh, 0.5*20) {
		t.Errorf("fully covered today = %+v", r.Today)
	}
}
