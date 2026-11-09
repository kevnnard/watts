package sensor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kevnnard/watts/internal/discover"
)

const (
	tvHost = "192.0.2.10"
	tvID   = "uuid:0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"
)

// tvJSON is a Samsung api/v2 answer; empty fields are left out.
func tvJSON(state, id, mac string) string {
	d := map[string]string{"name": "Living room TV", "modelName": "QN00X00", "type": "Samsung SmartTV"}
	for k, v := range map[string]string{"PowerState": state, "id": id, "wifiMac": mac} {
		if v != "" {
			d[k] = v
		}
	}
	b, _ := json.Marshal(map[string]any{"device": d, "type": "Samsung SmartTV"})
	return string(b)
}

// samsungAt serves h on a local test server and returns options whose real
// client sends the TV request there, plus the requests as the query made them.
func samsungAt(t *testing.T, h http.HandlerFunc) (NetworkOptions, *[]string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	var mu sync.Mutex
	var asked []string
	client := networkHTTPClient()
	return NetworkOptions{Timeout: time.Second, Now: func() time.Time { return queryAt }, HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.String())
		mu.Unlock()
		r = r.Clone(r.Context())
		r.URL.Host = srv.Listener.Addr().String()
		return client.Do(r)
	})}, &asked
}

func serve(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(body)) }
}

func TestQueryNetworkSamsungState(t *testing.T) {
	tests := []struct {
		name, body, id string
		state          string
		verified       bool
	}{
		{"on", tvJSON("on", tvID, ""), tvID, discover.StateOn, true},
		{"standby", tvJSON("standby", tvID, ""), tvID, discover.StateStandby, true},
		{"prefix and case", tvJSON(" ON ", "0F1E2D3C-4B5A-6978-8796-A5B4C3D2E1F0", ""), "UUID:0F1E2D3C-4B5A-6978-8796-a5b4c3d2e1f0", discover.StateOn, true},
		{"mac alias", tvJSON("standby", tvID, "AA:BB:CC:00:11:22"), "MAC:aa-bb-cc-00-11-22", discover.StateStandby, true},
		{"no configured id", tvJSON("on", tvID, ""), "", discover.StateOn, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, asked := samsungAt(t, serve(tt.body))
			obs, err := QueryNetwork(context.Background(), NetworkTarget{Probe: discover.ProbeSamsung, Host: tvHost, ID: tt.id}, opts)
			if err != nil {
				t.Fatal(err)
			}
			want := NetworkObservation{At: queryAt, State: tt.state, Host: tvHost, ID: tvID, Name: "Living room TV", Model: "QN00X00", Verified: tt.verified}
			if obs != want {
				t.Errorf("got %+v\nwant %+v", obs, want)
			}
			if got := strings.Join(*asked, ","); got != "GET http://"+tvHost+":8001/api/v2/" {
				t.Errorf("asked %q", got)
			}
		})
	}
}

// Only an explicit, identity-checked PowerState counts; everything else is
// unknown, never on or off.
func TestQueryNetworkSamsungUnknown(t *testing.T) {
	tests := []struct {
		name string
		h    http.HandlerFunc
		id   string
		want error
	}{
		{"missing state", serve(tvJSON("", tvID, "")), tvID, ErrNoState},
		{"odd state", serve(tvJSON("sleeping", tvID, "")), tvID, ErrNoState},
		{"malformed", serve(`{"device":`), tvID, ErrMalformed},
		{"not samsung", serve(`{"device":{"name":"x","PowerState":"on"}}`), "", ErrMalformed},
		{"http error", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", 500) }, tvID, ErrMalformed},
		{"oversized", serve(tvJSON("on", tvID, "") + strings.Repeat(" ", 70<<10)), tvID, ErrMalformed},
		{"other tv", serve(tvJSON("on", "uuid:11111111-2222-3333-4444-555555555555", "")), tvID, ErrIdentityMismatch},
		{"mac of other tv", serve(tvJSON("on", tvID, "aa:bb:cc:00:11:99")), "mac:aa:bb:cc:00:11:22", ErrIdentityMismatch},
		{"identity not reported", serve(`{"device":{"type":"Samsung SmartTV","PowerState":"on"}}`), tvID, ErrIdentityUnknown},
		{"only another id family", serve(tvJSON("on", "", "aa:bb:cc:00:11:22")), tvID, ErrIdentityUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, _ := samsungAt(t, tt.h)
			obs, err := QueryNetwork(context.Background(), NetworkTarget{Probe: discover.ProbeSamsung, Host: tvHost, ID: tt.id}, opts)
			if !errors.Is(err, tt.want) || obs.State != discover.StateUnknown || obs.Verified {
				t.Errorf("got %+v, %v; want unknown, %v", obs, err, tt.want)
			}
		})
	}
}

