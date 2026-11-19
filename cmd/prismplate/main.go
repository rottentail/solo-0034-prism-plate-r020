package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"prismplate/internal/font"
	"prismplate/internal/repository"
	"prismplate/internal/service"
	"prismplate/internal/transport"
	"prismplate/internal/validate"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	addr := flag.String("listen", "127.0.0.1:7846", "HTTP address")
	root := flag.String("data", "var/prism-plate-v1", "persistent snapshot root")
	flag.Parse()
	repo, err := repository.Open(*root)
	if err != nil {
		return err
	}
	defer repo.Close()
	// A checksum proves identity, not semantic validity. Recompile recovered
	// scenes against the persisted font inventory through the same admission.
	st := repo.View()
	blobs := make(map[string][]byte, len(st.Fonts))
	for id, f := range st.Fonts {
		blobs[id] = f.Data
	}
	set, err := font.ParseAll(blobs)
	if err != nil {
		return err
	}
	for _, p := range st.Plates {
		for _, r := range p.Revisions {
			if _, err = validate.Compile(r.Document, set); err != nil {
				return err
			}
		}
	}
	svc := service.New(repo)
	defer svc.Close()
	server := &http.Server{Addr: *addr, Handler: transport.API{Service: svc}.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Printf("prism-plate ready at %s", *addr)
	select {
	case err = <-done:
		if err != http.ErrServerClosed {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		if err = server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
		<-done
	}
	return nil
}
