package serve

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/netip"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"xorkevin.dev/kerrors"
	"xorkevin.dev/kfs"
	"xorkevin.dev/klog"
)

var (
	// ErrNotFound is returned when a file is not found
	ErrNotFound errNotFound
	// ErrInvalidReq is returned when a file request is invalid
	ErrInvalidReq errInvalidReq
	// ErrMalformedChecksum is returned when a file checksum is malformed
	ErrMalformedChecksum errMalformedChecksum
)

type (
	errNotFound          struct{}
	errInvalidReq        struct{}
	errMalformedChecksum struct{}
)

func (e errNotFound) Error() string {
	return "File not found"
}

func (e errInvalidReq) Error() string {
	return "Invalid file request"
}

func (e errMalformedChecksum) Error() string {
	return "Malformed checksum"
}

type (
	MimeType struct {
		Ext         string `mapstructure:"ext" json:"ext"`
		ContentType string `mapstructure:"contenttype" json:"contenttype"`
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
	Server struct {
		log      *klog.LevelLogger
		dir      fs.FS
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
		log   *klog.LevelLogger
		dir   fs.FS
		route Route
	}

	serverFile struct {
		log   *klog.LevelLogger
		dir   fs.FS
		route Route
	}

	Route struct {
		Prefix             string     `mapstructure:"prefix"`
		Dir                bool       `mapstructure:"dir"`
		Path               string     `mapstructure:"path"`
		Include            string     `mapstructure:"include"`
		Exclude            string     `mapstructure:"exclude"`
		Encodings          []Encoding `mapstructure:"encodings"`
		DefaultContentType string     `mapstructure:"default_content_type"`
		CacheControl       string     `mapstructure:"cachecontrol"`
		include            *regexp.Regexp
		exclude            *regexp.Regexp
	}

	Encoding struct {
		Code  string `mapstructure:"code"`
		Match string `mapstructure:"match"`
		Ext   string `mapstructure:"ext"`
		match *regexp.Regexp
	}

	fileConfig struct {
		path     string
		basename string
		ctype    string
		encoding string
		checksum string
		tag      string
	}
)

const (
	headerAcceptEncoding  = "Accept-Encoding"
	headerCacheControl    = "Cache-Control"
	headerContentEncoding = "Content-Encoding"
	headerContentType     = "Content-Type"
	headerETag            = "ETag"
	headerIfNoneMatch     = "If-None-Match"
	headerVary            = "Vary"
)

func getErrorStatus(err error) int {
	if errors.Is(err, ErrNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, ErrInvalidReq) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func writeError(ctx context.Context, log *klog.LevelLogger, w http.ResponseWriter, err error) {
	status := getErrorStatus(err)

	if status >= http.StatusBadRequest && status < http.StatusInternalServerError {
		log.WarnErr(ctx, err)
	} else {
		log.Err(ctx, err)
	}

	headers := w.Header()
	headers.Del(headerCacheControl)
	headers.Del(headerContentEncoding)
	headers.Del(headerContentType)
	headers.Del(headerETag)
	headers.Del(headerVary)

	http.Error(w, http.StatusText(status), status)
}

func detectEncoding(dir fs.FS, encodings []Encoding, reqHeaders http.Header, name string) (string, fs.FileInfo, string, error) {
	encodingsSet := map[string]struct{}{}
	if accept := strings.TrimSpace(reqHeaders.Get(headerAcceptEncoding)); accept != "" {
		for _, directive := range strings.Split(accept, ",") {
			enc, _, _ := strings.Cut(directive, ";")
			enc = strings.TrimSpace(enc)
			encodingsSet[enc] = struct{}{}
		}
	}
	for _, i := range encodings {
		_, ok := encodingsSet[i.Code]
		if !ok {
			continue
		}
		if i.match != nil {
			if !i.match.MatchString(name) {
				continue
			}
		}
		alt := name + i.Ext
		stat, err := fs.Stat(dir, alt)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return "", nil, "", kerrors.WithMsg(err, fmt.Sprintf("Failed to stat file %s", alt))
		}
		if stat.IsDir() {
			continue
		}
		return alt, stat, i.Code, nil
	}
	stat, err := fs.Stat(dir, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, "", kerrors.WithKind(err, ErrNotFound, fmt.Sprintf("File not found: %s", name))
		}
		return "", nil, "", kerrors.WithMsg(err, fmt.Sprintf("Failed to stat file %s", name))
	}
	if stat.IsDir() {
		return "", nil, "", kerrors.WithKind(nil, ErrInvalidReq, fmt.Sprintf("File %s is a directory", name))
	}
	return name, stat, "", nil
}

