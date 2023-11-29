package serve

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"path"
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
		log        *klog.LevelLogger
		db         TreeDB
		contentDir fs.FS
		mux        *http.ServeMux
		config     Config
		reqcount   *atomic.Uint32
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
		log        *klog.LevelLogger
		db         TreeDB
		contentSys http.FileSystem
		route      Route
	}

	serverFile struct {
		log        *klog.LevelLogger
		db         TreeDB
		contentSys http.FileSystem
		route      Route
	}

	Route struct {
		Prefix       string `mapstructure:"prefix"`
		Dir          bool   `mapstructure:"dir"`
		Path         string `mapstructure:"path"`
		CacheControl string `mapstructure:"cachecontrol"`
	}

	contentFile struct {
		basename string
		hash     string
		ctype    string
		encoding string
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
	return http.StatusBadRequest
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

func detectEncoding(cfg ContentConfig, reqHeaders http.Header) (string, string) {
	encodingsSet := map[string]struct{}{}
	if accept := strings.TrimSpace(reqHeaders.Get(headerAcceptEncoding)); accept != "" {
		for _, directive := range strings.Split(accept, ",") {
			enc, _, _ := strings.Cut(directive, ";")
			enc = strings.TrimSpace(enc)
			encodingsSet[enc] = struct{}{}
		}
	}
	for _, i := range cfg.Encoded {
		_, ok := encodingsSet[i.Code]
		if !ok {
			continue
		}
		return i.Hash, i.Code
	}
	return cfg.Hash, ""
}

const (
	defaultContentType = "application/octet-stream"
)

func detectContentType(cfg ContentConfig, name string) string {
	ctype := cfg.ContentType
	if ctype != "" {
		return ctype
	}
	// need to detect content type on original path since mime.TypeByExtension
	// does not handle .gz, .br, etc.
	ctype = mime.TypeByExtension(path.Ext(name))
	if ctype != "" {
		return ctype
	}
	return defaultContentType
}

func getContentConfig(
	ctx context.Context,
	d TreeDB,
	reqHeaders http.Header,
	name string,
) (*contentFile, error) {
	cfg, err := d.Get(ctx, name)
	if err != nil {
		return nil, kerrors.WithMsg(err, fmt.Sprintf("Failed to get content config for %s", name))
	}

	hash, encoding := detectEncoding(*cfg, reqHeaders)
	ctype := detectContentType(*cfg, name)

	return &contentFile{
		basename: path.Base(name),
		hash:     hash,
		ctype:    ctype,
		encoding: encoding,
	}, nil
}

func writeResHeaders(w http.ResponseWriter, reqHeaders http.Header, cfg contentFile, cachecontrol string) bool {
	// According to RFC7232 section 4.1, server must send same Cache-Control,
	// Content-Location, Date, ETag, Expires, and Vary headers for 304 response
	// as 200 response.
	w.Header().Add(headerVary, headerAcceptEncoding)

	if cachecontrol != "" {
		// strong etag since content is addressed by hash
		etag := `"` + url.QueryEscape(cfg.hash) + `"`

		w.Header().Set(headerCacheControl, cachecontrol)
		// ETag also used by [net/http.ServeContent] for byte range requests
		w.Header().Set(headerETag, etag)

		if v := reqHeaders.Get(headerIfNoneMatch); v == etag {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}

	w.Header().Set(headerContentEncoding, cfg.encoding)
	w.Header().Set(headerContentType, cfg.ctype)
	return false
}

func sendFile(
	ctx context.Context,
	log *klog.LevelLogger,
	contentSys http.FileSystem,
	w http.ResponseWriter,
	r *http.Request,
	cfg contentFile,
	cachecontrol string,
) {
	f, err := contentSys.Open(cfg.hash)
	if err != nil {
		writeError(ctx, log, w, kerrors.WithMsg(err, fmt.Sprintf("Failed to open file %s", cfg.hash)))
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Err(ctx, kerrors.WithMsg(err, fmt.Sprintf("Failed to close open file %s", cfg.hash)))
		}
	}()
	stat, err := f.Stat()
	if err != nil {
		writeError(ctx, log, w, kerrors.WithMsg(err, fmt.Sprintf("Failed to stat file %s", cfg.hash)))
		return
	}
	if stat.IsDir() {
		writeError(ctx, log, w, kerrors.WithMsg(nil, fmt.Sprintf("File %s is a directory", cfg.hash)))
		return
	}
	http.ServeContent(w, r, cfg.basename, stat.ModTime(), f)
}

func serveFile(
	log *klog.LevelLogger,
	d TreeDB,
	contentSys http.FileSystem,
	w http.ResponseWriter,
	r *http.Request,
	name string,
	cachecontrol string,
) {
	ctx := r.Context()

	cfg, err := getContentConfig(ctx, d, r.Header, name)
	if err != nil {
		writeError(ctx, log, w, err)
		return
	}

	if writeResHeaders(w, r.Header, *cfg, cachecontrol) {
		return
	}

	sendFile(ctx, log, contentSys, w, r, *cfg, cachecontrol)
}

func (s *serverSubdir) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	serveFile(s.log, s.db, s.contentSys, w, r, path.Join(s.route.Path, r.URL.Path), s.route.CacheControl)
}

func (s *serverFile) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// may not use url path here to prevent unwanted file access
	serveFile(s.log, s.db, s.contentSys, w, r, s.route.Path, s.route.CacheControl)
}

func NewServer(l klog.Logger, treedb TreeDB, contentDir fs.FS, config Config) *Server {
	return &Server{
		log:        klog.NewLevelLogger(l),
		db:         treedb,
		contentDir: contentDir,
		mux:        http.NewServeMux(),
		config:     config,
		reqcount:   &atomic.Uint32{},
	}
}

func (s *Server) Mount(routes []Route) error {
	s.mux = http.NewServeMux()
	contentSys := http.FS(s.contentDir)
	for _, i := range routes {
		i := i
		s.log.Info(context.Background(), "Handle route",
			klog.AString("route.prefix", i.Prefix),
			klog.AString("route.fspath", i.Path),
			klog.ABool("route.dir", i.Dir),
		)
		log := klog.NewLevelLogger(s.log.Logger.Sublogger("router", klog.AString("router.path", i.Prefix)))
		if i.Dir {
			s.mux.Handle(i.Prefix, http.StripPrefix(i.Prefix, &serverSubdir{
				log:        log,
				db:         s.db,
				contentSys: contentSys,
				route:      i,
			}))
		} else {
			s.mux.Handle(i.Prefix, &serverFile{
				log:        log,
				db:         s.db,
				contentSys: contentSys,
				route:      i,
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
