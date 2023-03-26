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
	"path"
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
		Prefix       string       `mapstructure:"prefix"`
		Dir          bool         `mapstructure:"dir"`
		Path         string       `mapstructure:"path"`
		CacheControl string       `mapstructure:"cachecontrol"`
		Compressed   []Compressed `mapstructure:"compressed"`
	}

	Server struct {
		log      *klog.LevelLogger
		rootSys  fs.FS
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
		log     *klog.LevelLogger
		fsys    fs.FS
		httpSys http.FileSystem
		route   Route
	}

	serverFile struct {
		log     *klog.LevelLogger
		fsys    fs.FS
		httpSys http.FileSystem
		route   Route
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
	headerIfNoneMatch     = "If-None-Match"
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

func getFileStat(fsys fs.FS, p string) (fs.FileInfo, error) {
	stat, err := fs.Stat(fsys, p)
	if err != nil {
		return nil, kerrors.WithMsg(err, fmt.Sprintf("Failed to stat file %s", p))
	}
	if stat.IsDir() {
		return nil, kerrors.WithKind(nil, fs.ErrNotExist, fmt.Sprintf("File %s is a directory", p))
	}
	return stat, nil
}

func writeCacheHeaders(w http.ResponseWriter, headers http.Header, stat fs.FileInfo, cachecontrol string) bool {
	if cachecontrol != "" {
		etag := fmt.Sprintf(`W/"%x-%x"`, stat.ModTime().Unix(), stat.Size())

		if v := headers.Get(headerIfNoneMatch); v == etag {
			return true
		}

		w.Header().Set(headerCacheControl, cachecontrol)
		// ETag will also be used by [net/http.ServeContent] to send 304 not modified
		w.Header().Set(headerETag, etag)
	}
	return false
}

const (
	sniffLen = 512
)

func readFileBuf(fsys fs.FS, name string, buf []byte) (_ int, retErr error) {
	f, err := fsys.Open(name)
	if err != nil {
		return 0, kerrors.WithMsg(err, fmt.Sprintf("Failed to open file %s", name))
	}
	defer func() {
		if err := f.Close(); err != nil {
			retErr = errors.Join(retErr, kerrors.WithMsg(err, fmt.Sprintf("Failed to close open file %s", name)))
		}
	}()
	n, err := io.ReadFull(f, buf)
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return n, kerrors.WithMsg(err, fmt.Sprintf("Failed to read file %s", name))
	}
	return n, nil
}

func detectContentType(w http.ResponseWriter, fsys fs.FS, name string) error {
	if w.Header().Get(headerContentType) != "" {
		return nil
	}
	ctype := mime.TypeByExtension(path.Ext(name))
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

func detectCompression(ctx context.Context, w http.ResponseWriter, headers http.Header, fsys fs.FS, origPath string, compressed []Compressed) (string, error) {
	encodingsSet := map[string]struct{}{}
	if accept := strings.TrimSpace(headers.Get(headerAcceptEncoding)); accept != "" {
		for _, directive := range strings.Split(accept, ",") {
			enc, _, _ := strings.Cut(directive, ";")
			enc = strings.TrimSpace(enc)
			encodingsSet[enc] = struct{}{}
		}
	}
	for _, j := range compressed {
		_, ok := encodingsSet[j.Code]
		// if regex is nil, then test was unspecified, and should always match
		if !ok || (j.regex != nil && !j.regex.MatchString(origPath)) {
			continue
		}

		compressedPath := origPath + j.Suffix
		stat, err := fs.Stat(fsys, compressedPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
				continue
			}
			return "", kerrors.WithMsg(err, fmt.Sprintf("Failed to stat file %s", compressedPath))
		}
		if stat.IsDir() {
			continue
		}

		w.Header().Set(headerContentEncoding, j.Code)
		return compressedPath, nil
	}
	return origPath, nil
}

func detectFilepath(
	ctx context.Context,
	log *klog.LevelLogger,
	w http.ResponseWriter,
	headers http.Header,
	fsys fs.FS,
	origPath string,
	cachecontrol string,
	compressed []Compressed,
) (string, fs.FileInfo, bool) {
	stat, err := getFileStat(fsys, origPath)
	if err != nil {
		writeError(ctx, log, w, err)
		return "", nil, true
	}

	// According to RFC7232 section 4.1, server must send same Cache-Control,
	// Content-Location, Date, ETag, Expires, and Vary headers for 304 response
	// as 200 response.
	w.Header().Add(headerVary, headerAcceptEncoding)

	if notModified := writeCacheHeaders(w, headers, stat, cachecontrol); notModified {
		w.WriteHeader(http.StatusNotModified)
		return "", nil, true
	}

	// need to detect content type on original path since mime.TypeByExtension
	// and http.DetectContentType does not handle .gz, .br, etc.
	if err := detectContentType(w, fsys, origPath); err != nil {
		writeError(ctx, log, w, err)
		return "", nil, true
	}

	p, err := detectCompression(ctx, w, headers, fsys, origPath, compressed)
	if err != nil {
		writeError(ctx, log, w, kerrors.WithMsg(err, "Failed to detect compression"))
		return "", nil, true
	}

	return p, stat, false
}

