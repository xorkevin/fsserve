package serve

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
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
		CacheControl string        `mapstructure:"cachecontrol"`
		ETag         bool          `mapstructure:"etag"`
		Compressed   []*Compressed `mapstructure:"compressed"`
	}

	Server struct {
		log      *klog.LevelLogger
		rootSys  fs.FS
		httpSys  http.FileSystem
		mux      *http.ServeMux
		config   Config
		reqcount *atomic.Uint32
	}

	Config struct {
		Instance string
		Proxies  []netip.Prefix
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

func getFSErrorStatus(err error) int {
	if errors.Is(err, fs.ErrNotExist) {
		return http.StatusNotFound
	}
	if errors.Is(err, fs.ErrPermission) {
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}

func writeError(ctx context.Context, log *klog.LevelLogger, w http.ResponseWriter, err error) {
	status := getFSErrorStatus(err)

	if status >= http.StatusBadRequest && status < http.StatusInternalServerError {
		log.WarnErr(ctx, err, nil)
	} else {
		log.Err(ctx, err, nil)
	}

	w.Header().Del(headerCacheControl)
	w.Header().Del(headerContentEncoding)
	w.Header().Del(headerContentType)
	w.Header().Del(headerETag)
	w.Header().Del(headerVary)

	http.Error(w, http.StatusText(status), status)
}

func writeCacheHeaders(w http.ResponseWriter, fsys fs.FS, path string, cachecontrol string, hasETag bool) error {
	if cachecontrol != "" {
		w.Header().Set(headerCacheControl, cachecontrol)
		if hasETag {
			stat, err := fs.Stat(fsys, path)
			if err != nil {
				return kerrors.WithMsg(err, fmt.Sprintf("Failed to stat file %s", path))
			}
			w.Header().Set(headerETag, fmt.Sprintf(`W/"%x-%x"`, stat.ModTime().Unix(), stat.Size()))
		}
	}
	return nil
}

const (
	sniffLen = 512
)

func (d *reqDetector) readFileBuf(ctx context.Context, fsys fs.FS, name string, buf []byte) (int, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return 0, kerrors.WithMsg(err, fmt.Sprintf("Failed to open file %s", name))
	}
	defer func() {
		if err := f.Close(); err != nil {
			d.log.Err(ctx, kerrors.WithMsg(err, fmt.Sprintf("Failed to close open file %s", name)), nil)
		}
	}()
	n, err := io.ReadFull(f, buf)
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return n, kerrors.WithMsg(err, fmt.Sprintf("Failed to read file %s", name))
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
			return kerrors.WithMsg(err, fmt.Sprintf("Failed to sample file %s", name))
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
			return "", kerrors.WithMsg(err, fmt.Sprintf("Failed to detect content type %s", origPath))
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
		writeError(ctx, s.log, w, kerrors.WithMsg(err, "Failed to write cache headers"))
		return
	}
	p, err := s.detector.detectCompression(ctx, w, r, s.fsys, r.URL.Path, s.route.Compressed)
	if err != nil {
		writeError(ctx, s.log, w, kerrors.WithMsg(err, "Failed to detect compression"))
		return
	}
	r.URL.Path = p
	s.handler.ServeHTTP(w, r)
}
func (s *serverFile) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := writeCacheHeaders(w, s.fsys, s.route.Path, s.route.CacheControl, s.route.ETag); err != nil {
		writeError(ctx, s.log, w, kerrors.WithMsg(err, "Failed to write cache headers"))
		return
	}
	// may not use url path here to prevent unwanted file access
	p, err := s.detector.detectCompression(ctx, w, r, s.fsys, s.route.Path, s.route.Compressed)
	if err != nil {
		writeError(ctx, s.log, w, kerrors.WithMsg(err, "Failed to detect compression"))
		return
	}

	// may not use url path here to prevent unwanted file access
	f, err := s.httpSys.Open(p)
	if err != nil {
		writeError(ctx, s.log, w, kerrors.WithMsg(err, fmt.Sprintf("Failed to open file %s", p)))
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			s.log.Err(r.Context(), kerrors.WithMsg(err, fmt.Sprintf("Failed to close open file %s", p)), nil)
		}
	}()
	stat, err := f.Stat()
	if err != nil {
		writeError(ctx, s.log, w, kerrors.WithMsg(err, fmt.Sprintf("Failed to stat file %s", p)))
		return
	}
	if stat.IsDir() {
		writeError(ctx, s.log, w, kerrors.WithKind(nil, fs.ErrNotExist, fmt.Sprintf("File %s is directory", p)))
		return
	}
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), f)
}