const (
	defaultContentType = "application/octet-stream"
)

func detectContentType(name string, fallbackContentType string) string {
	// need to detect content type on original path since mime.TypeByExtension
	// does not handle .gz, .br, etc.
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype != "" {
		return ctype
	}
	if fallbackContentType != "" {
		return fallbackContentType
	}
	return defaultContentType
}

func statToTag(stat fs.FileInfo) string {
	if stat.ModTime().IsZero() {
		return ""
	}
	var etagbytes [16]byte
	binary.BigEndian.PutUint64(etagbytes[:8], uint64(stat.ModTime().UnixMilli()))
	binary.BigEndian.PutUint64(etagbytes[8:], uint64(stat.Size()))
	return base64.RawURLEncoding.EncodeToString(etagbytes[:])
}

func getFileConfig(
	ctx context.Context,
	log *klog.LevelLogger,
	dir fs.FS,
	reqHeaders http.Header,
	name string,
	route Route,
) (*fileConfig, error) {
	ctype := detectContentType(name, route.DefaultContentType)

	p, stat, encoding, err := detectEncoding(dir, route.Encodings, reqHeaders, name)
	if err != nil {
		return nil, err
	}

	currentTag := statToTag(stat)
	var checksum string
	if fullFilePath, err := kfs.FullFilePath(dir, p); err != nil {
		log.Err(ctx, kerrors.WithMsg(err, "Failed to get full file path for file"),
			klog.AString("path", p),
		)
	} else {
		if hash, tag, err := readChecksumXAttr(fullFilePath); err != nil {
			log.Err(ctx, err, klog.AString("path", p))
		} else if tag == currentTag {
			checksum = hash
		}
	}

	return &fileConfig{
		path:     p,
		basename: path.Base(name),
		ctype:    ctype,
		encoding: encoding,
		checksum: checksum,
		tag:      currentTag,
	}, nil
}

func calcWeakETag(tag string) string {
	return `W/"` + tag + `"`
}

func calcStrongETag(tag string) string {
	return `"` + tag + `"`
}

func writeResHeaders(w http.ResponseWriter, reqHeaders http.Header, cfg fileConfig, cachecontrol string) bool {
	// According to RFC7232 section 4.1, server must send same Cache-Control,
	// Content-Location, Date, ETag, Expires, and Vary headers for 304 response
	// as 200 response.
	w.Header().Add(headerVary, headerAcceptEncoding)

	if cachecontrol != "" {
		w.Header().Set(headerCacheControl, cachecontrol)

		// ETag used by [net/http.ServeContent] for byte range requests
		// strong etag allows serving range queries
		// weak etag does not allow range queries
		if cfg.tag != "" {
			weakETag := calcWeakETag(cfg.tag)
			var strongETag string
			if cfg.checksum != "" {
				strongETag = calcStrongETag(cfg.checksum)
			}
			if match := strings.TrimSpace(reqHeaders.Get(headerIfNoneMatch)); match != "" {
				for _, tag := range strings.Split(match, ",") {
					tag = strings.TrimSpace(tag)
					if tag == "" {
						continue
					}
					if tag == strongETag || tag == weakETag {
						w.Header().Set(headerETag, tag)
						w.WriteHeader(http.StatusNotModified)
						return true
					}
				}
			}

			if strongETag != "" {
				w.Header().Set(headerETag, strongETag)
			} else {
				w.Header().Set(headerETag, weakETag)
			}
		}
	}

	if cfg.encoding != "" {
		w.Header().Set(headerContentEncoding, cfg.encoding)
	}
	w.Header().Set(headerContentType, cfg.ctype)
	return false
}

func sendFile(
	ctx context.Context,
	log *klog.LevelLogger,
	dir fs.FS,
	w http.ResponseWriter,
	r *http.Request,
	cfg fileConfig,
) {
	f, err := dir.Open(cfg.path)
	if err != nil {
		writeError(ctx, log, w, kerrors.WithMsg(err, fmt.Sprintf("Failed to open file %s", cfg.path)))
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Err(ctx, kerrors.WithMsg(err, fmt.Sprintf("Failed to close open file %s", cfg.path)))
		}
	}()
	rsf, ok := f.(io.ReadSeeker)
	if !ok {
		writeError(ctx, log, w, kerrors.WithMsg(nil, fmt.Sprintf("FS impl does not support seek for file %s", cfg.path)))
		return
	}
	stat, err := f.Stat()
	if err != nil {
		writeError(ctx, log, w, kerrors.WithMsg(err, fmt.Sprintf("Failed to stat file %s", cfg.path)))
		return
	}
	if stat.IsDir() {
		writeError(ctx, log, w, kerrors.WithMsg(nil, fmt.Sprintf("File %s changed to a directory", cfg.path)))
		return
	}
	if cfg.tag != "" && statToTag(stat) != cfg.tag {
		writeError(ctx, log, w, kerrors.WithMsg(nil, fmt.Sprintf("File changed while handling %s", cfg.path)))
		return
	}
	http.ServeContent(w, r, cfg.basename, stat.ModTime(), rsf)
}

