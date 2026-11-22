package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

type serviceOptions struct {
	port                       int
	db, hosts, runtime, binary string
	machine                    bool
}

func serviceCommand(args []string, out, diagnostics io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" {
		fmt.Fprintln(out, "Usage: tp service setup|status|start|stop|restart|upgrade")
		fmt.Fprintln(out, "Use tp service ACTION -h for options. Service management runs on macOS.")
		return nil
	}
	var options serviceOptions
	flags := flag.NewFlagSet("service "+args[0], flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	switch args[0] {
	case "setup":
		flags.IntVar(&options.port, "p", 10000, "listen on 127.0.0.1 at this port")
		flags.StringVar(&options.db, "db-config", os.Getenv("TELEPORT_DB_CONFIG"), "owner-only database configuration file (default ~/.teleport/database.json)")
		flags.StringVar(&options.hosts, "public-host", "", "comma-separated trusted HTTPS proxy hostnames")
		flags.StringVar(&options.runtime, "runtime-dir", os.Getenv("TELEPORT_RUNTIME_DIR"), "private local session daemon directory")
	case "status":
		flags.BoolVar(&options.machine, "json", false, "print status as JSON")
	case "upgrade":
		flags.StringVar(&options.binary, "binary", "", "candidate tp executable (defaults to the invoking binary)")
	case "start", "stop", "restart":
	default:
		return fmt.Errorf("unknown service action %q; use tp service -h", args[0])
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected service arguments; use tp service ACTION -h")
	}
	return manageService(args[0], options, out)
}