func NewServer(l klog.Logger, rootSys fs.FS, config Config) *Server {
	return &Server{
		log:      klog.NewLevelLogger(l),
		rootSys:  rootSys,
		httpSys:  http.FS(rootSys),
		config:   config,
		reqcount: &atomic.Uint32{},
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

const (
	headerXForwardedFor = "X-Forwarded-For"
)

func getForwardedForIP(r *http.Request, proxies []netip.Prefix) string {
	xff := r.Header.Get(headerXForwardedFor)
	if xff == "" {
		return ""
	}

	ipstrs := strings.Split(xff, ",")
	for i := len(ipstrs) - 1; i >= 0; i-- {
		ip, err := netip.ParseAddr(strings.TrimSpace(ipstrs[i]))
		if err != nil {
			break
		}
		if !ipnetsContain(ip, proxies) {
			return ip.String()
		}
	}

	return ""
}

func ipnetsContain(ip netip.Addr, ipnet []netip.Prefix) bool {
	for _, i := range ipnet {
		if i.Contains(ip) {
			return true
		}
	}
	return false
}

var (
	base32RawHexEncoding = base32.HexEncoding.WithPadding(base32.NoPadding)
)

const (
	timeSize = 8
)

// NewSnowflake creates a new snowflake uid
func NewSnowflake(randsize int) (string, error) {
	u := make([]byte, timeSize+randsize)
	now := uint64(time.Now().Round(0).UnixMilli())
	binary.BigEndian.PutUint64(u[:timeSize], now)
	_, err := rand.Read(u[timeSize:])
	if err != nil {
		return "", kerrors.WithMsg(err, "Failed reading crypto/rand")
	}
	return strings.ToLower(base32RawHexEncoding.EncodeToString(u)), nil
}

const (
	reqIDUnusedTimeSize    = 3
	reqIDTimeSize          = 5
	reqIDTotalTimeSize     = reqIDUnusedTimeSize + reqIDTimeSize
	reqIDCounterSize       = 3
	reqIDUnusedCounterSize = 1
	reqIDTotalCounterSize  = reqIDCounterSize + reqIDUnusedCounterSize
	reqIDSize              = reqIDTimeSize + reqIDCounterSize
	reqIDCounterShift      = 8 * reqIDUnusedCounterSize
)

func (s *Server) lreqID() string {
	count := s.reqcount.Add(1)
	// id looks like:
	// reqIDUnusedTimeSize | reqIDTimeSize | reqIDCounterSize | reqIDUnusedCounterSize
	b := [reqIDTotalTimeSize + reqIDTotalCounterSize]byte{}
	now := uint64(time.Now().Round(0).UnixMilli())
	binary.BigEndian.PutUint64(b[:reqIDTotalTimeSize], now)
	binary.BigEndian.PutUint32(b[reqIDTotalTimeSize:], count<<reqIDCounterShift)
	return s.config.Instance + "-" + strings.ToLower(base32RawHexEncoding.EncodeToString(b[reqIDUnusedTimeSize:reqIDUnusedTimeSize+reqIDSize]))
}

type (
	serverResponseWriter struct {
		http.ResponseWriter
		status      int
		wroteHeader bool
	}
)

func (w *serverResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *serverResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lreqid := s.lreqID()
	forwarded := getForwardedForIP(r, s.config.Proxies)
	ctx = klog.WithFields(ctx, klog.Fields{
		"http.host":      r.Host,
		"http.method":    r.Method,
		"http.reqpath":   r.URL.EscapedPath(),
		"http.remote":    r.RemoteAddr,
		"http.forwarded": forwarded,
		"http.lreqid":    lreqid,
	})
	r = r.WithContext(ctx)
	w2 := &serverResponseWriter{
		ResponseWriter: w,
		status:         0,
	}
	s.log.Info(ctx, "HTTP request", nil)
	start := time.Now()
	s.mux.ServeHTTP(w2, r)
	duration := time.Since(start)
	s.log.Info(ctx, "HTTP response", klog.Fields{
		"http.status":     w2.status,
		"http.latency_us": duration.Microseconds(),
	})
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
		"http.server.addr": srv.Addr,
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
