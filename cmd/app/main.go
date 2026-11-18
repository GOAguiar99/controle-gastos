// Command app is the whole personal finance web app: one binary plus PostgreSQL
// (DATABASE_URL).
//
//	app serve                          start the web server
//	app import [-force] planilha.xlsx  import the legacy spreadsheet
//	app migrate-sqlite [-force] app.db copy the legacy SQLite database into PostgreSQL
//	app claim-ledger email             reserve that ledger for a Google account (first sign-in joins it)
//	app suspend-user email|id          block an account (sign-in, sessions, MCP links)
//	app unsuspend-user email|id        reactivate it
//	app delete-user email|id           erase an account and its ledger (verified LGPD request)
//	app grant-admin|revoke-admin email|id  give or take the admin role (admin page /admin)
//	app set-password [-stdin] email    create/update a password account (APP_AUTH=password)
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the VM may not ship tzdata

	"golang.org/x/term"

	"controle-gastos/internal/app"
	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
	"controle-gastos/internal/importer"
	"controle-gastos/internal/mcpserver"
	"controle-gastos/internal/metrics"
	"controle-gastos/internal/web"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve()
	case "import":
		err = importCmd(os.Args[2:])
	case "migrate-sqlite":
		err = migrateSQLite(os.Args[2:])
	case "claim-ledger":
		err = claimLedger(os.Args[2:])
	case "set-password":
		err = setPassword(os.Args[2:])
	case "suspend-user", "unsuspend-user", "delete-user", "grant-admin", "revoke-admin":
		err = adminUser(os.Args[1], os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "uso: app serve | app import [-force] [-today AAAA-MM-DD] arquivo.xlsx | app migrate-sqlite [-force] app.db | app claim-ledger email@gmail.com | app suspend-user|unsuspend-user|delete-user|grant-admin|revoke-admin email|id | app set-password [-stdin] email")
	os.Exit(2)
}

func location() *time.Location {
	loc, err := time.LoadLocation(env("APP_TZ", "America/Sao_Paulo"))
	if err != nil {
		log.Printf("timezone inválida, usando UTC: %v", err)
		return time.UTC
	}
	return loc
}

