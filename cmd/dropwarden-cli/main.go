// Command dropwarden-cli is the headless development harness for dropwarden. It
// exposes the engine and Twitch layers without the Wails UI so every phase can
// be run and observed directly from GoLand.
//
// Subcommands:
//
//	doctor     print resolved paths, config, and detected Chrome (no browser launch)
//	login      open the dedicated Chrome profile and wait for manual Twitch login
//	discover   run one discovery pass and print campaigns + queue
//	inventory  read the inventory page and print per-drop progress
//	watch      run the full watcher engine until interrupted
//	report     print claim history, session stats, and the current queue
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/rake-pro/dropwarden/internal/app"
	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/service"
	"github.com/rake-pro/dropwarden/internal/twitch"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	var err error
	switch cmd {
	case "doctor":
		err = doctor()
	case "login":
		err = login()
	case "discover":
		err = discover()
	case "inventory":
		err = inventory()
	case "watch":
		err = watch()
	case "report":
		err = report()
	case "update-apply":
		// Hidden: the self-updater's out-of-process installer, started by the
		// GUI host (see updateapply.go). Not listed in usage.
		err = updateApply(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `dropwarden-cli <command>

  doctor     print resolved paths, config summary, detected Chrome
  login      launch the dedicated Chrome profile and wait for manual login
  discover   run one discovery pass; print campaigns and queue
  inventory  read the inventory page; print per-drop progress
  watch      run the watcher engine until Ctrl-C
  report     print claim history, session stats, and the queue
`)
}

// signalContext returns a context cancelled on SIGINT/SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		fmt.Fprintln(os.Stderr, "\ninterrupt received, shutting down...")
		cancel()
	}()
	return ctx, cancel
}

func doctor() error {
	a, err := app.Open(app.Options{Console: true})
	if err != nil {
		return err
	}
	defer a.Close()

	dir, _ := config.Dir()
	logPath, _ := config.LogPath()
	paths, _ := a.Cfg.ProfilePaths(a.Main().Name)
	profile, _ := a.Cfg.ProfileDir()
	chrome, chromeErr := browser.FindChrome(a.Cfg.Browser.ChromePath)

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "dropwarden doctor")
	fmt.Fprintf(w, "app root\t%s\n", dir)
	fmt.Fprintf(w, "config\t%s\n", a.ConfigPath)
	fmt.Fprintf(w, "profile\t%s\n", a.Main().Name)
	fmt.Fprintf(w, "database\t%s\n", paths.DB)
	fmt.Fprintf(w, "log\t%s\n", logPath)
	fmt.Fprintf(w, "chrome profile\t%s\n", profile)
	if chromeErr != nil {
		fmt.Fprintf(w, "chrome binary\tNOT FOUND (%v)\n", chromeErr)
	} else {
		fmt.Fprintf(w, "chrome binary\t%s\n", chrome)
	}
	fmt.Fprintf(w, "theme\t%s\n", a.Cfg.Theme)
	fmt.Fprintf(w, "games\t%v\n", a.Cfg.Games)
	fmt.Fprintf(w, "priority mode\t%s\n", a.Cfg.Queue.PriorityMode)
	fmt.Fprintf(w, "auth mode\t%s\n", a.Cfg.Auth.Mode)
	fmt.Fprintf(w, "player volume\t%.3f (muted-safe: %v)\n", a.Cfg.Browser.Player.Volume, a.Cfg.Browser.Player.Volume > 0)
	fmt.Fprintf(w, "gql claim fallback\t%v\n", a.Cfg.Claim.AllowGQLFallback)
	w.Flush()
	if chromeErr != nil {
		return fmt.Errorf("chrome not found; set browser.chrome_path in %s", a.ConfigPath)
	}
	return nil
}

// mainTwitch returns the first profile's Twitch runtime. StartRuntime only
// errors when NO profile started, so on a multi-profile config the first
// profile's launch can have failed while a later one came up.
func mainTwitch(a *app.App) (*app.PlatformRuntime, error) {
	pr := a.Main()
	if pr == nil || pr.Twitch == nil {
		return nil, fmt.Errorf("twitch runtime for the first profile did not start (see log)")
	}
	return pr.Twitch, nil
}