func serveFile(
	log *klog.LevelLogger,
	dir fs.FS,
	w http.ResponseWriter,
	r *http.Request,
	name string,
	route Route,
) {
	ctx := r.Context()

	cfg, err := getFileConfig(ctx, log, dir, r.Header, name, route)
	if err != nil {
		writeError(ctx, log, w, err)
		return
	}

	if writeResHeaders(w, r.Header, *cfg, route.CacheControl) {
		return
	}

	sendFile(ctx, log, dir, w, r, *cfg)
}

func routeMatchPath(route Route, name string) bool {
	if route.include != nil {
		if !route.include.MatchString(name) {
			return false
		}
	}
	if route.exclude != nil {
		if route.exclude.MatchString(name) {
			return false
		}
	}
	return true
}

func (s *serverSubdir) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !routeMatchPath(s.route, r.URL.Path) {
		writeError(r.Context(), s.log, w, kerrors.WithKind(nil, ErrNotFound, fmt.Sprintf("File is not included: %s", r.URL.Path)))
		return
	}
	serveFile(s.log, s.dir, w, r, r.URL.Path, s.route)
}

func (s *serverFile) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// may not use url path here to prevent unwanted file access
	serveFile(s.log, s.dir, w, r, s.route.Path, s.route)
}

func NewServer(l klog.Logger, dir fs.FS, config Config) *Server {
	return &Server{
		log:      klog.NewLevelLogger(l),
		dir:      dir,
		mux:      http.NewServeMux(),
		config:   config,
		reqcount: &atomic.Uint32{},
	}
}

func parseRoutes(routes []Route) error {
	for n, i := range routes {
		for _, j := range i.Encodings {
			if j.Code == "" {
				return kerrors.WithMsg(nil, fmt.Sprintf("Missing encoding code for route %s", i.Prefix))
			}
		}
		if i.Dir {
			if i.Include != "" {
				var err error
				routes[n].include, err = regexp.Compile(i.Include)
				if err != nil {
					return kerrors.WithMsg(err, fmt.Sprintf("Invalid route include regex for route %s", i.Prefix))
				}
			}
			if i.Exclude != "" {
				var err error
				routes[n].exclude, err = regexp.Compile(i.Exclude)
				if err != nil {
					return kerrors.WithMsg(err, fmt.Sprintf("Invalid route include regex for route %s", i.Prefix))
				}
			}
			for m, j := range i.Encodings {
				if j.Match != "" {
					var err error
					i.Encodings[m].match, err = regexp.Compile(j.Match)
					if err != nil {
						return kerrors.WithMsg(err, fmt.Sprintf("Invalid encoding match regex for code %s of route %s", j.Code, i.Prefix))
					}
				}
			}
		}
	}
	return nil
}

func (s *Server) Mount(routes []Route) error {
	if err := parseRoutes(routes); err != nil {
		return err
	}

	s.mux = http.NewServeMux()
	for _, i := range routes {
		s.log.Info(context.Background(), "Handle route",
			klog.AString("route.prefix", i.Prefix),
			klog.AString("route.fspath", i.Path),
			klog.ABool("route.dir", i.Dir),
		)
		log := klog.NewLevelLogger(s.log.Logger.Sublogger("router", klog.AString("router.path", i.Prefix)))
		if i.Dir {
			dir, err := fs.Sub(s.dir, i.Path)
			if err != nil {
				return kerrors.WithMsg(err, fmt.Sprintf("Failed to open subdir %s", i.Path))
			}
			s.mux.Handle(i.Prefix, http.StripPrefix(i.Prefix, &serverSubdir{
				log:   log,
				dir:   dir,
				route: i,
			}))
		} else {
			s.mux.Handle(i.Prefix, &serverFile{
				log:   log,
				dir:   s.dir,
				route: i,
			})
		}
	}
	return nil
}

const (
	headerXForwardedFor = "X-Forwarded-For"
)

