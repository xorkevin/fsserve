package serve

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
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
)

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
		Status     int
		Body       string
		Headers    http.Header
		Compressed bool
	}{
		{
			Path:   "/static/icon/someicon.png",
			Status: http.StatusOK,
			Body:   `this is a test image file`,
		},
		{
			Path:       "/static/testfile.js",
			Status:     http.StatusOK,
			Body:       `this is a test js file`,
			Compressed: true,
		},
		{
			Path:       "/manifest.json",
			Status:     http.StatusOK,
			Body:       `this is a test json file`,
			Compressed: true,
		},
		{
			Path:       "/someotherpath",
			Status:     http.StatusOK,
			Body:       `this is a test index html file`,
			Compressed: true,
		},
	} {
		tc := tc
		t.Run(tc.Path, func(t *testing.T) {
			t.Parallel()

			assert := require.New(t)

			logb := bytes.Buffer{}
			server := NewServer(klog.New(klog.OptSerializer(klog.NewJSONSerializer(klog.NewSyncWriter(&logb)))), fsys, Config{
				Instance: "testinstance",
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
					rec := httptest.NewRecorder()
					server.ServeHTTP(rec, req)

					assert.Equal(tc.Status, rec.Code)
					for k, v := range tc.Headers {
						assert.Equal(v, rec.HeaderMap.Values(k))
					}

					if tc.Status != http.StatusOK {
						for _, i := range []string{
							headerCacheControl,
							headerContentEncoding,
							headerContentType,
							headerETag,
							headerVary,
						} {
							assert.Equal("", rec.HeaderMap.Get(i))
						}
						return
					}

					assert.Equal(tc.Body, rec.Body.String())

					etag = rec.HeaderMap.Get(headerETag)
					assert.True(etag != "")
				}
				{
					req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
					req.Header.Set("If-None-Match", etag)
					rec := httptest.NewRecorder()
					server.ServeHTTP(rec, req)

					assert.Equal(http.StatusNotModified, rec.Code)
				}
			}()
			func() {
				var etag string
				{
					req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
					req.Header.Set(headerAcceptEncoding, "gzip")
					rec := httptest.NewRecorder()
					server.ServeHTTP(rec, req)

					assert.Equal(tc.Status, rec.Code)
					for k, v := range tc.Headers {
						assert.Equal(v, rec.HeaderMap.Values(k))
					}

					if tc.Status != http.StatusOK {
						for _, i := range []string{
							headerCacheControl,
							headerContentEncoding,
							headerContentType,
							headerETag,
							headerVary,
						} {
							assert.Equal("", rec.HeaderMap.Get(i))
						}
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
					req.Header.Set(headerAcceptEncoding, "gzip")
					req.Header.Set("If-None-Match", etag)
					rec := httptest.NewRecorder()
					server.ServeHTTP(rec, req)

					assert.Equal(http.StatusNotModified, rec.Code)
				}
			}()
			func() {
				var etag string
				{
					req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
					req.Header.Set(headerAcceptEncoding, "deflate")
					rec := httptest.NewRecorder()
					server.ServeHTTP(rec, req)

					assert.Equal(tc.Status, rec.Code)
					for k, v := range tc.Headers {
						assert.Equal(v, rec.HeaderMap.Values(k))
					}

					if tc.Status != http.StatusOK {
						for _, i := range []string{
							headerCacheControl,
							headerContentEncoding,
							headerContentType,
							headerETag,
							headerVary,
						} {
							assert.Equal("", rec.HeaderMap.Get(i))
						}
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
					req.Header.Set(headerAcceptEncoding, "deflate")
					req.Header.Set("If-None-Match", etag)
					rec := httptest.NewRecorder()
					server.ServeHTTP(rec, req)

					assert.Equal(http.StatusNotModified, rec.Code)
				}
			}()
		})
	}
}