func login() error {
	a, err := app.Open(app.Options{Console: true})
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()

	if err := a.StartRuntime(ctx, service.NewLogEmitter(a.Log)); err != nil {
		return err
	}
	fmt.Println("A dedicated Chrome window has opened. Log in to Twitch by hand.")
	fmt.Println("Waiting for the session to become authenticated (Ctrl-C to abort)...")

	tw, err := mainTwitch(a)
	if err != nil {
		return err
	}
	lctx, lcancel := context.WithTimeout(ctx, 10*time.Minute)
	defer lcancel()
	if err := twitch.WaitForLogin(lctx, tw.Session, 3*time.Second); err != nil {
		return err
	}
	fmt.Println("Logged in. The session is saved in the profile; future runs reuse it.")
	return nil
}

func discover() error {
	a, err := app.Open(app.Options{Console: true})
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	if err := a.StartRuntime(ctx, service.NewLogEmitter(a.Log)); err != nil {
		return err
	}
	tw, err := mainTwitch(a)
	if err != nil {
		return err
	}
	res, err := tw.Disco.RunOnce(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("discovery: %d campaigns, %d details, %d progress, %d enqueued\n\n",
		res.Campaigns, res.Details, res.Progress, res.Enqueued)
	return printCampaigns(a)
}

func inventory() error {
	a, err := app.Open(app.Options{Console: true})
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	if err := a.StartRuntime(ctx, service.NewLogEmitter(a.Log)); err != nil {
		return err
	}
	tw, err := mainTwitch(a)
	if err != nil {
		return err
	}
	prog, err := tw.Pages.ReadInventory(ctx)
	if err != nil {
		return err
	}
	if len(prog) == 0 {
		fmt.Println("no in-progress drops found (verify you are logged in and have an active campaign)")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "DROP\tMINUTES\tREQUIRED\tCLAIMABLE\tSOURCE")
	for _, p := range prog {
		m, _ := p.Effective()
		fmt.Fprintf(w, "%s\t%d\t%d\t%v\t%s\n", p.DropID, m, p.RequiredMinutes, p.Claimable, p.Source)
	}
	w.Flush()
	return nil
}

func watch() error {
	a, err := app.Open(app.Options{Console: true})
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	if err := a.StartRuntime(ctx, service.NewLogEmitter(a.Log)); err != nil {
		return err
	}
	tw, err := mainTwitch(a)
	if err != nil {
		return err
	}
	fmt.Println("watcher engine running; press Ctrl-C to stop")
	eng := tw.Engine
	eng.Start()
	return eng.Run(ctx)
}

func report() error {
	a, err := app.Open(app.Options{Console: false})
	if err != nil {
		return err
	}
	defer a.Close()

	st := a.Main().Store
	claims, _ := st.ListClaims(time.Time{}, time.Time{})
	sessions, _ := st.ListSessions(0)
	queue, _ := st.GetQueue()

	fmt.Printf("== Claims (%d) ==\n", len(claims))
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, c := range claims {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.ClaimedAt.Format("2006-01-02 15:04"), c.GameName, c.DropName, c.DropID)
	}
	w.Flush()

	fmt.Printf("\n== Sessions (%d) ==\n", len(sessions))
	outcomes := map[string]int{}
	totalMinutes := 0
	for _, s := range sessions {
		outcomes[string(s.Outcome)]++
		totalMinutes += s.MinutesWatched()
	}
	for k, v := range outcomes {
		fmt.Printf("  %-22s %d\n", k, v)
	}
	fmt.Printf("  total minutes watched: %d\n", totalMinutes)

	fmt.Printf("\n== Queue (%d) ==\n", len(queue))
	w = tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, it := range queue {
		fmt.Fprintf(w, "%d\t%s\tenabled=%v\t%s\t%s\n", it.Position, it.DropID, it.Enabled, it.Status, it.Reason)
	}
	w.Flush()
	return nil
}

func printCampaigns(a *app.App) error {
	campaigns, err := a.Main().Store.ListCampaigns()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "GAME\tCAMPAIGN\tSTATUS\tDROPS\tACL\tACCOUNT_LINK")
	for _, c := range campaigns {
		acl := "any"
		if !c.AnyChannel() {
			acl = fmt.Sprintf("%d chans", len(c.AllowedChannels))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%v\n", c.GameName, c.Name, c.Status, len(c.Drops), acl, c.RequiresAccountLink())
	}
	w.Flush()
	return nil
}