func getRealIP(r *http.Request, proxies []netip.Prefix) string {
	host, err := netip.ParseAddrPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return ""
	}
	remoteip := host.Addr()
	if !ipnetsContain(remoteip, proxies) {
		return remoteip.String()
	}

	xff := r.Header.Get(headerXForwardedFor)
	if xff == "" {
		return remoteip.String()
	}

	prev := remoteip
	ipstrs := strings.Split(xff, ",")
	for i := len(ipstrs) - 1; i >= 0; i-- {
		ip, err := netip.ParseAddr(strings.TrimSpace(ipstrs[i]))
		if err != nil {
			return remoteip.String()
		}
		if !ipnetsContain(ip, proxies) {
			return ip.String()
		}
		prev = ip
	}

	return prev.String()
}

func ipnetsContain(ip netip.Addr, ipnet []netip.Prefix) bool {
	for _, i := range ipnet {
		if i.Contains(ip) {
			return true
		}
	}
	return false
}

var base64HexEncoding = base64.NewEncoding("-0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz").WithPadding(base64.NoPadding)

type (
	// Snowflake is a short, time orderable unique identifier
	Snowflake uint64
)

// NewSnowflake returns a new [Snowflake] with a provided seq number
func NewSnowflake(seq uint32) Snowflake {
	now := uint64(time.Now().Round(0).UnixMilli())
	now = now << 24
	now |= (uint64(seq) & 0xffffff)
	return Snowflake(now)
}

// Base64 returns the full raw bytes encoded in unpadded base64hex
func (s Snowflake) Base64() string {
	var u [8]byte
	binary.BigEndian.PutUint64(u[:], uint64(s))
	return base64HexEncoding.EncodeToString(u[:])
}

// NewRandSnowflake returns a new [Snowflake] with random bytes for the seq
func NewRandSnowflake() (Snowflake, error) {
	var u [3]byte
	_, err := rand.Read(u[:])
	if err != nil {
		return 0, kerrors.WithMsg(err, "Failed reading crypto/rand")
	}
	k := uint32(u[0])
	k |= uint32(u[1]) << 8
	k |= uint32(u[2]) << 16
	return NewSnowflake(k), nil
}

func (s *Server) lreqID() string {
	return NewSnowflake(s.reqcount.Add(1)).Base64() + s.config.Instance
}

type (
	serverResponseWriter struct {
		w           http.ResponseWriter
		status      int
		wroteHeader bool
	}
)

func (w *serverResponseWriter) Header() http.Header {
	return w.w.Header()
}

func (w *serverResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		w.w.WriteHeader(status)
		return
	}
	w.status = status
	w.wroteHeader = true
	w.w.WriteHeader(status)
}

func (w *serverResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.w.Write(p)
}

func (w *serverResponseWriter) Unwrap() http.ResponseWriter {
	return w.w
}

var allowedHTTPMethods = map[string]struct{}{
	http.MethodGet:  {},
	http.MethodHead: {},
}

func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if _, ok := allowedHTTPMethods[r.Method]; !ok {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lreqid := s.lreqID()
	realip := getRealIP(r, s.config.Proxies)
	ctx = klog.CtxWithAttrs(ctx,
		klog.AString("http.host", r.Host),
		klog.AString("http.method", r.Method),
		klog.AString("http.reqpath", r.URL.EscapedPath()),
		klog.AString("http.remote", r.RemoteAddr),
		klog.AString("http.realip", realip),
		klog.AString("http.lreqid", lreqid),
	)
	r = r.WithContext(ctx)
	w2 := &serverResponseWriter{
		w:      w,
		status: 0,
	}
	s.log.Info(ctx, "HTTP request")
	start := time.Now()
	s.handleHTTP(w2, r)
	duration := time.Since(start)
	s.log.Info(ctx, "HTTP response",
		klog.AInt("http.status", w2.status),
		klog.AInt64("http.latency_us", duration.Microseconds()),
	)
}

func (s *Server) Serve(ctx context.Context, port int, opts Opts) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := http.Server{
		Addr:              ":" + strconv.Itoa(port),
		Handler:           s,
		ReadTimeout:       opts.ReadTimeout,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		WriteTimeout:      opts.WriteTimeout,
		IdleTimeout:       opts.IdleTimeout,
		MaxHeaderBytes:    opts.MaxHeaderBytes,
	}
	go func() {
		defer cancel()
		if err := srv.ListenAndServe(); err != nil {
			s.log.Err(context.Background(), kerrors.WithMsg(err, "Shutting down server"))
		}
	}()
	s.log.Info(context.Background(), "HTTP server listening",
		klog.AString("http.server.addr", srv.Addr),
	)
	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(klog.ExtendCtx(context.Background(), ctx), opts.GracefulShutdown)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		s.log.Err(context.Background(), kerrors.WithMsg(err, "Failed to shut down server"))
	}
}
