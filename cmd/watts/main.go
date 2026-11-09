// Command watts tracks the power use and electricity cost of the user's
// devices. Run `watts --help` for the commands.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// command is one subcommand. run gets the arguments after the command name.
type command struct {
	name    string
	summary string
	run     func(args []string, stdout, stderr io.Writer) int
	usage   string              // synopsis after "watts "; "" means the name
	flags   func(*flag.FlagSet) // registers the flags, for help; nil means none
}

// commands lists the subcommands in help order. "" is the bare `watts` (TUI).
var commands = []command{
	{"", "open the dashboard (TUI)", runDashboard, "", nil},
	{"status", "print a one-shot text summary", runStatus, "", nil},
	{"daemon", "sample this machine's sensor every interval into the local store; stops on SIGINT/SIGTERM", runDaemon, "", nil},
	{"export", "print stored samples, daily aggregates and toggles as JSON (read by the Mac over SSH)", runExport,
		"export --json [--from TIME] [--to TIME]", func(fs *flag.FlagSet) { exportFlags(fs) }},
	{"sensors", "print one reading of this machine's power sensor", runSensors,
		"sensors [--json]", func(fs *flag.FlagSet) { sensorsFlags(fs) }},
	{"install", "write and load the LaunchAgent (macOS) or systemd user unit (Linux) that runs the daemon", runInstall,
		"install [--dry-run] [--binary PATH]", func(fs *flag.FlagSet) { installFlags(fs) }},
	{"import-consumo", "import the consumo daily ledgers as daily aggregates (re-running replaces, never adds)", runImport,
		"import-consumo [--dir DIR]", func(fs *flag.FlagSet) { importFlags(fs) }},
	{"version", "print the version", runVersion, "", nil},
}

// help is added in init because it lists commands.
func init() {
	showHelp = commandHelp
	commands = append(commands, command{"help", "show this help, or a command's flags", runHelp, "help [command]", nil})
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	name, rest := "", args
	if len(args) > 0 {
		name, rest = args[0], args[1:]
	}
	switch name {
	case "-h", "--help":
		name = "help"
	case "-v", "--version":
		name = "version"
	}
	for _, c := range commands {
		if c.name == name {
			return c.run(rest, stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "watts: unknown command %q\nRun 'watts --help' for the commands.\n", name)
	return 2
}

func runVersion(_ []string, stdout, _ io.Writer) int {
	fmt.Fprintf(stdout, "watts %s\n", version)
	return 0
}

func runHelp(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		for _, c := range commands {
			if c.name == args[0] {
				commandHelp(stdout, c.name)
				return 0
			}
		}
		fmt.Fprintf(stderr, "watts help: unknown command %q\n", args[0])
		return 2
	}
	fmt.Fprint(stdout, `watts — energy use and electricity cost of your devices

Usage:
  watts [command] [flags]

Commands:
`)
	for _, c := range commands {
		name := c.name
		if name == "" {
			name = "(none)"
		}
		fmt.Fprintf(stdout, "  %-15s %s\n", name, c.summary)
	}
	fmt.Fprint(stdout, "\nFlags:\n")
	for _, c := range commands {
		if f := flagLines(c); f != "" {
			fmt.Fprintf(stdout, "  %s\n%s", c.synopsis(), strings.ReplaceAll(f, "  --", "    --"))
		}
	}
	fmt.Fprint(stdout, `
Files:
  config    $XDG_CONFIG_HOME/watts/config.toml (default ~/.config/watts/config.toml)
  store     $XDG_DATA_HOME/watts/watts.db      (default ~/.local/share/watts/watts.db)
  tariffs   $XDG_DATA_HOME/watts/tariffs.toml  (default ~/.local/share/watts/tariffs.toml)
  ledgers   ~/.local/share/consumo/YYYY-MM-DD  (old consumo files, read by import-consumo)
  service   ~/Library/LaunchAgents/dev.kevnnard.watts.plist, log ~/Library/Logs/watts.log (macOS)
            ~/.config/systemd/user/watts.service, log: journalctl --user -u watts.service (Linux)

Monitor watts from DDC (config keys; the Linux daemon reads them with ddcutil):
  ddc = "linux"              on a follows item: while the PC's daemon reads the monitor,
                             watts = min_w + (max_w - min_w) x brightness, or standby_w
                             asleep; the fixed watts fill the rest of the time
  min_w, max_w, standby_w    optional, per unit; default: the catalog model's values
                             (LG 29WQ600: 7, 22, 0.3), else 0.3 x watts, watts, 0.3
  [display] bus = N          the monitor's /dev/i2c-N; default: ddcutil detect at start

Environment:
  XDG_CONFIG_HOME, XDG_DATA_HOME  move the config and the data dir
  WATTS_REMOTE_COMMAND            watts binary on the remote host; overrides remote.command
                                  in the config (default ~/.local/bin/watts)

Run 'watts <command> -h' for one command's flags.
`)
	return 0
}
