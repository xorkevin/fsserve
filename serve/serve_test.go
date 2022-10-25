package serve

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"
	"xorkevin.dev/klog"
)

type (
	mountLog struct {
		Level    string `json:"level"`
		Path     string `json:"path"`
		Msg      string `json:"msg"`
		Prefix   string `json:"route.prefix"`
		FSPath   string `json:"route.fspath"`
		Dir      bool   `json:"route.dir"`
		Encoding string `json:"compressed.encoding"`
		Test     string `json:"compressed.test"`
		Suffix   string `json:"compressed.suffix"`
	}

	httpLog struct {
		Level   string `json:"level"`
		Path    string `json:"path"`
		Msg     string `json:"msg"`
		Host    string `json:"http.host"`
		Method  string `json:"http.method"`
		ReqPath string `json:"http.reqpath"`
		Remote  string `json:"http.remote"`
		RealIP  string `json:"http.realip"`
		LReqID  string `json:"http.lreqid"`
		Status  int    `json:"http.status"`
		Latency int    `json:"http.latency_us"`
	}
)

func checkHTTPLog(t *testing.T, jdec *json.Decoder, path string, remoteaddr string, realip string, status int, otherLogs []string) {
	t.Helper()

	assert := require.New(t)

	var vlog httpLog
	assert.NoError(jdec.Decode(&vlog))
	assert.True(vlog.Host != "")
	assert.True(strings.HasPrefix(vlog.LReqID, "testinstance"))
	assert.Equal(httpLog{
		Level:   klog.LevelInfo.String(),
		Msg:     "HTTP request",
		Host:    vlog.Host,
		Method:  http.MethodGet,
		ReqPath: path,
		Remote:  remoteaddr,
		RealIP:  realip,
		LReqID:  vlog.LReqID,
		Status:  0,
		Latency: 0,
	}, vlog)
	for _, i := range otherLogs {
		assert.NoError(jdec.Decode(&vlog))
		assert.Contains(vlog.Msg, i)
	}
	assert.NoError(jdec.Decode(&vlog))
	assert.True(vlog.Host != "")
	assert.True(vlog.LReqID != "")
	assert.True(vlog.Latency != 0)
	assert.Equal(httpLog{
		Level:   klog.LevelInfo.String(),
		Msg:     "HTTP response",
		Host:    vlog.Host,
		Method:  http.MethodGet,
		ReqPath: path,
		Remote:  remoteaddr,
		RealIP:  realip,
		LReqID:  vlog.LReqID,
		Status:  status,
		Latency: vlog.Latency,
	}, vlog)
	assert.False(jdec.More())
}

