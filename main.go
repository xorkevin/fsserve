package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"
)

const (
	defaultMaxHeaderSize = 1 << 20 // 1MB

	seconds5 = 5 * time.Second
	seconds2 = 2 * time.Second
)

// Cache control directives
const (
	ccHeader = "Cache-Control"
	// Cacheability
	ccPublic = "public"
	// Expiration
	ccMaxAge = "max-age"
)

func main() {
	mux := http.NewServeMux()
	dir := http.FileServer(http.Dir("./static"))
	mux.Handle("/static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add(ccHeader, string(ccPublic))
		w.Header().Add(ccHeader, fmt.Sprintf("%s=%d", ccMaxAge, 31536000))
		dir.ServeHTTP(w, r)
	})))
	mux.Handle("/favicon.ico", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "favicon.ico")
	}))
	mux.Handle("/manifest.json", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "manifest.json")
	}))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "index.html")
	}))
	srv := http.Server{
		Addr:              ":3000",
		Handler:           mux,
		ReadTimeout:       seconds5,
		ReadHeaderTimeout: seconds2,
		WriteTimeout:      seconds5,
		IdleTimeout:       seconds5,
		MaxHeaderBytes:    defaultMaxHeaderSize,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("Shutting down server: %v\n", err)
		}
	}()
	log.Println("HTTP server listening on :3000")
	waitForInterrupt(ctx)
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), seconds5)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown server error: %v\n", err.Error())
	}
}

func waitForInterrupt(ctx context.Context) {
	notifyCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	<-notifyCtx.Done()
}