func serve() error {
	addr := env("ADDR", ":8080")
	cfg := web.Config{Secret: []byte(os.Getenv("APP_SECRET")),
		ControllerName: os.Getenv("APP_CONTROLLER_NAME"), ContactEmail: os.Getenv("APP_CONTACT_EMAIL")}
	base := os.Getenv("APP_BASE_URL")
	switch mode := env("APP_AUTH", "google"); mode {
	case "google":
		if id := os.Getenv("GOOGLE_CLIENT_ID"); id != "" {
			secure, err := web.ValidateBaseURL(base)
			if err != nil {
				return err
			}
			cfg.Google = &web.GoogleConfig{ClientID: id, ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"), BaseURL: base}
			cfg.SecureCookies, cfg.BaseURL = secure, base
		}
	case "password":
		// self-hosted without Google: passwords never travel over plain HTTP
		secure, err := web.ValidateBaseURL(base)
		if err != nil {
			return err
		}
		cfg.Password, cfg.SecureCookies, cfg.BaseURL = true, secure, base
		log.Println("login por senha (APP_AUTH=password); crie contas com: controle-gastos set-password email")
	default:
		return fmt.Errorf("APP_AUTH inválido: %q (use google ou password)", mode)
	}
	if os.Getenv("APP_INSECURE_NO_AUTH") == "1" {
		if !loopback(addr) {
			return errors.New("APP_INSECURE_NO_AUTH=1 só é aceito com ADDR em localhost (ex.: 127.0.0.1:8080)")
		}
		cfg.DevLogin = true
		log.Println("ATENÇÃO: login de desenvolvimento ativo (APP_INSECURE_NO_AUTH=1)")
	}
	if cfg.Google == nil && !cfg.DevLogin && !cfg.Password {
		return errors.New("defina GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET e APP_BASE_URL, ou APP_AUTH=password e APP_BASE_URL (ou APP_INSECURE_NO_AUTH=1 para desenvolvimento local)")
	}
	if len(cfg.Secret) < 32 {
		// only signs the 10-minute OAuth state cookie: a restart just fails logins in progress
		cfg.Secret = []byte(rand.Text() + rand.Text())
	}

	ctx := context.Background()
	d, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer d.Close()
	a := app.NewApp(d, location())
	a.AllowEmail = app.AllowList(os.Getenv("APP_ALLOWED_EMAILS"))
	a.Signup = env("APP_SIGNUP", app.SignupApproval)
	if a.Signup != app.SignupApproval && a.Signup != app.SignupClosed {
		return fmt.Errorf("APP_SIGNUP inválido: %q (use approval ou closed)", a.Signup)
	}
	if cfg.DevLogin {
		allow := a.AllowEmail
		a.AllowEmail = func(e string) bool { return e == "dev@localhost" || allow(e) }
	}
	srv, err := web.New(ctx, a, cfg, mcpserver.Handler(web.ServiceFrom))
	if err != nil {
		return err
	}
	metrics.RegisterPool(d.PoolStat)
	app.OnAudit = metrics.CountAudit
	var ms *http.Server
	if maddr := os.Getenv("METRICS_ADDR"); maddr != "" {
		if err := metrics.CheckAddr(maddr); err != nil {
			return err
		}
		ms = &http.Server{Addr: maddr, Handler: metrics.Handler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
		ln, err := net.Listen("tcp", maddr)
		if err != nil {
			return fmt.Errorf("METRICS_ADDR: %w", err)
		}
		go func() {
			if err := ms.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				log.Printf("métricas: %v", err)
			}
		}()
		log.Printf("métricas em http://%s/metrics", maddr)
	}
	hs := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(shutdown)
		if ms != nil {
			ms.Shutdown(shutdown)
		}
	}()
	log.Printf("ouvindo em %s", hs.Addr)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func importCmd(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	force := fs.Bool("force", false, "apaga os dados existentes antes de importar")
	todayStr := fs.String("today", "", "data de referência (AAAA-MM-DD); padrão: hoje")
	fs.Parse(args)
	if fs.NArg() != 1 {
		usage()
	}
	today := finance.DateOnly(time.Now().In(location()))
	if *todayStr != "" {
		t, err := finance.ParseDate(*todayStr)
		if err != nil {
			return err
		}
		today = t
	}
	st, closeDB, err := openLedger()
	if err != nil {
		return err
	}
	defer closeDB()
	if err := ensureEmpty(st, *force); err != nil {
		return err
	}
	return importer.Run(st, fs.Arg(0), today, os.Stdout)
}

func migrateSQLite(args []string) error {
	fs := flag.NewFlagSet("migrate-sqlite", flag.ExitOnError)
	force := fs.Bool("force", false, "apaga os dados existentes da carteira antes de copiar")
	fs.Parse(args)
	if fs.NArg() != 1 {
		usage()
	}
	st, closeDB, err := openLedger()
	if err != nil {
		return err
	}
	defer closeDB()
	if err := ensureEmpty(st, *force); err != nil {
		return err
	}
	return importer.FromSQLite(st, fs.Arg(0), os.Stdout)
}

func openDB(ctx context.Context) (*db.DB, error) {
	u := os.Getenv("DATABASE_URL")
	if u == "" {
		return nil, errors.New("defina DATABASE_URL (ex.: postgres://cg:senha@localhost:5432/cg)")
	}
	return db.Open(ctx, u)
}

// openLedger returns the store of the ledger the bulk loaders write to: the
// first one, created on first run. Reserve it for its owner with claim-ledger.
func openLedger() (*db.Store, func(), error) {
	ctx := context.Background()
	d, err := openDB(ctx)
	if err != nil {
		return nil, nil, err
	}
	id, err := d.DefaultLedger(ctx)
	if err != nil {
		d.Close()
		return nil, nil, err
	}
	return d.Ledger(id), d.Close, nil
}

// claimLedger reserves the first ledger (filled by import or migrate-sqlite)
// for the Google account with this email: its first sign-in joins it.
func claimLedger(args []string) error {
	if len(args) != 1 || !strings.Contains(args[0], "@") {
		usage()
	}
	ctx := context.Background()
	d, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer d.Close()
	id, err := d.DefaultLedger(ctx)
	if err != nil {
		return err
	}
	if err := d.SetClaimEmail(ctx, id, args[0]); err != nil {
		return err
	}
	fmt.Printf("carteira %d reservada para %s: o primeiro login dessa conta Google entra nela\n", id, args[0])
	return nil
}

// adminUser runs the operator commands. Administration happens only here,
// over SSH on the server: the web app has no admin role, so no web session
// can ever reach another person's account or data.
func adminUser(cmd string, args []string) error {
	if len(args) != 1 {
		usage()
	}
	ctx := context.Background()
	d, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer d.Close()
	a := app.NewApp(d, location())
	var u db.User
	switch cmd {
	case "suspend-user", "unsuspend-user":
		u, err = a.SetSuspended(ctx, args[0], cmd == "suspend-user")
	case "delete-user":
		u, err = a.DeleteUserByOperator(ctx, args[0])
	case "grant-admin", "revoke-admin":
		role := db.RoleUser
		if cmd == "grant-admin" {
			role = db.RoleAdmin
		}
		u, err = a.SetRole(ctx, args[0], role)
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s: conta %d ok\n", cmd, u.ID)
	return nil
}

// setPassword creates or updates a password account. The password is read
// from the terminal without echo (or from stdin with -stdin), never from
// arguments or environment variables, which end up in shell history and ps.
func setPassword(args []string) error {
	fs := flag.NewFlagSet("set-password", flag.ExitOnError)
	stdin := fs.Bool("stdin", false, "lê a senha da entrada padrão (uma linha)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		usage()
	}
	var pw string
	if *stdin {
		line, err := bufio.NewReader(io.LimitReader(os.Stdin, 4096)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		pw = strings.TrimRight(line, "\r\n")
	} else {
		fd := int(os.Stdin.Fd())
		if !term.IsTerminal(fd) {
			return errors.New("sem terminal: use -stdin para passar a senha pela entrada padrão")
		}
		fmt.Fprint(os.Stderr, "Senha (mín. 12 caracteres): ")
		a, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stderr, "Repita a senha: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		if string(a) != string(b) {
			return errors.New("as senhas não conferem")
		}
		pw = string(a)
	}
	if err := app.CheckPasswordPolicy(fs.Arg(0), pw); err != nil {
		return err
	}
	ctx := context.Background()
	d, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer d.Close()
	u, err := app.NewApp(d, location()).SetPassword(ctx, fs.Arg(0), pw)
	if err != nil {
		return err
	}
	fmt.Printf("senha definida para a conta %d (%s); sessões anteriores encerradas\n", u.ID, u.Email)
	return nil
}

// loopback reports whether addr only listens on localhost.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func ensureEmpty(st *db.Store, force bool) error {
	empty, err := st.IsEmpty()
	if err != nil || empty {
		return err
	}
	if !force {
		return errors.New("a carteira já tem dados; use -force para apagar e importar de novo")
	}
	return st.Reset()
}