func TestServer(t *testing.T) {
	t.Parallel()

	assert := require.New(t)

	now := time.Now()
	var filemode fs.FileMode = 0644

	fsys := fstest.MapFS{
		"static/icon/someicon.png": &fstest.MapFile{
			Data:    []byte(`this is a test image file`),
			Mode:    filemode,
			ModTime: now,
		},
		"static/testfile.js": &fstest.MapFile{
			Data:    []byte(`this is a test js file`),
			Mode:    filemode,
			ModTime: now,
		},
		"static/fileunknownext": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE HTML>`),
		},
		"static/test.html": &fstest.MapFile{
			Data: []byte(`sample html file`),
		},
		"static/test.html.gz/textfile": &fstest.MapFile{
			Data: []byte(`sample text file`),
		},
		"subdir/textfile": &fstest.MapFile{
			Data: []byte(`sample text file`),
		},
		"manifest.json": &fstest.MapFile{
			Data:    []byte(`this is a test json file`),
			Mode:    filemode,
			ModTime: now,
		},
		"index.html": &fstest.MapFile{
			Data:    []byte(`this is a test index html file`),
			Mode:    filemode,
			ModTime: now,
		},
	}
	{
		gw := gzip.NewWriter(nil)
		for _, i := range []string{
			"static/testfile.js",
			"manifest.json",
			"index.html",
		} {
			f := fsys[i]
			assert.NotNil(f)
			b := bytes.Buffer{}
			gw.Reset(&b)
			_, err := gw.Write(f.Data)
			assert.NoError(err)
			assert.NoError(gw.Close())
			fsys[i+".gz"] = &fstest.MapFile{
				Data:    b.Bytes(),
				Mode:    filemode,
				ModTime: now,
			}
		}
	}

	routes := []Route{
		{
			Prefix:       "/static/icon/",
			Dir:          true,
			Path:         "static/icon",
			CacheControl: "public, max-age=31536000, no-cache",
		},
		{
			Prefix:       "/static/",
			Dir:          true,
			Path:         "static",
			CacheControl: "public, max-age=31536000, immutable",
			Compressed: []Compressed{
				{
					Code:   "gzip",
					Test:   `\.(html|js|css|json)(\.map)?$`,
					Suffix: ".gz",
				},
				{
					Code:   "deflate",
					Test:   `\.(html|js|css|json)(\.map)?$`,
					Suffix: ".zz",
				},
			},
		},
		{
			Prefix:       "/manifest.json",
			Path:         "manifest.json",
			CacheControl: "public, max-age=31536000, no-cache",
			Compressed: []Compressed{
				{
					Code:   "gzip",
					Suffix: ".gz",
				},
				{
					Code:   "deflate",
					Suffix: ".zz",
				},
			},
		},
		{
			Prefix: "/bogus",
			Path:   "bogus",
		},
		{
			Prefix: "/subdir",
			Path:   "subdir",
		},
		{
			Prefix:       "/",
			Path:         "index.html",
			CacheControl: "public, max-age=31536000, no-cache",
			Compressed: []Compressed{
				{
					Code:   "gzip",
					Suffix: ".gz",
				},
				{
					Code:   "deflate",
					Suffix: ".zz",
				},
			},
		},
	}

	for _, tc := range []struct {
		Path       string
		ReqHeaders map[string]string
		RemoteAddr string
		Status     int
		ResHeaders map[string]string
		Body       string
		Compressed bool
		RealIP     string
		OtherLogs  []string
	}{
		{
			Path: "/static/icon/someicon.png",
			ReqHeaders: map[string]string{
				headerXForwardedFor: "172.16.0.2, 10.0.0.4, 10.0.0.3",
			},
			RemoteAddr: "172.16.0.3:1234",
			Status:     http.StatusOK,
			ResHeaders: map[string]string{
				headerCacheControl: "public, max-age=31536000, no-cache",
				headerContentType:  "image/png",
			},
			Body:   `this is a test image file`,
			RealIP: "172.16.0.3",
		},
		{
			Path: "/static/testfile.js",
			ReqHeaders: map[string]string{
				headerXForwardedFor: "172.16.0.2, 10.0.0.4, 10.0.0.3",
			},
			RemoteAddr: "10.0.0.2:1234",
			Status:     http.StatusOK,
			ResHeaders: map[string]string{
				headerCacheControl: "public, max-age=31536000, immutable",
				headerContentType:  "text/javascript; charset=utf-8",
			},
			Body:       `this is a test js file`,
			Compressed: true,
			RealIP:     "172.16.0.2",
		},
		{
			Path: "/manifest.json",
			ReqHeaders: map[string]string{
				headerXForwardedFor: "bogus, 10.0.0.4, 10.0.0.3",
			},
			RemoteAddr: "10.0.0.2:1234",
			Status:     http.StatusOK,
			ResHeaders: map[string]string{
				headerCacheControl: "public, max-age=31536000, no-cache",
				headerContentType:  "application/json",
			},
			Body:       `this is a test json file`,
			Compressed: true,
			RealIP:     "10.0.0.2",
		},
		{
			Path: "/someotherpath",
			ReqHeaders: map[string]string{
				headerXForwardedFor: "10.0.0.5, 10.0.0.4, 10.0.0.3",
			},
			RemoteAddr: "10.0.0.2:1234",
			Status:     http.StatusOK,
			ResHeaders: map[string]string{
				headerCacheControl: "public, max-age=31536000, no-cache",
				headerContentType:  "text/html; charset=utf-8",
			},
			Body:       `this is a test index html file`,
			Compressed: true,
			RealIP:     "10.0.0.5",
		},
		{
			Path:       "/index",
			RemoteAddr: "10.0.0.2:1234",
			Status:     http.StatusOK,
			ResHeaders: map[string]string{
				headerCacheControl: "public, max-age=31536000, no-cache",
				headerContentType:  "text/html; charset=utf-8",
			},
			Body:       `this is a test index html file`,
			Compressed: true,
			RealIP:     "10.0.0.2",
		},
		{
			Path:       "/static/fileunknownext",
			RemoteAddr: "10.0.0.2:1234",
			Status:     http.StatusOK,
			ResHeaders: map[string]string{
				headerCacheControl: "public, max-age=31536000, immutable",
				headerContentType:  "text/html; charset=utf-8",
			},
			Body:       `<!DOCTYPE HTML>`,
			Compressed: false,
			RealIP:     "10.0.0.2",
		},
		{
			Path:       "/static/bogus",
			RemoteAddr: "10.0.0.2:1234",
			Status:     http.StatusNotFound,
			RealIP:     "10.0.0.2",
			OtherLogs:  []string{"Failed to stat file"},
		},
		{
			Path:       "/bogus",
			RemoteAddr: "10.0.0.2:1234",
			Status:     http.StatusNotFound,
			RealIP:     "10.0.0.2",
			OtherLogs:  []string{"Failed to stat file"},
		},
		{
			Path:       "/subdir",
			RemoteAddr: "10.0.0.2:1234",
			Status:     http.StatusNotFound,
			RealIP:     "10.0.0.2",
			OtherLogs:  []string{"is a directory"},
		},
		{
			Path:       "/static/test.html",
			RemoteAddr: "10.0.0.2:1234",
			Status:     http.StatusOK,
			ResHeaders: map[string]string{
				headerCacheControl: "public, max-age=31536000, immutable",
				headerContentType:  "text/html; charset=utf-8",
			},
			Body:       `sample html file`,
			Compressed: false,
			RealIP:     "10.0.0.2",
		},
	} {
		tc := tc
		t.Run(tc.Path, func(t *testing.T) {
			t.Parallel()

			assert := require.New(t)

			logb := bytes.Buffer{}
			server := NewServer(klog.New(klog.OptSerializer(klog.NewJSONSerializer(klog.NewSyncWriter(&logb)))), fsys, Config{
				Instance: "testinstance",
				Proxies: []netip.Prefix{
					netip.MustParsePrefix("10.0.0.0/8"),
				},
			})
			assert.NoError(server.Mount(routes))

			jdec := json.NewDecoder(&logb)
			for _, i := range []mountLog{
				{
					Level:  klog.LevelInfo.String(),
					Msg:    "Handle route",
					Prefix: "/static/icon/",
					FSPath: "static/icon",
					Dir:    true,
				},
				{
					Level:  klog.LevelInfo.String(),
					Msg:    "Handle route",
					Prefix: "/static/",
					FSPath: "static",
					Dir:    true,
				},
				{
					Level:    klog.LevelInfo.String(),
					Msg:      "Compressed",
					Prefix:   "/static/",
					Encoding: "gzip",
					Test:     `\.(html|js|css|json)(\.map)?$`,
					Suffix:   ".gz",
				},
				{
					Level:    klog.LevelInfo.String(),
					Msg:      "Compressed",
					Prefix:   "/static/",
					Encoding: "deflate",
					Test:     `\.(html|js|css|json)(\.map)?$`,
					Suffix:   ".zz",
				},
				{
					Level:  klog.LevelInfo.String(),
					Msg:    "Handle route",
					Prefix: "/manifest.json",
					FSPath: "manifest.json",
				},
				{
					Level:    klog.LevelInfo.String(),
					Msg:      "Compressed",
					Prefix:   "/manifest.json",
					Encoding: "gzip",
					Suffix:   ".gz",
				},
				{
					Level:    klog.LevelInfo.String(),
					Msg:      "Compressed",
					Prefix:   "/manifest.json",
					Encoding: "deflate",
					Suffix:   ".zz",
				},
				{
					Level:  klog.LevelInfo.String(),
					Msg:    "Handle route",
					Prefix: "/bogus",
					FSPath: "bogus",
				},
				{
					Level:  klog.LevelInfo.String(),
					Msg:    "Handle route",
					Prefix: "/subdir",
					FSPath: "subdir",
				},
				{
					Level:  klog.LevelInfo.String(),
					Msg:    "Handle route",
					Prefix: "/",
					FSPath: "index.html",
				},
				{
					Level:    klog.LevelInfo.String(),
					Msg:      "Compressed",
					Prefix:   "/",
					Encoding: "gzip",
					Suffix:   ".gz",
				},
				{
					Level:    klog.LevelInfo.String(),
					Msg:      "Compressed",
					Prefix:   "/",
					Encoding: "deflate",
					Suffix:   ".zz",
				},
			} {
				var v mountLog
				assert.NoError(jdec.Decode(&v))
				assert.Equal(i, v)
			}
			assert.False(jdec.More())

			func() {
				var etag string
				{
					req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
					for k, v := range tc.ReqHeaders {
						req.Header.Set(k, v)
					}
					req.RemoteAddr = tc.RemoteAddr
					rec := httptest.NewRecorder()
					server.ServeHTTP(rec, req)

					checkHTTPLog(t, jdec, tc.Path, tc.RemoteAddr, tc.RealIP, tc.Status, tc.OtherLogs)

					assert.Equal(tc.Status, rec.Code)

					for k, v := range rec.HeaderMap {
						t.Log(k, v)
					}

					for k, v := range tc.ResHeaders {
						assert.Equal(v, rec.HeaderMap.Get(k))
					}

					if tc.Status != http.StatusOK {
						for _, i := range []string{
							headerCacheControl,
							headerContentEncoding,
							headerETag,
							headerVary,
						} {
							assert.Equal("", rec.HeaderMap.Get(i))
						}
						assert.Equal("text/plain; charset=utf-8", rec.HeaderMap.Get(headerContentType))
						return
					}

					assert.Equal(tc.Body, rec.Body.String())

					etag = rec.HeaderMap.Get(headerETag)
					assert.True(etag != "")
				}
				{
					req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
					for k, v := range tc.ReqHeaders {
						req.Header.Set(k, v)
					}
					req.RemoteAddr = tc.RemoteAddr
					req.Header.Set(headerIfNoneMatch, etag)
					rec := httptest.NewRecorder()
					server.ServeHTTP(rec, req)

					checkHTTPLog(t, jdec, tc.Path, tc.RemoteAddr, tc.RealIP, http.StatusNotModified, tc.OtherLogs)

					assert.Equal(http.StatusNotModified, rec.Code)
					for _, i := range []string{
						headerCacheControl,
						headerContentEncoding,
						headerContentType,
						headerETag,
						headerVary,
					} {
						assert.Equal("", rec.HeaderMap.Get(i))
					}
				}
			}()
			func() {
				var etag string
				{
					req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
					for k, v := range tc.ReqHeaders {
						req.Header.Set(k, v)
					}
					req.RemoteAddr = tc.RemoteAddr
					req.Header.Set(headerAcceptEncoding, "gzip")
					rec := httptest.NewRecorder()
					server.ServeHTTP(rec, req)

					checkHTTPLog(t, jdec, tc.Path, tc.RemoteAddr, tc.RealIP, tc.Status, tc.OtherLogs)

					assert.Equal(tc.Status, rec.Code)
					for k, v := range tc.ResHeaders {
						assert.Equal(v, rec.HeaderMap.Get(k))
					}

					if tc.Status != http.StatusOK {
						for _, i := range []string{
							headerCacheControl,
							headerContentEncoding,
							headerETag,
							headerVary,
						} {
							assert.Equal("", rec.HeaderMap.Get(i))
						}
						assert.Equal("text/plain; charset=utf-8", rec.HeaderMap.Get(headerContentType))
						return
					}

					if !tc.Compressed {
						assert.Equal("", rec.HeaderMap.Get(headerContentEncoding))
						assert.Equal(tc.Body, rec.Body.String())
						return
					}

					assert.Equal("gzip", rec.HeaderMap.Get(headerContentEncoding))
					gr, err := gzip.NewReader(rec.Body)
					assert.NoError(err)
					b := bytes.Buffer{}
					_, err = io.Copy(&b, gr)
					assert.NoError(err)
					assert.Equal(tc.Body, b.String())

					etag = rec.HeaderMap.Get(headerETag)
					assert.True(etag != "")
				}
				{
					req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
					for k, v := range tc.ReqHeaders {
						req.Header.Set(k, v)
					}
					req.RemoteAddr = tc.RemoteAddr
					req.Header.Set(headerAcceptEncoding, "gzip")
					req.Header.Set(headerIfNoneMatch, etag)
					rec := httptest.NewRecorder()
					server.ServeHTTP(rec, req)

					checkHTTPLog(t, jdec, tc.Path, tc.RemoteAddr, tc.RealIP, http.StatusNotModified, tc.OtherLogs)

					assert.Equal(http.StatusNotModified, rec.Code)
					for _, i := range []string{
						headerCacheControl,
						headerContentEncoding,
						headerContentType,
						headerETag,
						headerVary,
					} {
						assert.Equal("", rec.HeaderMap.Get(i))
					}
				}
			}()
			func() {
				var etag string
				{
					req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
					for k, v := range tc.ReqHeaders {
						req.Header.Set(k, v)
					}
					req.RemoteAddr = tc.RemoteAddr
					req.Header.Set(headerAcceptEncoding, "deflate")
					rec := httptest.NewRecorder()
					server.ServeHTTP(rec, req)

					checkHTTPLog(t, jdec, tc.Path, tc.RemoteAddr, tc.RealIP, tc.Status, tc.OtherLogs)

					assert.Equal(tc.Status, rec.Code)
					for k, v := range tc.ResHeaders {
						assert.Equal(v, rec.HeaderMap.Get(k))
					}

					if tc.Status != http.StatusOK {
						for _, i := range []string{
							headerCacheControl,
							headerContentEncoding,
							headerETag,
							headerVary,
						} {
							assert.Equal("", rec.HeaderMap.Get(i))
						}
						assert.Equal("text/plain; charset=utf-8", rec.HeaderMap.Get(headerContentType))
						return
					}

					if !tc.Compressed {
						assert.Equal("", rec.HeaderMap.Get(headerContentEncoding))
						assert.Equal(tc.Body, rec.Body.String())
						return
					}

					assert.Equal("", rec.HeaderMap.Get(headerContentEncoding))
					assert.Equal(tc.Body, rec.Body.String())

					etag = rec.HeaderMap.Get(headerETag)
					assert.True(etag != "")
				}
				{
					req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
					for k, v := range tc.ReqHeaders {
						req.Header.Set(k, v)
					}
					req.RemoteAddr = tc.RemoteAddr
					req.Header.Set(headerAcceptEncoding, "deflate")
					req.Header.Set(headerIfNoneMatch, etag)
					rec := httptest.NewRecorder()
					server.ServeHTTP(rec, req)

					checkHTTPLog(t, jdec, tc.Path, tc.RemoteAddr, tc.RealIP, http.StatusNotModified, tc.OtherLogs)

					assert.Equal(http.StatusNotModified, rec.Code)
					for _, i := range []string{
						headerCacheControl,
						headerContentEncoding,
						headerContentType,
						headerETag,
						headerVary,
					} {
						assert.Equal("", rec.HeaderMap.Get(i))
					}
				}
			}()
			assert.False(jdec.More())
		})
	}
}

func TestSnowflake(t *testing.T) {
	t.Parallel()

	assert := require.New(t)

	var prev string
	for i := 0; i < 2; i++ {
		u, err := NewSnowflake(1)
		assert.NoError(err)
		assert.NotEqual(prev, u)
		prev = u
	}
}
