package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kevnnard/watts/internal/discover"
)

// tvItem and ps5Item are network items as the add flow writes them, with
// RFC 5737 addresses.
var (
	tvItem = Item{Name: "TV", Mode: ModeNetwork, Source: SourceLinux, Probe: ProbeSamsung, Host: "192.0.2.16",
		ID: "uuid:0f1e2d3c-aaaa-bbbb-cccc-000000000016", Watts: 63, StandbyW: 0.5, Quantity: 1, Brand: "Samsung", Model: "QN55Q60DAKXZL"}
	ps5Item = Item{Name: "PS5", Mode: ModeNetwork, Source: SourceMac, Probe: ProbePS5, Host: "192.0.2.40",
		ID: "ps:00AABBCCDDEE", Watts: 217, StandbyW: 1.2, Quantity: 1}
)

func withItems(its ...Item) *Config {
	c := validConfig()
	c.Items = append(c.Items, its...)
	return c
}

func TestProbesMatchDiscover(t *testing.T) {
	if ProbeSamsung != discover.ProbeSamsung || ProbePS5 != discover.ProbePS5 {
		t.Fatalf("probes %q %q differ from discover's", ProbeSamsung, ProbePS5)
	}
}

func TestValidateNetwork(t *testing.T) {
	noID := ps5Item
	noID.Name, noID.ID = "PS5 no id", ""
	zero := tvItem
	zero.Name, zero.StandbyW, zero.ID = "TV zero standby", 0, "mac:AA-BB-CC-00-00-16"
	linkLocal := tvItem
	linkLocal.Name, linkLocal.Host, linkLocal.ID = "TV link-local", "169.254.10.16", ""
	for _, it := range []Item{tvItem, ps5Item, noID, zero, linkLocal} {
		if err := withItems(it).Validate(); err != nil {
			t.Errorf("%s: Validate() = %v", it.Name, err)
		}
	}
	tests := []struct {
		name   string
		mutate func(it *Item)
		want   string
	}{
		{"no source", func(it *Item) { it.Source = "" }, `item "TV": source must be "mac" or "linux" (the machine whose daemon asks the device), got ""`},
		{"bad source", func(it *Item) { it.Source = "router" }, `item "TV": source must be "mac" or "linux" (the machine whose daemon asks the device), got "router"`},
		{"no probe", func(it *Item) { it.Probe = "" }, `item "TV": probe must be "samsung" or "ps5", got ""`},
		{"unknown probe", func(it *Item) { it.Probe = "roku" }, `item "TV": probe must be "samsung" or "ps5", got "roku"`},
		{"no host", func(it *Item) { it.Host = "" }, `item "TV": host "" must be one IPv4 address, such as 192.0.2.16`},
		{"host name", func(it *Item) { it.Host = "tv.lan" }, `item "TV": host "tv.lan" must be one IPv4 address, such as 192.0.2.16`},
		{"host with port", func(it *Item) { it.Host = "192.0.2.16:8001" }, `item "TV": host "192.0.2.16:8001" must be one IPv4 address, such as 192.0.2.16`},
		{"host with scheme", func(it *Item) { it.Host = "http://192.0.2.16" }, `item "TV": host "http://192.0.2.16" must be one IPv4 address, such as 192.0.2.16`},
		{"IPv6 host", func(it *Item) { it.Host = "2001:db8::16" }, `item "TV": host "2001:db8::16" must be one IPv4 address, such as 192.0.2.16`},
		{"mapped IPv4", func(it *Item) { it.Host = "::ffff:192.0.2.16" }, `item "TV": host "::ffff:192.0.2.16" must be one IPv4 address, such as 192.0.2.16`},
		{"broadcast host", func(it *Item) { it.Host = "255.255.255.255" }, `item "TV": host "255.255.255.255" must be one IPv4 address, such as 192.0.2.16`},
		{"multicast host", func(it *Item) { it.Host = "239.255.255.250" }, `item "TV": host "239.255.255.250" must be one IPv4 address, such as 192.0.2.16`},
		{"loopback host", func(it *Item) { it.Host = "127.0.0.1" }, `item "TV": host "127.0.0.1" must be one IPv4 address, such as 192.0.2.16`},
		{"host id", func(it *Item) { it.ID = "host:192.0.2.16" }, `item "TV": id "host:192.0.2.16" is not a stable samsung identity (want uuid:… or mac:…)`},
		{"mdns id", func(it *Item) { it.ID = "mdns:Living-TV" }, `item "TV": id "mdns:Living-TV" is not a stable samsung identity (want uuid:… or mac:…)`},
		{"ps id on samsung", func(it *Item) { it.ID = "ps:00AABBCCDDEE" }, `item "TV": id "ps:00AABBCCDDEE" is not a stable samsung identity (want uuid:… or mac:…)`},
		{"bad mac", func(it *Item) { it.ID = "mac:not-a-mac" }, `item "TV": id "mac:not-a-mac" is not a stable samsung identity (want uuid:… or mac:…)`},
		{"id with space", func(it *Item) { it.ID = "uuid:a b" }, `item "TV": id "uuid:a b" is not a stable samsung identity (want uuid:… or mac:…)`},
		{"no watts", func(it *Item) { it.Watts = 0 }, `item "TV": watts must be greater than 0`},
		{"negative standby", func(it *Item) { it.StandbyW = -1 }, `item "TV": standby_w must not be negative, got -1`},
		{"standby above watts", func(it *Item) { it.StandbyW = 70 }, `item "TV": standby_w must not exceed watts, got standby_w 70, watts 63`},
		{"ddc range", func(it *Item) { it.MaxW = 70 }, `item "TV": min_w and max_w need ddc = "linux"; a network item uses watts and standby_w`},
		{"ddc", func(it *Item) { it.DDC = SourceLinux }, `item "TV": ddc is only for follows items, got mode "network"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := tvItem
			tt.mutate(&it)
			if err := withItems(it).Validate(); err == nil || err.Error() != tt.want {
				t.Fatalf("Validate() = %v, want %q", err, tt.want)
			}
		})
	}
	t.Run("ps5 wants a ps id", func(t *testing.T) {
		it := ps5Item
		it.ID = "uuid:0f1e"
		want := `item "PS5": id "uuid:0f1e" is not a stable ps5 identity (want ps:…)`
		if err := withItems(it).Validate(); err == nil || err.Error() != want {
			t.Fatalf("Validate() = %v, want %q", err, want)
		}
	})
	t.Run("probe, host and id belong to network items", func(t *testing.T) {
		for _, lamp := range []Item{
			{Name: "Lamp", Mode: ModeManual, Watts: 9, Probe: ProbePS5},
			{Name: "Lamp", Mode: ModeManual, Watts: 9, Host: "192.0.2.9"},
			{Name: "Lamp", Mode: ModeManual, Watts: 9, ID: "ps:00"},
		} {
			want := `item "Lamp": probe, host and id are only for network items, got mode "manual"`
			if err := withItems(lamp).Validate(); err == nil || err.Error() != want {
				t.Errorf("%+v: Validate() = %v, want %q", lamp, err, want)
			}
		}
	})
	t.Run("standby_w without ddc stays an error on other modes", func(t *testing.T) {
		lamp := Item{Name: "Lamp", Mode: ModeManual, Watts: 9, StandbyW: 0.5}
		want := `item "Lamp": min_w, max_w and standby_w need ddc = "linux"`
		if err := withItems(lamp).Validate(); err == nil || err.Error() != want {
			t.Errorf("Validate() = %v, want %q", err, want)
		}
	})
	t.Run("a network item cannot be followed", func(t *testing.T) {
		c := withItems(ps5Item)
		c.Items[2].Follows = []string{"PS5"}
		want := `item "LG 29WQ600": follows "PS5", which is not a measured item`
		if err := c.Validate(); err == nil || err.Error() != want {
			t.Errorf("Validate() = %v, want %q", err, want)
		}
	})
	t.Run("one id names one item", func(t *testing.T) {
		other := tvItem
		other.Name, other.ID, other.Host = "TV bedroom", "UUID:0F1E2D3C-AAAA-BBBB-CCCC-000000000016", "192.0.2.17"
		want := `item "TV bedroom": id uuid:0f1e2d3c-aaaa-bbbb-cccc-000000000016 is also item "TV"'s`
		if err := withItems(tvItem, other).Validate(); err == nil || err.Error() != want {
			t.Errorf("Validate() = %v, want %q", err, want)
		}
		// Two items without an ID, or at one host, are allowed: the ID is
		// what identifies a device.
		a, b := noID, noID
		b.Name = "PS5 second"
		if err := withItems(a, b).Validate(); err != nil {
			t.Errorf("two items without id: %v", err)
		}
	})
}