func serveFile(ctx context.Context, log *klog.LevelLogger, w http.ResponseWriter, r *http.Request, httpSys http.FileSystem, p string, origStat fs.FileInfo) {
	f, err := httpSys.Open(p)
	if err != nil {
		writeError(ctx, log, w, kerrors.WithMsg(err, fmt.Sprintf("Failed to open file %s", p)))
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Err(ctx, kerrors.WithMsg(err, fmt.Sprintf("Failed to close open file %s", p)))
		}
	}()
	stat, err := f.Stat()
	if err != nil {
		writeError(ctx, log, w, kerrors.WithMsg(err, fmt.Sprintf("Failed to stat file %s", p)))
		return
	}
	if stat.IsDir() {
		writeError(ctx, log, w, kerrors.WithKind(nil, fs.ErrNotExist, fmt.Sprintf("File %s is a directory", p)))
		return
	}
	http.ServeContent(w, r, origStat.Name(), origStat.ModTime(), f)
}

func (s *serverSubdir) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, stat, wroteResponse := detectFilepath(ctx, s.log, w, r.Header, s.fsys, r.URL.Path, s.route.CacheControl, s.route.Compressed)
	if wroteResponse {
		return
	}
	serveFile(ctx, s.log, w, r, s.httpSys, p, stat)
}

func (s *serverFile) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// may not use url path here to prevent unwanted file access
	p, stat, wroteResponse := detectFilepath(ctx, s.log, w, r.Header, s.fsys, s.route.Path, s.route.CacheControl, s.route.Compressed)
	if wroteResponse {
		return
	}
	serveFile(ctx, s.log, w, r, s.httpSys, p, stat)
}

func NewServer(l klog.Logger, rootSys fs.FS, config Config) *Server {
	return &Server{
		log:      klog.NewLevelLogger(l),
		rootSys:  rootSys,
		mux:      http.NewServeMux(),
		config:   config,
		reqcount: &atomic.Uint32{},
	}
}

func (s *Server) Mount(routes []Route) error {
	s.mux = http.NewServeMux()
	rootHTTPSys := http.FS(s.rootSys)
	for _, i := range routes {
		k := i
		ctx := klog.CtxWithAttrs(context.Background(), klog.AString("route.prefix", k.Prefix))
		s.log.Info(ctx, "Handle route",
			klog.AString("route.fspath", k.Path),
			klog.ABool("route.dir", k.Dir),
		)
		compressed := make([]Compressed, 0, len(k.Compressed))
		for _, j := range k.Compressed {
			if j.regex == nil {
				if j.Test == "" {
					compressed = append(compressed, j)
					s.log.Info(ctx, "Compressed",
						klog.AString("compressed.encoding", j.Code),
						klog.AString("compressed.suffix", j.Suffix),
					)
					continue
				}
				r, err := regexp.Compile(j.Test)
				if err != nil {
					return kerrors.WithMsg(err, fmt.Sprintf("Invalid compressed test regex %s", j.Test))
				}
				j.regex = r
				compressed = append(compressed, j)
				s.log.Info(ctx, "Compressed",
					klog.AString("compressed.encoding", j.Code),
					klog.AString("compressed.test", r.String()),
					klog.AString("compressed.suffix", j.Suffix),
				)
			}
		}
		k.Compressed = compressed
		log := klog.NewLevelLogger(s.log.Logger.Sublogger("router", klog.AString("router.path", k.Prefix)))
		if k.Dir {
			fsys, err := fs.Sub(s.rootSys, k.Path)
			if err != nil {
				return kerrors.WithMsg(err, fmt.Sprintf("Failed to get root fs subdir %s", k.Path))
			}
			s.mux.Handle(k.Prefix, http.StripPrefix(k.Prefix, &serverSubdir{
				log:     log,
				fsys:    fsys,
				httpSys: http.FS(fsys),
				route:   k,
			}))
		} else {
			s.mux.Handle(k.Prefix, &serverFile{
				log:     log,
				fsys:    s.rootSys,
				httpSys: rootHTTPSys,
				route:   k,
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

var base32RawHexEncoding = base32.HexEncoding.WithPadding(base32.NoPadding)

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
		ResponseWriter: w,
		status:         0,
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
