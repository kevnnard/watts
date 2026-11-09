package sensor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/kevnnard/watts/internal/discover"
)

const (
	samsungPort        = 8001
	samsungBodyLimit   = 64 << 10
	samsungHeaderLimit = 32 << 10
)

// networkHTTPClient goes straight to the device (no proxy from the
// environment), keeps default TLS verification, never follows a redirect,
// bounds the response headers and keeps no idle connection, as the scanner
// does.
func networkHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            (&net.Dialer{}).DialContext,
			DisableKeepAlives:      true,
			MaxResponseHeaderBytes: samsungHeaderLimit,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// querySamsung makes one GET of the TV's http://ip:8001/api/v2/.
func querySamsung(ctx context.Context, ip net.IP, c discover.HTTPDoer) (answer, error) {
	if c == nil {
		c = networkHTTPClient()
	}
	u := "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(samsungPort)) + "/api/v2/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return answer{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return answer{}, noAnswer(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return answer{}, fmt.Errorf("%w: HTTP %d", ErrMalformed, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, samsungBodyLimit+1))
	if err != nil {
		return answer{}, noAnswer(err)
	}
	if len(body) > samsungBodyLimit {
		return answer{}, fmt.Errorf("%w: over %d bytes", ErrMalformed, samsungBodyLimit)
	}
	return parseSamsungState(body)
}

// parseSamsungState reads an api/v2 answer such as
//
//	{"device": {"id": "uuid:…", "PowerState": "on", "modelName": "…", …}, …}
//
// Only an explicit PowerState "on" or "standby" is a state.
func parseSamsungState(body []byte) (answer, error) {
	var v struct {
		Device struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			ModelName  string `json:"modelName"`
			PowerState string `json:"PowerState"`
			WifiMac    string `json:"wifiMac"`
			Type       string `json:"type"`
		} `json:"device"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return answer{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	d := v.Device
	if d.ID == "" && !strings.Contains(strings.ToLower(v.Type+" "+d.Type), "samsung") {
		return answer{}, fmt.Errorf("%w: not a Samsung TV", ErrMalformed)
	}
	a := answer{name: strings.TrimSpace(d.Name), model: strings.TrimSpace(d.ModelName)}
	id := strings.TrimSpace(d.ID)
	if !strings.HasPrefix(strings.ToLower(id), "uuid:") {
		id = "uuid:" + id
	}
	if id, ok := normalizeID(id); ok {
		a.ids = append(a.ids, id)
	}
	if mac, ok := normalizeID("mac:" + d.WifiMac); ok {
		a.ids = append(a.ids, mac)
	}
	switch strings.ToLower(strings.TrimSpace(d.PowerState)) {
	case "on":
		a.state = discover.StateOn
	case "standby":
		a.state = discover.StateStandby
	}
	return a, nil
}
