// Command app is the whole personal finance web app: one binary, one SQLite file.
//
//	app serve                         start the web server
//	app import [-force] planilha.xlsx import the legacy spreadsheet
//	app hash-password                 read a password from stdin, print its bcrypt hash
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the VM may not ship tzdata

	"golang.org/x/crypto/bcrypt"

	"controle-gastos/internal/app"
	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
	"controle-gastos/internal/importer"
	"controle-gastos/internal/mcpserver"
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
	case "hash-password":
		err = hashPassword()
	default:
		usage()
	}
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "uso: app serve | app import [-force] [-today AAAA-MM-DD] arquivo.xlsx | app hash-password")
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
	auth := &web.Auth{PasswordHash: []byte(os.Getenv("APP_PASSWORD_HASH")), Secret: []byte(os.Getenv("APP_SECRET"))}
	if !auth.Enabled() {
		if os.Getenv("APP_INSECURE_NO_AUTH") != "1" {
			return errors.New("defina APP_PASSWORD_HASH (gere com `app hash-password`) ou APP_INSECURE_NO_AUTH=1 para desenvolvimento local")
		}
		log.Println("ATENÇÃO: login desativado (APP_INSECURE_NO_AUTH=1)")
	}
	if len(auth.Secret) < 32 {
		auth.Secret = make([]byte, 32)
		rand.Read(auth.Secret)
		log.Println("APP_SECRET ausente ou curto: sessões serão perdidas ao reiniciar")
	}

	st, err := db.Open(env("DB_PATH", "data/app.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	svc := app.New(st, location())
	srv, err := web.New(svc, auth, mcpserver.Handler(svc))
	if err != nil {
		return err
	}
	hs := &http.Server{
		Addr:              env("ADDR", ":8080"),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(shutdown)
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
	st, err := db.Open(env("DB_PATH", "data/app.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	empty, err := st.IsEmpty()
	if err != nil {
		return err
	}
	if !empty {
		if !*force {
			return errors.New("o banco já tem dados; use -force para apagar e reimportar")
		}
		if err := st.Reset(); err != nil {
			return err
		}
	}
	return importer.Run(st, fs.Arg(0), today, os.Stdout)
}

func hashPassword() error {
	fmt.Fprint(os.Stderr, "Senha: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return err
	}
	pw := strings.TrimRight(line, "\r\n")
	if len(pw) < 8 {
		return errors.New("use pelo menos 8 caracteres")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	if err != nil {
		return err
	}
	fmt.Println(string(h))
	return nil
}
