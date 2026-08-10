package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/manager"
	"github.com/Jensen95/shelly-local-comms/internal/tui"
	"github.com/Jensen95/shelly-local-comms/internal/web"
)

var version = "dev"

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage())
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "tui":
		return withManager(rest, func(ctx context.Context, m *manager.Manager) error {
			m.Start(ctx)
			return tui.Run(m)
		})
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		addr := fs.String("addr", ":8790", "listen address")
		configPath := configFlag(fs)
		discoverEvery := discoverFlag(fs)
		if err := fs.Parse(rest); err != nil {
			return err
		}
		m, err := openManager(*configPath, manager.WithDiscoveryInterval(*discoverEvery))
		if err != nil {
			return err
		}
		ctx, stop := signalContext()
		defer stop()
		m.Start(ctx)
		fmt.Printf("shellyctl web UI on http://localhost%s\n", displayAddr(*addr))
		return web.Serve(ctx, *addr, m)
	case "discover":
		fs := flag.NewFlagSet("discover", flag.ContinueOnError)
		timeout := fs.Int("timeout", 5, "scan duration in seconds")
		configPath := configFlag(fs)
		if err := fs.Parse(rest); err != nil {
			return err
		}
		m, err := openManager(*configPath)
		if err != nil {
			return err
		}
		ctx, stop := signalContext()
		defer stop()
		fresh, err := m.Discover(ctx, *timeout)
		if err != nil {
			return err
		}
		devs := m.Devices()
		w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tADDRESS\tMODEL\tGEN\tSOURCE")
		for _, d := range devs {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", d.Key(), d.Addr, d.Info.Model, d.Info.Gen, d.Source)
		}
		if err := w.Flush(); err != nil {
			return err
		}
		fmt.Printf("%d device(s) known, %d newly discovered\n", len(devs), len(fresh))
		return nil
	case "version":
		fmt.Println("shellyctl", version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage())
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage())
	}
}

func configFlag(fs *flag.FlagSet) *string {
	def, err := app.DefaultConfigPath()
	if err != nil {
		def = "config.json"
	}
	return fs.String("config", def, "path to config file")
}

func discoverFlag(fs *flag.FlagSet) *time.Duration {
	return fs.Duration("discover-interval", manager.DefaultDiscoveryInterval,
		"background mDNS auto-discovery interval (0 disables)")
}

func openManager(configPath string, opts ...manager.Option) (*manager.Manager, error) {
	store, err := app.OpenStore(configPath)
	if err != nil {
		return nil, err
	}
	return manager.New(store, opts...), nil
}

func withManager(args []string, fn func(context.Context, *manager.Manager) error) error {
	fs := flag.NewFlagSet("shellyctl", flag.ContinueOnError)
	configPath := configFlag(fs)
	discoverEvery := discoverFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := openManager(*configPath, manager.WithDiscoveryInterval(*discoverEvery))
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	return fn(ctx, m)
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func displayAddr(addr string) string {
	if addr != "" && addr[0] == ':' {
		return addr
	}
	return " (" + addr + ")"
}
