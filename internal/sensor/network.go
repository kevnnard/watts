package sensor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/kevnnard/watts/internal/discover"
)

// Network state queries (S20, network panel S2–S3) ask one configured
// Samsung TV or PS5 for its power state. An explicit on or standby answer
// is an estimated state observation, never an electrical measurement, and
// a PS5 that is on is not proof of gaming. Anything else (no answer,
// refused, a missing or odd state, a malformed answer, an identity that
// does not match or cannot be checked) is unknown: never on, never off.

// NetworkTimeout bounds one query when NetworkOptions.Timeout is zero.
const NetworkTimeout = 2 * time.Second

// Why a query is unknown. Every error comes with State discover.StateUnknown.
var (
	ErrInvalidTarget    = errors.New("invalid network target")
	ErrNoAnswer         = errors.New("no answer")
	ErrMalformed        = errors.New("not a power-state answer")
	ErrNoState          = errors.New("no power state in the answer")
	ErrIdentityMismatch = errors.New("device identity does not match")
	ErrIdentityUnknown  = errors.New("device identity not reported")
)

// NetworkTarget is the configured device to ask.
type NetworkTarget struct {
	Probe string // discover.ProbeSamsung or discover.ProbePS5
	Host  string // IPv4 address; required, a query never searches
	// ID is the stable identity from a scan ("uuid:…" or "mac:…" for
	// Samsung, "ps:…" for PS5), or empty. When set, an answer only counts
	// if the device reports that identity.
	ID string
}

// NetworkObservation is one query result. ID, Name and Model are what the
// device reported; State is only on or standby when err is nil.
type NetworkObservation struct {
	At       time.Time
	State    string // discover.StateOn, StateStandby or StateUnknown
	Host     string
	ID       string // normalized; "" when the device reported none
	Name     string
	Model    string
	Verified bool // the target's ID was set and the device reported it
}

// NetworkOptions holds the network edges of a query; zero values use the
// real network.
type NetworkOptions struct {
	Timeout time.Duration // NetworkTimeout when zero
	// HTTP is trusted code injected by the caller. It replaces the default
	// client and with it that client's safety (no proxy from the
	// environment, no redirects, bounded headers, default TLS checks).
	HTTP      discover.HTTPDoer
	ListenUDP func() (discover.PacketConn, error) // unconnected IPv4 UDP socket
	Now       func() time.Time
}

// answer is what a device said: state is "" when it gave no usable one,
// ids lists its normalized identities, primary first.
type answer struct {
	state, name, model string
	ids                []string
}

// QueryNetwork asks the target for its power state once.
func QueryNetwork(ctx context.Context, t NetworkTarget, o NetworkOptions) (NetworkObservation, error) {
	obs := NetworkObservation{At: now(o.Now), State: discover.StateUnknown, Host: t.Host}
	ip, want, err := t.check()
	if err != nil {
		return obs, err
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = NetworkTimeout
	}
	qctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var a answer
	if t.Probe == discover.ProbeSamsung {
		a, err = querySamsung(qctx, ip, o.HTTP)
	} else {
		a, err = queryPS5(qctx, ip, o.ListenUDP)
	}
	if err != nil && errors.Is(ctx.Err(), context.Canceled) {
		err = ctx.Err()
	}
	if err != nil {
		return obs, fmt.Errorf("%s %s: %w", t.Probe, t.Host, err)
	}
	obs.Name, obs.Model = a.name, a.model
	if len(a.ids) > 0 {
		obs.ID = a.ids[0]
	}
	if want != "" {
		// Only an ID of the same family can confirm or contradict want.
		family, _, _ := strings.Cut(want, ":")
		same := slices.DeleteFunc(slices.Clone(a.ids), func(id string) bool { return !strings.HasPrefix(id, family+":") })
		switch {
		case len(same) == 0:
			return obs, fmt.Errorf("%s %s: %w (want %s)", t.Probe, t.Host, ErrIdentityUnknown, want)
		case !slices.Contains(same, want):
			return obs, fmt.Errorf("%s %s: %w: %s, want %s", t.Probe, t.Host, ErrIdentityMismatch, same[0], want)
		}
		obs.Verified = true
	}
	if a.state == "" {
		obs.Verified = false
		return obs, fmt.Errorf("%s %s: %w", t.Probe, t.Host, ErrNoState)
	}
	obs.State = a.state
	return obs, nil
}

// check returns the target's address and normalized ID. The host must be
// one unicast IPv4 address, so a query can never become a broadcast, and
// the ID must be a stable identity of the probe's device family.
func (t NetworkTarget) check() (net.IP, string, error) {
	if t.Probe != discover.ProbeSamsung && t.Probe != discover.ProbePS5 {
		return nil, "", fmt.Errorf("%w: probe %q", ErrInvalidTarget, t.Probe)
	}
	ip := net.ParseIP(t.Host).To4()
	if ip == nil || !(ip.IsGlobalUnicast() || ip.IsLinkLocalUnicast()) {
		return nil, "", fmt.Errorf("%w: host %q is not one IPv4 unicast address", ErrInvalidTarget, t.Host)
	}
	if strings.TrimSpace(t.ID) == "" {
		return ip, "", nil
	}
	id, ok := normalizeID(t.ID)
	family, _, _ := strings.Cut(id, ":")
	if !ok || (family == "ps") != (t.Probe == discover.ProbePS5) {
		return nil, "", fmt.Errorf("%w: %q is not a stable %s identity", ErrInvalidTarget, t.ID, t.Probe)
	}
	return ip, id, nil
}

// normalizeID returns a stable identity as "uuid:…", "mac:…" (canonical
// colon form) or "ps:…", lower case. Host or mDNS IDs are not stable
// identities.
func normalizeID(s string) (string, bool) {
	family, v, _ := strings.Cut(strings.TrimSpace(s), ":")
	family, v = strings.ToLower(family), strings.ToLower(strings.TrimSpace(v))
	if v == "" || strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' }) {
		return "", false
	}
	switch family {
	case "uuid", "ps":
		return family + ":" + v, true
	case "mac":
		if hw, err := net.ParseMAC(v); err == nil && len(hw) == 6 {
			return "mac:" + hw.String(), true
		}
	}
	return "", false
}

// noAnswer marks err as ErrNoAnswer when it means the device did not
// answer: a timeout, refused, or an unreachable host or network.
func noAnswer(err error) error {
	if isTimeout(err) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EHOSTDOWN) || errors.Is(err, syscall.ENETUNREACH) {
		return fmt.Errorf("%w: %v", ErrNoAnswer, err)
	}
	return err
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout())
}