func TestQueryNetworkSamsungHTTPBounds(t *testing.T) {
	var elsewhere int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere++
		w.Write([]byte(tvJSON("on", tvID, "")))
	}))
	defer other.Close()
	for name, h := range map[string]http.HandlerFunc{
		"redirect": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) },
		"big header": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Big", strings.Repeat("h", 64<<10))
			w.Write([]byte(tvJSON("on", tvID, "")))
		},
	} {
		t.Run(name, func(t *testing.T) {
			opts, _ := samsungAt(t, h)
			obs, err := QueryNetwork(context.Background(), NetworkTarget{Probe: discover.ProbeSamsung, Host: tvHost, ID: tvID}, opts)
			if err == nil || obs.State != discover.StateUnknown {
				t.Errorf("got %+v, %v; want unknown with an error", obs, err)
			}
		})
	}
	if elsewhere != 0 {
		t.Errorf("redirect followed %d times", elsewhere)
	}
	c := networkHTTPClient()
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil || tr.TLSClientConfig != nil || tr.MaxResponseHeaderBytes <= 0 || tr.MaxResponseHeaderBytes > 64<<10 || !tr.DisableKeepAlives {
		t.Fatalf("transport %+v: want no proxy, default TLS, bounded headers, no idle conns", c.Transport)
	}
	if c.CheckRedirect == nil || c.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Error("client follows redirects")
	}
}

// No answer is unknown, not proof of off, and comes back within the timeout.
func TestQueryNetworkSamsungNoAnswer(t *testing.T) {
	refused := doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	})
	silent := doerFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	tv := NetworkTarget{Probe: discover.ProbeSamsung, Host: tvHost, ID: tvID}
	checkNoAnswer(t, []noAnswerCase{
		{"samsung refused", context.Background(), tv, NetworkOptions{HTTP: refused}, ErrNoAnswer},
		{"samsung timeout", context.Background(), tv, NetworkOptions{HTTP: silent, Timeout: 50 * time.Millisecond}, ErrNoAnswer},
		{"samsung canceled", canceled(), tv, NetworkOptions{HTTP: silent}, context.Canceled},
	})
}

// A target must name one device: no hostless query, no weak host IDs, no
// ID from another device family.
func TestQueryNetworkSamsungInvalidTarget(t *testing.T) {
	checkInvalidTargets(t, []NetworkTarget{
		{Probe: discover.ProbeSamsung, ID: tvID},
		{Probe: discover.ProbeSamsung, Host: "0.0.0.0"},
		{Probe: discover.ProbeSamsung, Host: "tv.example"},
		{Probe: "roku", Host: tvHost},
		{Probe: discover.ProbeSamsung, Host: tvHost, ID: "host:" + tvHost},
		{Probe: discover.ProbeSamsung, Host: tvHost, ID: "mdns:tv"},
		{Probe: discover.ProbeSamsung, Host: tvHost, ID: "uuid:"},
		{Probe: discover.ProbeSamsung, Host: tvHost, ID: "ps:a1b2c3d4e5f6"},
	})
}
