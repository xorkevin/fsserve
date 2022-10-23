package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xorkevin.dev/kerrors"
	"xorkevin.dev/klog"
)

type (
	MimeType struct {
		Ext         string `mapstructure:"ext" json:"ext"`
		ContentType string `mapstructure:"contenttype" json:"contentType"`
	}
)

func AddMimeTypes(mimeTypes []MimeType) error {
	for _, i := range mimeTypes {
		if err := mime.AddExtensionType(i.Ext, i.ContentType); err != nil {
			return kerrors.WithMsg(err, fmt.Sprintf("Failed to add mime type %s %s", i.Ext, i.ContentType))
		}
	}
	return nil
}

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
		log     *klog.LevelLogger
		rootSys fs.FS
		httpSys http.FileSystem
		mux     *http.ServeMux
	}

	Opts struct {
		ReadTimeout       time.Duration
		ReadHeaderTimeout time.Duration
		WriteTimeout      time.Duration
		IdleTimeout       time.Duration
		MaxHeaderBytes    int
		GracefulShutdown  time.Duration
	}

	serverSubdir struct {
		log      *klog.LevelLogger
		detector *reqDetector
		fsys     fs.FS
		handler  http.Handler
		route    Route
	}

	serverFile struct {
		log      *klog.LevelLogger
		detector *reqDetector
		fsys     fs.FS
		httpSys  http.FileSystem
		route    Route
	}

	reqDetector struct {
		log *klog.LevelLogger
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

func handleFSError(w http.ResponseWriter, err error) {
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusNotFound)
		return
	}
	if errors.Is(err, fs.ErrPermission) {
		writeError(w, http.StatusForbidden)
		return
	}
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

func (d *reqDetector) readFileBuf(ctx context.Context, fsys fs.FS, name string, buf []byte) (int, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := f.Close(); err != nil {
			d.log.Err(ctx, kerrors.WithMsg(err, fmt.Sprintf("Failed to close open file %s", name)), nil)
		}
	}()
	n, err := io.ReadFull(f, buf)
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return n, kerrors.WithMsg(err, "Failed to read file")
	}
	return n, nil
}

func (d *reqDetector) detectContentType(ctx context.Context, w http.ResponseWriter, fsys fs.FS, name string) error {
	if w.Header().Get(headerContentType) != "" {
		return nil
	}
	ctype := mime.TypeByExtension(filepath.Ext(name))
	if ctype == "" {
		var buf [sniffLen]byte
		n, err := d.readFileBuf(ctx, fsys, name, buf[:])
		if err != nil {
			return err
		}
		ctype = http.DetectContentType(buf[:n])
	}
	w.Header().Set(headerContentType, ctype)
	return nil
}

func (d *reqDetector) detectCompression(ctx context.Context, w http.ResponseWriter, r *http.Request, fsys fs.FS, origPath string, compressed []*Compressed) (string, error) {
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
		if err := d.detectContentType(ctx, w, fsys, origPath); err != nil {
			return "", err
		}
		w.Header().Set(headerContentEncoding, j.Code)
		w.Header().Add(headerVary, headerContentEncoding)
		return origPath + j.Suffix, nil
	}
	return origPath, nil
}

func (s *serverSubdir) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := writeCacheHeaders(w, s.fsys, r.URL.Path, s.route.CacheControl, s.route.ETag); err != nil {
		handleFSError(w, err)
		return
	}
	p, err := s.detector.detectCompression(ctx, w, r, s.fsys, r.URL.Path, s.route.Compressed)
	if err != nil {
		handleFSError(w, err)
		return
	}
	r.URL.Path = p
	s.handler.ServeHTTP(w, r)
}
func (s *serverFile) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := writeCacheHeaders(w, s.fsys, s.route.Path, s.route.CacheControl, s.route.ETag); err != nil {
		handleFSError(w, err)
		return
	}
	// may not use url path here to prevent unwanted file access
	p, err := s.detector.detectCompression(ctx, w, r, s.fsys, s.route.Path, s.route.Compressed)
	if err != nil {
		handleFSError(w, err)
		return
	}

	// may not use url path here to prevent unwanted file access
	f, err := s.httpSys.Open(p)
	if err != nil {
		handleFSError(w, err)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			s.log.Err(r.Context(), kerrors.WithMsg(err, fmt.Sprintf("Failed to close open file %s", p)), nil)
		}
	}()
	stat, err := f.Stat()
	if err != nil {
		handleFSError(w, err)
		return
	}
	if stat.IsDir() {
		writeError(w, http.StatusNotFound)
		return
	}
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), f)
}

func NewServer(l klog.Logger, rootSys fs.FS) *Server {
	return &Server{
		log:     klog.NewLevelLogger(l),
		rootSys: rootSys,
		httpSys: http.FS(rootSys),
	}
}

func (s *Server) Mount(routes []*Route) error {
	s.mux = http.NewServeMux()
	detector := &reqDetector{
		log: s.log,
	}
	for _, i := range routes {
		k := i
		ctx := klog.WithFields(context.Background(), klog.Fields{
			"route.prefix": k.Prefix,
		})
		s.log.Info(ctx, "Handle route", klog.Fields{
			"route.fspath": k.Path,
			"route.dir":    k.Dir,
		})
		for _, j := range k.Compressed {
			if j.regex == nil {
				if j.Test == "" {
					s.log.Info(ctx, "Compressed", klog.Fields{
						"encoding": j.Code,
						"suffix":   j.Suffix,
					})
					continue
				}
				r, err := regexp.Compile(j.Test)
				if err != nil {
					return kerrors.WithMsg(err, fmt.Sprintf("Invalid compressed test regex %s", j.Test))
				}
				j.regex = r
				s.log.Info(ctx, "Compressed", klog.Fields{
					"encoding": j.Code,
					"suffix":   j.Suffix,
					"test":     r.String(),
				})
			}
		}
		if k.Dir {
			fsys, err := fs.Sub(s.rootSys, k.Path)
			if err != nil {
				return kerrors.WithMsg(err, fmt.Sprintf("Failed to get root fs subdir %s", k.Path))
			}
			s.mux.Handle(k.Prefix, http.StripPrefix(k.Prefix, &serverSubdir{
				log: klog.NewLevelLogger(klog.Sub(s.log.Logger, "", klog.Fields{
					"router.path": k.Prefix,
				})),
				detector: detector,
				fsys:     fsys,
				handler:  http.FileServer(http.FS(fsys)),
				route:    *k,
			}))
		} else {
			s.mux.Handle(k.Prefix, &serverFile{
				log: klog.NewLevelLogger(klog.Sub(s.log.Logger, "", klog.Fields{
					"router.path": k.Prefix,
				})),
				detector: detector,
				fsys:     s.rootSys,
				httpSys:  s.httpSys,
				route:    *k,
			})
		}
	}
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) Serve(ctx context.Context, port int, opts Opts) {
	srv := http.Server{
		Addr:              ":" + strconv.Itoa(port),
		Handler:           s,
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
			s.log.Err(context.Background(), kerrors.WithMsg(err, "Shutting down server"), nil)
		}
	}()
	s.log.Info(context.Background(), "HTTP server listening", klog.Fields{
		"server.addr": srv.Addr,
	})
	s.waitForInterrupt(ctx)
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, opts.GracefulShutdown)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		s.log.Err(context.Background(), kerrors.WithMsg(err, "Failed to shut down server"), nil)
	}
}

func (s *Server) waitForInterrupt(ctx context.Context) {
	notifyCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	<-notifyCtx.Done()
}
