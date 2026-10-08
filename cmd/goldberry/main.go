// Command goldberry is the whole app: one binary, one container (plan §12).
//
//	goldberry [serve]                         run the web app (default)
//	goldberry migrate                         apply database migrations and exit
//	goldberry healthcheck                     exit 0 if the local server answers /healthz
//	goldberry admin reset-password <username> set a new admin password
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
	"strings"
	"syscall"
	"time"

	"github.com/chinny/goldberry/internal/auth"
	"github.com/chinny/goldberry/internal/buildinfo"
	"github.com/chinny/goldberry/internal/config"
	"github.com/chinny/goldberry/internal/scheduler"
	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
	"github.com/chinny/goldberry/internal/store/engine"
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
	case "backup", "export", "import":
		fmt.Fprintf(os.Stderr, "goldberry %s: not built yet (planned for v1.0, plan §14 phase 6)\n", cmd)
		os.Exit(2)
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
		{Name: "request-expiry", Run: func(ctx context.Context) error {
			n, err := svc.ExpireRequests(ctx)
			if n > 0 {
				log.Info("expired requests", "count", n)
			}
			return err
		}},
	}}
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