func TestNetworkID(t *testing.T) {
	tests := []struct {
		probe, id, want string
		ok              bool
	}{
		{ProbeSamsung, " UUID:ABC-def ", "uuid:abc-def", true},
		{ProbeSamsung, "mac:AA-BB-CC-00-00-16", "mac:aa:bb:cc:00:00:16", true},
		{ProbeSamsung, "MAC:aabb.cc00.0016", "mac:aa:bb:cc:00:00:16", true},
		{ProbePS5, "ps:00AABBCCDDEE", "ps:00aabbccddee", true},
		{ProbePS5, "mac:aa:bb:cc:00:00:40", "", false},
		{ProbeSamsung, "mac:aa:bb:cc:00:00:00:00:16", "", false}, // EUI-64 is not a MAC
		{ProbeSamsung, "uuid:", "", false},
		{ProbeSamsung, "host:192.0.2.16", "", false},
		{ProbeSamsung, "mdns:tv", "", false},
		{"roku", "uuid:abc", "", false},
		{ProbeSamsung, "", "", false},
		{ProbeSamsung, "uuid:ab\u0085c", "", false},  // C1 control
		{ProbeSamsung, "uuid:ab\u200bc", "", false},  // zero-width space
		{ProbePS5, "ps:00aabbccdd\u00e9", "", false}, // not ASCII
	}
	for _, tt := range tests {
		got, ok := NetworkID(tt.probe, tt.id)
		if got != tt.want || ok != tt.ok {
			t.Errorf("NetworkID(%q, %q) = %q, %v, want %q, %v", tt.probe, tt.id, got, ok, tt.want, tt.ok)
		}
	}
}

func TestLoadNetworkItemsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `[[items]]
name = "TV"
mode = "network"
source = "linux"
probe = "samsung"
host = "192.0.2.16"
id = "uuid:0f1e2d3c-aaaa-bbbb-cccc-000000000016"
watts = 63
standby_w = 0.5

[[items]]
name = "PS5"
mode = "network"
source = "mac"
probe = "ps5"
host = "192.0.2.40"
watts = 217
standby_w = 0
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []Item{
		{Name: "TV", Mode: ModeNetwork, Source: SourceLinux, Probe: ProbeSamsung, Host: "192.0.2.16",
			ID: "uuid:0f1e2d3c-aaaa-bbbb-cccc-000000000016", Watts: 63, StandbyW: 0.5, Quantity: 1},
		// An explicit standby_w = 0 stays 0: no catalog value replaces it.
		{Name: "PS5", Mode: ModeNetwork, Source: SourceMac, Probe: ProbePS5, Host: "192.0.2.40", Watts: 217, Quantity: 1},
	}
	if !reflect.DeepEqual(c.Items, want) {
		t.Fatalf("items = %+v, want %+v", c.Items, want)
	}
	if err := Save(path, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	saved, _ := os.ReadFile(path)
	for _, s := range []string{`mode = "network"`, `probe = "samsung"`, `host = "192.0.2.16"`, `id = "uuid:0f1e2d3c-aaaa-bbbb-cccc-000000000016"`, `standby_w = 0.5`} {
		if !strings.Contains(string(saved), s) {
			t.Errorf("saved config lacks %s:\n%s", s, saved)
		}
	}
	again, err := Load(path)
	if err != nil || !reflect.DeepEqual(again.Items, want) {
		t.Fatalf("reload = %+v, %v", again, err)
	}
}

func TestSaveRejectsInvalidNetworkItemWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	bad := tvItem
	bad.ID = "host:192.0.2.16"
	if err := Save(path, withItems(bad)); err == nil {
		t.Fatal("Save accepted a host: id")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("a rejected Save changed the file")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("a rejected Save left files: %v", entries)
	}
}
