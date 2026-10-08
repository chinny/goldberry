// Command goldberry is the whole app: one binary, one container (plan §12).
//
//	goldberry [serve]                         run the web app (default)
//	goldberry migrate                         apply database migrations and exit
//	goldberry healthcheck                     exit 0 if the local server answers /healthz
//	goldberry admin reset-password <username> set a new admin password
//	goldberry backup                          snapshot the SQLite database now
//	goldberry export [-o file]                write a portable JSONL dump
//	goldberry import <file>                   load a dump into an empty database
//	goldberry version                         print the version
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chinny/goldberry/internal/auth"
	"github.com/chinny/goldberry/internal/backup"
	"github.com/chinny/goldberry/internal/buildinfo"
	"github.com/chinny/goldberry/internal/config"
	"github.com/chinny/goldberry/internal/scheduler"
	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
	"github.com/chinny/goldberry/internal/store/engine"
	"github.com/chinny/goldberry/internal/store/sqlite"
	"github.com/chinny/goldberry/internal/web"
)

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "migrate":
		err = migrate()
	case "healthcheck":
		err = healthcheck()
	case "admin":
		err = admin(args)
	case "version", "--version", "-v":
		fmt.Printf("goldberry %s (%s)\n", buildinfo.Version, buildinfo.Commit)
	case "backup":
		err = backupNow()
	case "export":
		err = exportDump(args)
	case "import":
		err = importDump(args)
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "goldberry:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: goldberry [command]

  serve                          run the web app (default)
  migrate                        apply database migrations and exit
  healthcheck                    exit 0 if the local server answers /healthz
  admin reset-password <user>    set a new admin password (prints one, or reads --password-stdin)
  backup                         snapshot the SQLite database into /data/backups now
  export [-o file]               write every table as a portable JSONL dump (stdout by default)
  import <file>                  load a dump into an empty database (SQLite or Postgres)
  version                        print the version
