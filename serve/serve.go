package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type (
	Compressed struct {
		Code   string `mapstructure:"code"`
		Test   string `mapstructure:"test"`
		Suffix string `mapstructure:"suffix"`
		regex  *regexp.Regexp
	}

	Route struct {
		Prefix       string        `mapstructure:"prefix"`
		Dir          bool          `mapstructure:"dir"`
		Path         string        `mapstructure:"path"`
		CacheControl []string      `mapstructure:"cachecontrol"`
		ETag         bool          `mapstructure:"etag"`
		Compressed   []*Compressed `mapstructure:"compressed"`
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
	headerAcceptEncoding  = "Accept-Encoding"
	headerCacheControl    = "Cache-Control"
	headerContentEncoding = "Content-Encoding"
	headerContentType     = "Content-Type"
	headerETag            = "ETag"
	headerVary            = "Vary"
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

func writeCacheHeaders(w http.ResponseWriter, fsys fs.FS, path string, cachecontrol []string, etag bool) error {
	for _, j := range cachecontrol {
		w.Header().Add(headerCacheControl, j)
	}
	if etag {
		stat, err := fs.Stat(fsys, path)
		if err != nil {
			return err
		}
		w.Header().Set(headerETag, fmt.Sprintf(`W/"%x-%x"`, stat.ModTime().Unix(), stat.Size()))
	}
	return nil
}

const (
	sniffLen = 512
)

func readFileBuf(fsys fs.FS, name string, buf []byte) (int, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("500 Error closing file %s: %v\n", name, err)
		}
	}()
	n, _ := io.ReadFull(f, buf)
	return n, nil
}

func detectContentType(w http.ResponseWriter, fsys fs.FS, name string) error {
	if w.Header().Get(headerContentType) != "" {
		return nil
	}
	ctype := mime.TypeByExtension(filepath.Ext(name))
	if ctype == "" {
		var buf [sniffLen]byte
		n, err := readFileBuf(fsys, name, buf[:])
		if err != nil {
			return err
		}
		ctype = http.DetectContentType(buf[:n])
	}
	w.Header().Set(headerContentType, ctype)
	return nil
}

func detectCompression(w http.ResponseWriter, r *http.Request, fsys fs.FS, origPath string, compressed []*Compressed) (string, error) {
	encodingsSet := map[string]struct{}{}
	if accept := strings.TrimSpace(r.Header.Get(headerAcceptEncoding)); accept != "" {
		for _, directive := range strings.Split(accept, ",") {
			enc, _, _ := strings.Cut(directive, ";")
			enc = strings.TrimSpace(enc)
			encodingsSet[enc] = struct{}{}
		}
	}
	for _, j := range compressed {
		_, ok := encodingsSet[j.Code]
		if !ok || (j.regex != nil && !j.regex.MatchString(origPath)) {
			continue
		}
		// need to detect content type on original path since mime.TypeByExtension
		// and http.DetectContentType does not handle .gz, .br, etc.
		if err := detectContentType(w, fsys, origPath); err != nil {
			return "", err
		}
		w.Header().Set(headerContentEncoding, j.Code)
		w.Header().Add(headerVary, headerContentEncoding)
		return origPath + j.Suffix, nil
	}
	return origPath, nil
}

func NewServer(base string, routes []*Route) (*Server, error) {
	rootSys := os.DirFS(base)
	mux := http.NewServeMux()
	for _, i := range routes {
		k := i
		log.Printf("handle %s: %s\n", k.Prefix, k.Path)
		for _, j := range k.Compressed {
			if j.regex == nil {
				if j.Test == "" {
					log.Printf("compressed %s: %s\n", j.Code, j.Suffix)
					continue
				}
				r, err := regexp.Compile(j.Test)
				if err != nil {
					return nil, fmt.Errorf("Invalid compressed test regex %s: %w", j.Test, err)
				}
				j.regex = r
				log.Printf("compressed %s %s: %s\n", j.Code, r.String(), j.Suffix)
			}
		}
		if k.Dir {
			fsys, err := fs.Sub(rootSys, k.Path)
			if err != nil {
				return nil, fmt.Errorf("Failed root fs subdir: %w", err)
			}
			dir := http.FileServer(http.FS(fsys))
			mux.Handle(k.Prefix, http.StripPrefix(k.Prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := writeCacheHeaders(w, fsys, r.URL.Path, k.CacheControl, k.ETag); err != nil {
					handleFSError(w, err, path.Join(k.Prefix, r.URL.Path))
					return
				}
				p, err := detectCompression(w, r, fsys, r.URL.Path, k.Compressed)
				if err != nil {
					handleFSError(w, err, path.Join(k.Prefix, r.URL.Path))
					return
				}
				r.URL.Path = p
				dir.ServeHTTP(w, r)
			})))
		} else {
			mux.Handle(k.Prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := writeCacheHeaders(w, rootSys, k.Path, k.CacheControl, k.ETag); err != nil {
					handleFSError(w, err, k.Prefix)
					return
				}
				p, err := detectCompression(w, r, rootSys, k.Path, k.Compressed)
				if err != nil {
					handleFSError(w, err, k.Prefix)
					return
				}
				http.ServeFile(w, r, filepath.Join(base, p))
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
	log.Println("HTTP server listening on " + srv.Addr)
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
