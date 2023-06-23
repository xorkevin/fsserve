package serve

import (
	"bytes"
	"compress/gzip"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
	"xorkevin.dev/fsserve/db"
	"xorkevin.dev/kfs/kfstest"
	"xorkevin.dev/klog"
)

func TestServer(t *testing.T) {
	t.Parallel()

	assert := require.New(t)

	rootDir := filepath.ToSlash(t.TempDir())
	srcDir := path.Join(rootDir, "src")

	srcFiles := map[string]string{
		"static/icon/someicon.png": `this is a test image file`,
		"static/testfile.js":       `this is a test js file`,
		"static/fileunknownext":    `<!DOCTYPE HTML>`,
		"static/test.html":         `sample html file`,
		"subdir/textfile":          `sample text file`,
		"manifest.json":            `this is a test json file`,
		"index.html":               `this is a test index html file`,
	}
	{
		var filemode fs.FileMode = 0o644
		for k, v := range srcFiles {
			name := filepath.FromSlash(path.Join(srcDir, k))
			dir := filepath.Dir(name)
			assert.NoError(os.MkdirAll(dir, 0o777))
			assert.NoError(os.WriteFile(name, []byte(v), filemode))
		}
		gw := gzip.NewWriter(nil)
		for _, i := range []string{
			"static/testfile.js",
			"manifest.json",
			"index.html",
		} {
			var b bytes.Buffer
			gw.Reset(&b)
			_, err := gw.Write([]byte(srcFiles[i]))
			assert.NoError(err)
			assert.NoError(gw.Close())
			assert.NoError(os.WriteFile(filepath.FromSlash(path.Join(srcDir, i)+".gz"), b.Bytes(), filemode))
		}
	}

	baseDir := path.Join(rootDir, "base")
	treeDBFile := path.Join(baseDir, "tree.db")
	assert.NoError(os.MkdirAll(filepath.Dir(filepath.FromSlash(treeDBFile)), 0o777))
	rwDB := db.NewSQLClient(klog.Discard{}, "file:"+filepath.FromSlash(treeDBFile)+"?mode=rwc")
	assert.NoError(rwDB.Init())

	contentDir := &kfstest.MapFS{
		Fsys: fstest.MapFS{},
	}
	tree := NewTree(
		klog.Discard{},
		NewSQLiteTreeDB(
			rwDB,
			"content",
			"encoded",
		),
		contentDir,
	)

	assert.NoError(tree.Setup(context.Background()))

	tree.Add(context.Background(), "static/testfile.js", "test/javascript", filepath.FromSlash(path.Join(srcDir, "static/testfile.js")), []EncodedFile{
		{Code: "gzip", Name: filepath.FromSlash(path.Join(srcDir, "static/testfile.js.gz"))},
	})

	rdb := db.NewSQLClient(klog.Discard{}, "file:"+filepath.FromSlash(treeDBFile)+"?mode=ro")
	assert.NoError(rdb.Init())
	server := NewServer(klog.Discard{},
		NewSQLiteTreeDB(
			rdb,
			"content",
			"encoded",
		),
		contentDir,
		Config{
			Instance: "testinstance",
			Proxies: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/8"),
			},
		},
	)
	assert.NoError(
		server.Mount([]Route{
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
			},
			{
				Prefix:       "/manifest.json",
				Path:         "manifest.json",
				CacheControl: "public, max-age=31536000, no-cache",
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
			},
		}),
	)

	{
		req := httptest.NewRequest(http.MethodGet, "/static/testfile.js", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		assert.Equal(http.StatusOK, rec.Code)
		assert.Equal(srcFiles["static/testfile.js"], rec.Body.String())
	}

	// for _, tc := range []struct {
	// 	Path       string
	// 	ReqHeaders map[string]string
	// 	RemoteAddr string
	// 	Status     int
	// 	ResHeaders map[string]string
	// 	Body       string
	// 	Compressed bool
	// 	RealIP     string
	// 	OtherLogs  []string
	// }{
	// 	{
	// 		Path: "/static/icon/someicon.png",
	// 		ReqHeaders: map[string]string{
	// 			headerXForwardedFor: "172.16.0.2, 10.0.0.4, 10.0.0.3",
	// 		},
	// 		RemoteAddr: "172.16.0.3:1234",
	// 		Status:     http.StatusOK,
	// 		ResHeaders: map[string]string{
	// 			headerCacheControl: "public, max-age=31536000, no-cache",
	// 			headerContentType:  "image/png",
	// 		},
	// 		Body:   `this is a test image file`,
	// 		RealIP: "172.16.0.3",
	// 	},
	// 	{
	// 		Path: "/static/testfile.js",
	// 		ReqHeaders: map[string]string{
	// 			headerXForwardedFor: "172.16.0.2, 10.0.0.4, 10.0.0.3",
	// 		},
	// 		RemoteAddr: "10.0.0.2:1234",
	// 		Status:     http.StatusOK,
	// 		ResHeaders: map[string]string{
	// 			headerCacheControl: "public, max-age=31536000, immutable",
	// 			headerContentType:  "text/javascript; charset=utf-8",
	// 		},
	// 		Body:       `this is a test js file`,
	// 		Compressed: true,
	// 		RealIP:     "172.16.0.2",
	// 	},
	// 	{
	// 		Path: "/manifest.json",
	// 		ReqHeaders: map[string]string{
	// 			headerXForwardedFor: "bogus, 10.0.0.4, 10.0.0.3",
	// 		},
	// 		RemoteAddr: "10.0.0.2:1234",
	// 		Status:     http.StatusOK,
	// 		ResHeaders: map[string]string{
	// 			headerCacheControl: "public, max-age=31536000, no-cache",
	// 			headerContentType:  "application/json",
	// 		},
	// 		Body:       `this is a test json file`,
	// 		Compressed: true,
	// 		RealIP:     "10.0.0.2",
	// 	},
	// 	{
	// 		Path: "/someotherpath",
	// 		ReqHeaders: map[string]string{
	// 			headerXForwardedFor: "10.0.0.5, 10.0.0.4, 10.0.0.3",
	// 		},
	// 		RemoteAddr: "10.0.0.2:1234",
	// 		Status:     http.StatusOK,
	// 		ResHeaders: map[string]string{
	// 			headerCacheControl: "public, max-age=31536000, no-cache",
	// 			headerContentType:  "text/html; charset=utf-8",
	// 		},
	// 		Body:       `this is a test index html file`,
	// 		Compressed: true,
	// 		RealIP:     "10.0.0.5",
	// 	},
	// 	{
	// 		Path:       "/index",
	// 		RemoteAddr: "10.0.0.2:1234",
	// 		Status:     http.StatusOK,
	// 		ResHeaders: map[string]string{
	// 			headerCacheControl: "public, max-age=31536000, no-cache",
	// 			headerContentType:  "text/html; charset=utf-8",
	// 		},
	// 		Body:       `this is a test index html file`,
	// 		Compressed: true,
	// 		RealIP:     "10.0.0.2",
	// 	},
	// 	{
	// 		Path:       "/static/fileunknownext",
	// 		RemoteAddr: "10.0.0.2:1234",
	// 		Status:     http.StatusOK,
	// 		ResHeaders: map[string]string{
	// 			headerCacheControl: "public, max-age=31536000, immutable",
	// 			headerContentType:  "text/html; charset=utf-8",
	// 		},
	// 		Body:       `<!DOCTYPE HTML>`,
	// 		Compressed: false,
	// 		RealIP:     "10.0.0.2",
	// 	},
	// 	{
	// 		Path:       "/static/bogus",
	// 		RemoteAddr: "10.0.0.2:1234",
	// 		Status:     http.StatusNotFound,
	// 		RealIP:     "10.0.0.2",
	// 		OtherLogs:  []string{"Failed to stat file"},
	// 	},
	// 	{
	// 		Path:       "/bogus",
	// 		RemoteAddr: "10.0.0.2:1234",
	// 		Status:     http.StatusNotFound,
	// 		RealIP:     "10.0.0.2",
	// 		OtherLogs:  []string{"Failed to stat file"},
	// 	},
	// 	{
	// 		Path:       "/subdir",
	// 		RemoteAddr: "10.0.0.2:1234",
	// 		Status:     http.StatusNotFound,
	// 		RealIP:     "10.0.0.2",
	// 		OtherLogs:  []string{"is a directory"},
	// 	},
	// 	{
	// 		Path:       "/static/test.html",
	// 		RemoteAddr: "10.0.0.2:1234",
	// 		Status:     http.StatusOK,
	// 		ResHeaders: map[string]string{
	// 			headerCacheControl: "public, max-age=31536000, immutable",
	// 			headerContentType:  "text/html; charset=utf-8",
	// 		},
	// 		Body:       `sample html file`,
	// 		Compressed: false,
	// 		RealIP:     "10.0.0.2",
	// 	},
	// } {
	// 	tc := tc
	// 	t.Run(tc.Path, func(t *testing.T) {
	// 		t.Parallel()

	// 		assert := require.New(t)

	// 		var logb bytes.Buffer
	// 		server := NewServer(klog.New(klog.OptHandler(klog.NewJSONSlogHandler(klog.NewSyncWriter(&logb)))), fsys, Config{
	// 			Instance: "testinstance",
	// 			Proxies: []netip.Prefix{
	// 				netip.MustParsePrefix("10.0.0.0/8"),
	// 			},
	// 		})
	// 		assert.NoError(server.Mount(routes))

	// 		jdec := json.NewDecoder(&logb)
	// 		for _, i := range []mountLog{
	// 			{
	// 				Level:  klog.LevelInfo.String(),
	// 				Msg:    "Handle route",
	// 				Prefix: "/static/icon/",
	// 				FSPath: "static/icon",
	// 				Dir:    true,
	// 			},
	// 			{
	// 				Level:  klog.LevelInfo.String(),
	// 				Msg:    "Handle route",
	// 				Prefix: "/static/",
	// 				FSPath: "static",
	// 				Dir:    true,
	// 			},
	// 			{
	// 				Level:    klog.LevelInfo.String(),
	// 				Msg:      "Compressed",
	// 				Prefix:   "/static/",
	// 				Encoding: "gzip",
	// 				Test:     `\.(html|js|css|json)(\.map)?$`,
	// 				Suffix:   ".gz",
	// 			},
	// 			{
	// 				Level:    klog.LevelInfo.String(),
	// 				Msg:      "Compressed",
	// 				Prefix:   "/static/",
	// 				Encoding: "deflate",
	// 				Test:     `\.(html|js|css|json)(\.map)?$`,
	// 				Suffix:   ".zz",
	// 			},
	// 			{
	// 				Level:  klog.LevelInfo.String(),
	// 				Msg:    "Handle route",
	// 				Prefix: "/manifest.json",
	// 				FSPath: "manifest.json",
	// 			},
	// 			{
	// 				Level:    klog.LevelInfo.String(),
	// 				Msg:      "Compressed",
	// 				Prefix:   "/manifest.json",
	// 				Encoding: "gzip",
	// 				Suffix:   ".gz",
	// 			},
	// 			{
	// 				Level:    klog.LevelInfo.String(),
	// 				Msg:      "Compressed",
	// 				Prefix:   "/manifest.json",
	// 				Encoding: "deflate",
	// 				Suffix:   ".zz",
	// 			},
	// 			{
	// 				Level:  klog.LevelInfo.String(),
	// 				Msg:    "Handle route",
	// 				Prefix: "/bogus",
	// 				FSPath: "bogus",
	// 			},
	// 			{
	// 				Level:  klog.LevelInfo.String(),
	// 				Msg:    "Handle route",
	// 				Prefix: "/subdir",
	// 				FSPath: "subdir",
	// 			},
	// 			{
	// 				Level:  klog.LevelInfo.String(),
	// 				Msg:    "Handle route",
	// 				Prefix: "/",
	// 				FSPath: "index.html",
	// 			},
	// 			{
	// 				Level:    klog.LevelInfo.String(),
	// 				Msg:      "Compressed",
	// 				Prefix:   "/",
	// 				Encoding: "gzip",
	// 				Suffix:   ".gz",
	// 			},
	// 			{
	// 				Level:    klog.LevelInfo.String(),
	// 				Msg:      "Compressed",
	// 				Prefix:   "/",
	// 				Encoding: "deflate",
	// 				Suffix:   ".zz",
	// 			},
	// 		} {
	// 			var v mountLog
	// 			assert.NoError(jdec.Decode(&v))
	// 			assert.Equal(i, v)
	// 		}
	// 		assert.False(jdec.More())

	// 		func() {
	// 			var etag string
	// 			{
	// 				req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
	// 				for k, v := range tc.ReqHeaders {
	// 					req.Header.Set(k, v)
	// 				}
	// 				req.RemoteAddr = tc.RemoteAddr
	// 				rec := httptest.NewRecorder()
	// 				server.ServeHTTP(rec, req)

	// 				assert.Equal(tc.Status, rec.Code)

	// 				for k, v := range tc.ResHeaders {
	// 					assert.Equal(v, rec.Result().Header.Get(k))
	// 				}

	// 				if tc.Status != http.StatusOK {
	// 					for _, i := range []string{
	// 						headerCacheControl,
	// 						headerContentEncoding,
	// 						headerETag,
	// 					} {
	// 						assert.Equal("", rec.Result().Header.Get(i))
	// 					}
	// 					assert.Equal("text/plain; charset=utf-8", rec.Result().Header.Get(headerContentType))
	// 					return
	// 				}

	// 				assert.Equal(tc.Body, rec.Body.String())

	// 				etag = rec.Result().Header.Get(headerETag)
	// 				assert.True(etag != "")
	// 			}
	// 			{
	// 				req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
	// 				for k, v := range tc.ReqHeaders {
	// 					req.Header.Set(k, v)
	// 				}
	// 				req.RemoteAddr = tc.RemoteAddr
	// 				req.Header.Set(headerIfNoneMatch, etag)
	// 				rec := httptest.NewRecorder()
	// 				server.ServeHTTP(rec, req)

	// 				assert.Equal(http.StatusNotModified, rec.Code)
	// 				for _, i := range []string{
	// 					headerContentEncoding,
	// 					headerContentType,
	// 				} {
	// 					assert.Equal("", rec.Result().Header.Get(i))
	// 				}
	// 			}
	// 		}()
	// 		func() {
	// 			var etag string
	// 			{
	// 				req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
	// 				for k, v := range tc.ReqHeaders {
	// 					req.Header.Set(k, v)
	// 				}
	// 				req.RemoteAddr = tc.RemoteAddr
	// 				req.Header.Set(headerAcceptEncoding, "gzip")
	// 				rec := httptest.NewRecorder()
	// 				server.ServeHTTP(rec, req)

	// 				assert.Equal(tc.Status, rec.Code)
	// 				for k, v := range tc.ResHeaders {
	// 					assert.Equal(v, rec.Result().Header.Get(k))
	// 				}

	// 				if tc.Status != http.StatusOK {
	// 					for _, i := range []string{
	// 						headerCacheControl,
	// 						headerContentEncoding,
	// 						headerETag,
	// 					} {
	// 						assert.Equal("", rec.Result().Header.Get(i))
	// 					}
	// 					assert.Equal("text/plain; charset=utf-8", rec.Result().Header.Get(headerContentType))
	// 					return
	// 				}

	// 				if !tc.Compressed {
	// 					assert.Equal("", rec.Result().Header.Get(headerContentEncoding))
	// 					assert.Equal(tc.Body, rec.Body.String())
	// 					return
	// 				}

	// 				assert.Equal("gzip", rec.Result().Header.Get(headerContentEncoding))
	// 				gr, err := gzip.NewReader(rec.Body)
	// 				assert.NoError(err)
	// 				var b bytes.Buffer
	// 				_, err = io.Copy(&b, gr)
	// 				assert.NoError(err)
	// 				assert.Equal(tc.Body, b.String())

	// 				etag = rec.Result().Header.Get(headerETag)
	// 				assert.True(etag != "")
	// 			}
	// 			{
	// 				req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
	// 				for k, v := range tc.ReqHeaders {
	// 					req.Header.Set(k, v)
	// 				}
	// 				req.RemoteAddr = tc.RemoteAddr
	// 				req.Header.Set(headerAcceptEncoding, "gzip")
	// 				req.Header.Set(headerIfNoneMatch, etag)
	// 				rec := httptest.NewRecorder()
	// 				server.ServeHTTP(rec, req)

	// 				assert.Equal(http.StatusNotModified, rec.Code)
	// 				for _, i := range []string{
	// 					headerContentEncoding,
	// 					headerContentType,
	// 				} {
	// 					assert.Equal("", rec.Result().Header.Get(i))
	// 				}
	// 			}
	// 		}()
	// 		func() {
	// 			var etag string
	// 			{
	// 				req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
	// 				for k, v := range tc.ReqHeaders {
	// 					req.Header.Set(k, v)
	// 				}
	// 				req.RemoteAddr = tc.RemoteAddr
	// 				req.Header.Set(headerAcceptEncoding, "deflate")
	// 				rec := httptest.NewRecorder()
	// 				server.ServeHTTP(rec, req)

	// 				assert.Equal(tc.Status, rec.Code)
	// 				for k, v := range tc.ResHeaders {
	// 					assert.Equal(v, rec.Result().Header.Get(k))
	// 				}

	// 				if tc.Status != http.StatusOK {
	// 					for _, i := range []string{
	// 						headerCacheControl,
	// 						headerContentEncoding,
	// 						headerETag,
	// 					} {
	// 						assert.Equal("", rec.Result().Header.Get(i))
	// 					}
	// 					assert.Equal("text/plain; charset=utf-8", rec.Result().Header.Get(headerContentType))
	// 					return
	// 				}

	// 				if !tc.Compressed {
	// 					assert.Equal("", rec.Result().Header.Get(headerContentEncoding))
	// 					assert.Equal(tc.Body, rec.Body.String())
	// 					return
	// 				}

	// 				assert.Equal("", rec.Result().Header.Get(headerContentEncoding))
	// 				assert.Equal(tc.Body, rec.Body.String())

	// 				etag = rec.Result().Header.Get(headerETag)
	// 				assert.True(etag != "")
	// 			}
	// 			{
	// 				req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
	// 				for k, v := range tc.ReqHeaders {
	// 					req.Header.Set(k, v)
	// 				}
	// 				req.RemoteAddr = tc.RemoteAddr
	// 				req.Header.Set(headerAcceptEncoding, "deflate")
	// 				req.Header.Set(headerIfNoneMatch, etag)
	// 				rec := httptest.NewRecorder()
	// 				server.ServeHTTP(rec, req)

	// 				assert.Equal(http.StatusNotModified, rec.Code)
	// 				for _, i := range []string{
	// 					headerContentEncoding,
	// 					headerContentType,
	// 				} {
	// 					assert.Equal("", rec.Result().Header.Get(i))
	// 				}
	// 			}
	// 		}()
	// 		assert.False(jdec.More())
	// 	})
	// }

	// t.Run("prevents disallowed methods", func(t *testing.T) {
	// 	t.Parallel()

	// 	assert := require.New(t)

	// 	var logb bytes.Buffer
	// 	server := NewServer(klog.New(klog.OptHandler(klog.NewJSONSlogHandler(klog.NewSyncWriter(&logb)))), fsys, Config{
	// 		Instance: "testinstance",
	// 	})

	// 	for _, i := range []string{
	// 		http.MethodGet,
	// 		http.MethodHead,
	// 	} {
	// 		req := httptest.NewRequest(i, "/", nil)
	// 		rec := httptest.NewRecorder()
	// 		server.ServeHTTP(rec, req)
	// 		assert.Equal(http.StatusNotFound, rec.Code)
	// 	}

	// 	for _, i := range []string{
	// 		http.MethodPost,
	// 		http.MethodPut,
	// 		http.MethodPatch,
	// 		http.MethodDelete,
	// 		http.MethodConnect,
	// 		http.MethodOptions,
	// 		http.MethodTrace,
	// 	} {
	// 		req := httptest.NewRequest(i, "/", nil)
	// 		rec := httptest.NewRecorder()
	// 		server.ServeHTTP(rec, req)
	// 		assert.Equal(http.StatusMethodNotAllowed, rec.Code)
	// 	}
	// })
}

func TestAddMimeTypes(t *testing.T) {
	t.Parallel()

	assert := require.New(t)

	assert.NoError(AddMimeTypes([]MimeType{
		{
			Ext:         ".mp3",
			ContentType: "audio/mpeg",
		},
		{
			Ext:         ".mp4",
			ContentType: "video/mp4",
		},
		{
			Ext:         ".woff",
			ContentType: "font/woff",
		},
		{
			Ext:         ".woff2",
			ContentType: "font/woff2",
		},
	}))
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