`)
}

func setup() (config.Config, *slog.Logger, error) {
	cfg, err := config.Load(config.DefaultEnvFile)
	if err != nil {
		return cfg, nil, err
	}
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if os.Getenv("GOLDBERRY_DEBUG") != "" {
		opts.Level = slog.LevelDebug
	}
	var h slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return cfg, slog.New(h), nil
}

func open(ctx context.Context, cfg config.Config, log *slog.Logger) (*store.SQL, error) {
	st, err := engine.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", engine.Redact(cfg.DatabaseURL), err)
	}
	return st, nil
}

func serve() error {
	cfg, log, err := setup()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting goldberry", "version", buildinfo.Version, "commit", buildinfo.Commit,
		"database", engine.Redact(cfg.DatabaseURL), "listen", cfg.Listen)
	if cfg.InsecureCookies {
		log.Warn("GOLDBERRY_INSECURE_COOKIES is on: session cookies work over plain HTTP. Use only for local testing.")
	}
	if cfg.SecretKey == nil {
		log.Info("GOLDBERRY_SECRET_KEY is not set; SMTP passwords can't be stored in the UI (email arrives in a later release)")
	}

	st, err := open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer st.Close()
	svc := service.New(st, log)

	token := ""
	if need, err := svc.NeedsSetup(ctx); err != nil {
		return err
	} else if need {
		token = auth.NewToken()[:24]
		log.Warn("first run: open /setup and enter this one-time setup token", "setup_token", token)
		fmt.Fprintf(os.Stderr, "\n  Goldberry setup token: %s\n  Open /setup in a browser and paste it to create the first parent account.\n\n", token)
	}

	if n, err := svc.EnsureDefaultJars(ctx); err != nil {
		return fmt.Errorf("add default jars: %w", err)
	} else if n > 0 {
		log.Info("gave existing kids Save and Give jars", "kids", n)
	}

	srv, err := web.New(ctx, svc, cfg, log, token)
	if err != nil {
		return err
	}
	sched := &scheduler.Scheduler{Interval: time.Minute, Log: log, Jobs: []scheduler.Job{
		{Name: "allowance", Run: func(ctx context.Context) error {
			_, err := svc.PostAllowance(ctx)
			return err
		}},
		{Name: "interest", Run: func(ctx context.Context) error {
			_, err := svc.PostInterest(ctx)
			return err
		}},
		{Name: "goals", Run: func(ctx context.Context) error {
			_, err := svc.CheckGoals(ctx)
			return err
		}},
		{Name: "request-expiry", Run: func(ctx context.Context) error {
			n, err := svc.ExpireRequests(ctx)
			if n > 0 {
				log.Info("expired requests", "count", n)
			}
			return err
		}},
	}}
	if path, err := sqlite.PathFromURL(cfg.DatabaseURL); err == nil {
		at, _ := config.ParseClock(cfg.BackupSchedule)
		loc, _ := time.LoadLocation(cfg.Timezone)
		nightly := &backup.Nightly{Store: st, Dir: backup.Dir(path), At: at, Retain: cfg.BackupRetain, Loc: loc, Now: time.Now, Log: log}
		sched.Jobs = append(sched.Jobs, scheduler.Job{Name: "backup", Run: nightly.Run})
		log.Info("nightly backups on", "dir", nightly.Dir, "at", cfg.BackupSchedule, "retain", cfg.BackupRetain)
	} else {
		log.Info("built-in backups are for SQLite; back up Postgres with pg_dump or your operator")
	}
	go sched.Run(ctx)

	hs := &http.Server{
		Addr: cfg.Listen, Handler: srv,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Listen)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := hs.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func migrate() error {
	cfg, log, err := setup()
	if err != nil {
		return err
	}
	st, err := open(context.Background(), cfg, log)
	if err != nil {
		return err
	}
	log.Info("migrations applied", "database", engine.Redact(cfg.DatabaseURL))
	return st.Close()
}

// healthcheck is for Docker's HEALTHCHECK: distroless has no curl.
func healthcheck() error {
	cfg, err := config.Load(config.DefaultEnvFile)
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return fmt.Errorf("GOLDBERRY_LISTEN: %w", err)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %s", resp.Status)
	}
	return nil
}

func admin(args []string) error {
	if len(args) == 0 || args[0] != "reset-password" {
		return errors.New("usage: goldberry admin reset-password [--password-stdin] <username>")
	}
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	fromStdin := fs.Bool("password-stdin", false, "read the new password from stdin")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: goldberry admin reset-password [--password-stdin] <username>")
	}
	cfg, log, err := setup()
	if err != nil {
		return err
	}
	ctx := context.Background()
	st, err := open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer st.Close()
	svc := service.New(st, log)
	u, err := st.GetUserByUsername(ctx, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("no user %q", fs.Arg(0))
	}
	if !u.IsAdmin() {
		return fmt.Errorf("%s is a kid; a parent resets kid PINs in the app", u.Username)
	}
	var pw string
	if *fromStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		pw = strings.TrimRight(line, "\r\n")
	} else {
		pw = randomPassword(20)
	}
	if err := svc.ResetSecret(ctx, nil, u.ID, pw); err != nil {
		return err
	}
	if !*fromStdin {
		fmt.Printf("New password for %s: %s\n", u.Username, pw)
	} else {
		fmt.Printf("Password updated for %s.\n", u.Username)
	}
	return nil
}

func randomPassword(n int) string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var b strings.Builder
	for range n {
		i, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b.WriteByte(alphabet[i.Int64()])
	}
	return b.String()
}

// backupNow is `goldberry backup`: a snapshot next to the nightly ones.
func backupNow() error {
	cfg, log, err := setup()
	if err != nil {
		return err
	}
	path, err := sqlite.PathFromURL(cfg.DatabaseURL)
	if err != nil {
		return errors.New("built-in backups are for SQLite; use pg_dump for Postgres, or `goldberry export`")
	}
	ctx := context.Background()
	st, err := open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer st.Close()
	out := filepath.Join(backup.Dir(path), sqlite.OnDemandName(time.Now()))
	if err := sqlite.Backup(ctx, st, out); err != nil {
		return err
	}
	fmt.Println("Backup written to", out)
	fmt.Println("Copy it off this machine: a backup on the same disk is not a backup.")
	return nil
}

func exportDump(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	outPath := fs.String("o", "", "write to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, log, err := setup()
	if err != nil {
		return err
	}
	ctx := context.Background()
	st, err := open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer st.Close()
	var w io.Writer = os.Stdout
	if *outPath != "" {
		f, err := os.OpenFile(*outPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	n, err := st.Export(ctx, w, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "exported %d rows\n", n)
	return nil
}

func importDump(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: goldberry import <file>")
	}
	cfg, log, err := setup()
	if err != nil {
		return err
	}
	f, err := os.Open(args[0]) //nolint:gosec // the operator names the dump to load
	if err != nil {
		return err
	}
	defer f.Close()
	ctx := context.Background()
	st, err := open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := st.Import(ctx, f)
	if err != nil {
		return err
	}
	fmt.Printf("Imported %d rows into %s.\n", n, engine.Redact(cfg.DatabaseURL))
	return nil
}
