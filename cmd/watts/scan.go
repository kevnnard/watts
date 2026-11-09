package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"unicode"
	"unicode/utf8"

	"github.com/kevnnard/watts/internal/catalog"
	"github.com/kevnnard/watts/internal/discover"
)

// scanNetwork is the network scan; tests replace it.
var scanNetwork = discover.Scan

// scanLimitsText is printed after every scan, so a missing device is not
// read as an off one.
const scanLimitsText = "Probe none: watts can't read its power state; add it as a manual or schedule item.\n" +
	"State unknown: no power-state answer. No answer never means off.\n" +
	"Only this machine's local networks are searched: multicast and broadcast don't cross\n" +
	"routers, guest or isolated Wi-Fi hides devices, and devices that are fully off can't answer.\n"

// scanCellMax caps a name or brand/model cell, as both come from the network.
const scanCellMax = 32

// runScan finds devices on the local networks and prints them (S21). It
// only reads: no config, store or other file is opened or written.
func runScan(args []string, stdout, stderr io.Writer) int {
	if code, ok := parseFlags("scan", args, stdout, stderr, noFlags); !ok {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var mu sync.Mutex
	var notes []string
	devs, err := scanNetwork(ctx, discover.Options{OnError: func(err error) {
		mu.Lock()
		notes = append(notes, err.Error())
		mu.Unlock()
	}})
	interrupted := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	if err != nil && !interrupted {
		fmt.Fprintf(stderr, "watts scan: %v\n", err)
		return 1
	}
	writeScan(stdout, devs, interrupted)
	mu.Lock()
	slices.Sort(notes)
	for _, n := range slices.Compact(notes) {
		fmt.Fprintf(stderr, "watts scan: note: %s\n", printable(n, 0))
	}
	mu.Unlock()
	if interrupted {
		fmt.Fprintln(stderr, "watts scan: interrupted")
		return 1
	}
	return 0
}

// writeScan prints devs, in the order discover.Scan sorted them, then the
// limits of a scan.
func writeScan(w io.Writer, devs []discover.Device, interrupted bool) {
	if len(devs) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tBRAND/MODEL\tSTATE\tPROBE\tWATTS\tHOST\tID")
		for _, d := range devs {
			model := strings.TrimSpace(d.Brand + " " + d.Model)
			if model == "" {
				model = "-"
			}
			probe := d.Probe
			if probe == "" {
				probe = "none"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", printable(d.Name, scanCellMax), printable(model, scanCellMax),
				printable(d.State, 0), printable(probe, 0), suggestedWatts(d), printable(d.Host, 0), printable(d.ID, 0))
		}
		tw.Flush()
		fmt.Fprintln(w)
	}
	n := strconv.Itoa(len(devs)) + " devices"
	switch len(devs) {
	case 0:
		n = "No devices"
	case 1:
		n = "1 device"
	}
	if interrupted {
		fmt.Fprintf(w, "%s answered before the scan was interrupted.\n", n)
	} else {
		fmt.Fprintf(w, "%s answered within %s.\n", n, discover.DefaultTimeout)
	}
	fmt.Fprint(w, scanLimitsText)
}

// suggestedWatts is the catalog's typical watts when the brand and model
// match an entry exactly; anything else stays unknown, never a guess.
func suggestedWatts(d discover.Device) string {
	if d.Brand == "" || d.Model == "" {
		return "unknown"
	}
	e, ok := catalog.Find(d.Brand, d.Model)
	if !ok || e.Watts <= 0 {
		return "unknown"
	}
	return "~" + strconv.FormatFloat(e.Watts, 'f', -1, 64) + " W"
}

// printable replaces control and other non-printing runes (terminal
// escapes, tabs, newlines) with '?', and cuts s to max runes when max > 0.
func printable(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, s)
	if r := []rune(s); max > 0 && len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}
