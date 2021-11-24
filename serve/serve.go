package serve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"time"
)

type (
	Route struct {
		Prefix       string   `mapstructure:"prefix"`
		Dir          bool     `mapstructure:"dir"`
		Path         string   `mapstructure:"path"`
		CacheControl []string `mapstructure:"cachecontrol"`
		ETag         bool     `mapstructure:"etag"`
	}

	Server struct {
		handler http.Handler
	}

	Opts struct {
		ReadTimeout       time.Duration
		ReadHeaderTimeout time.Duration
		WriteTimeout      time.Duration
		IdleTimeout       time.Duration
		MaxHeaderBytes    int
		GracefulShutdown  time.Duration
	}
)

const (
	ccHeader   = "Cache-Control"
	etagHeader = "ETag"
)

func writeError(w http.ResponseWriter, code int) {
	http.Error(w, http.StatusText(code), code)
}

func handleFSError(w http.ResponseWriter, err error, path string) {
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusNotFound)
		return
	}
	if errors.Is(err, fs.ErrPermission) {
		log.Printf("403 Error serving %s: %v\n", path, err)
		writeError(w, http.StatusForbidden)
		return
	}
	log.Printf("500 Error serving %s: %v\n", path, err)
	writeError(w, http.StatusInternalServerError)
}

func writeCacheHeaders(headers http.Header, fsys fs.FS, path string, cachecontrol []string, etag bool) error {
	for _, j := range cachecontrol {
		headers.Add(ccHeader, j)
	}
	if etag {
		stat, err := fs.Stat(fsys, path)
		if err != nil {
			return err
		}
		headers.Set(etagHeader, fmt.Sprintf(`W/"%x-%x"`, stat.ModTime().Unix(), stat.Size()))
	}
	return nil
}

func NewServer(base string, routes []Route) (*Server, error) {
	rootSys := os.DirFS(base)
	mux := http.NewServeMux()
	for _, i := range routes {
		k := i
		log.Printf("handle %s: %s\n", k.Prefix, k.Path)
		if k.Dir {
			fsys, err := fs.Sub(rootSys, k.Path)
			if err != nil {
				return nil, fmt.Errorf("Failed root fs subdir: %w", err)
			}
			dir := http.FileServer(http.FS(fsys))
			mux.Handle(k.Prefix, http.StripPrefix(k.Prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p := r.URL.Path
				if err := writeCacheHeaders(w.Header(), fsys, p, k.CacheControl, k.ETag); err != nil {
					handleFSError(w, err, path.Join(k.Prefix, p))
					return
				}
				dir.ServeHTTP(w, r)
			})))
		} else {
			mux.Handle(k.Prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := writeCacheHeaders(w.Header(), rootSys, k.Path, k.CacheControl, k.ETag); err != nil {
					handleFSError(w, err, k.Prefix)
					return
				}
				http.ServeFile(w, r, filepath.Join(base, k.Path))
			}))
		}
	}
	return &Server{
		handler: mux,
	}, nil
}

func (s *Server) Serve(ctx context.Context, port int, opts Opts) {
	srv := http.Server{
		Addr:              ":" + strconv.Itoa(port),
		Handler:           s.handler,
		ReadTimeout:       opts.ReadTimeout,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		WriteTimeout:      opts.WriteTimeout,
		IdleTimeout:       opts.IdleTimeout,
		MaxHeaderBytes:    opts.MaxHeaderBytes,
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("Shutting down server: %v\n", err)
		}
	}()
	log.Println("HTTP server listening on :3000")
	s.waitForInterrupt(ctx)
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, opts.GracefulShutdown)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown server error: %v\n", err.Error())
	}
}

func (s *Server) waitForInterrupt(ctx context.Context) {
	notifyCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	<-notifyCtx.Done()
}
