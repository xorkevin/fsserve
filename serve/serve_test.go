package serve

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
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
		"static/hideme":            `should be hidden`,
		"static/a":                 `also should be hidden`,
		"manifest.json":            `this is a test json file`,
		"index.html":               `this is a test index html file`,
		"subdir/file.txt":          `placeholder file`,
	}
	srcGzipFiles := []string{
		"static/testfile.js",
		"static/test.html",
		"manifest.json",
		"index.html",
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
		for _, i := range srcGzipFiles {
			var b bytes.Buffer
			gw.Reset(&b)
			_, err := gw.Write([]byte(srcFiles[i]))
			assert.NoError(err)
			assert.NoError(gw.Close())
			assert.NoError(os.WriteFile(filepath.FromSlash(path.Join(srcDir, i)+".gz"), b.Bytes(), filemode))
		}
	}

	server := NewServer(
		klog.Discard{},
		os.DirFS(filepath.FromSlash(srcDir)),
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
				Prefix:             "/static/",
				Dir:                true,
				Path:               "static",
				Include:            `.{2,}`,
				Exclude:            `^hideme$`,
				Encodings:          []Encoding{{Code: "gzip", Match: `\.js$`, Ext: ".gz"}},
				DefaultContentType: "text/plain",
				CacheControl:       "public, max-age=31536000, immutable",
			},
			{
				Prefix:       "/manifest.json",
				Path:         "manifest.json",
				Encodings:    []Encoding{{Code: "gzip", Ext: ".gz"}},
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
				Encodings:    []Encoding{{Code: "gzip", Ext: ".gz"}},
				CacheControl: "public, max-age=31536000, no-cache",
			},
		}),
	)

	for _, tc := range []struct {
		Name       string
		Path       string
		ReqHeaders map[string]string
		Status     int
		ResHeaders map[string]string
		Body       string
		Compressed bool
	}{
		{
			Name: "file without compressed encoding",
			Path: "/static/icon/someicon.png",
			ReqHeaders: map[string]string{
				headerAcceptEncoding: "gzip",
			},
			Status: http.StatusOK,
			ResHeaders: map[string]string{
				headerCacheControl: "public, max-age=31536000, no-cache",
				headerContentType:  "image/png",
			},
			Body: `this is a test image file`,
		},
		{
			Name: "file with compressed encoding",
			Path: "/static/testfile.js",
			ReqHeaders: map[string]string{
				headerAcceptEncoding: "gzip",
			},
			Status: http.StatusOK,
			ResHeaders: map[string]string{
				headerCacheControl: "public, max-age=31536000, immutable",
				headerContentType:  "text/javascript; charset=utf-8",
			},
			Body:       `this is a test js file`,
			Compressed: true,
		},
		{
			Name: "file with compressed encoding but no match",
			Path: "/static/test.html",
			ReqHeaders: map[string]string{
				headerAcceptEncoding: "gzip",
			},
			Status: http.StatusOK,
			ResHeaders: map[string]string{
				headerCacheControl: "public, max-age=31536000, immutable",
				headerContentType:  "text/html; charset=utf-8",
			},
			Body: `sample html file`,
		},
		{
			Name: "file with exact routing rule match",
			Path: "/manifest.json",
			ReqHeaders: map[string]string{
				headerAcceptEncoding: "gzip",
			},
			Status: http.StatusOK,
			ResHeaders: map[string]string{
				headerCacheControl: "public, max-age=31536000, no-cache",
				headerContentType:  "application/json",
			},
			Body:       `this is a test json file`,
			Compressed: true,
		},
		{
			Name: "fall back on index",
			Path: "/someotherpath",
			ReqHeaders: map[string]string{
				headerAcceptEncoding: "gzip",
			},
			Status: http.StatusOK,
			ResHeaders: map[string]string{
				headerCacheControl: "public, max-age=31536000, no-cache",
				headerContentType:  "text/html; charset=utf-8",
			},
			Body:       `this is a test index html file`,
			Compressed: true,
		},
		{
			Name:   "uses fallback content type",
			Path:   "/static/fileunknownext",
			Status: http.StatusOK,
			ResHeaders: map[string]string{
				headerCacheControl: "public, max-age=31536000, immutable",
				headerContentType:  "text/plain",
			},
			Body: `<!DOCTYPE HTML>`,
		},
		{
			Name:   "missing file in directory route",
			Path:   "/static/bogus",
			Status: http.StatusNotFound,
		},
		{
			Name:   "missing exact file",
			Path:   "/bogus",
			Status: http.StatusNotFound,
		},
		{
			Name:   "file excluded",
			Path:   "/static/hideme",
			Status: http.StatusNotFound,
		},
		{
			Name:   "file not included",
			Path:   "/static/a",
			Status: http.StatusNotFound,
		},
		{
			Name:   "cannot serve directory",
			Path:   "/subdir",
			Status: http.StatusBadRequest,
		},
	} {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			assert := require.New(t)

			req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
			for k, v := range tc.ReqHeaders {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)

			assert.Equal(tc.Status, rec.Code)

			for k, v := range tc.ResHeaders {
				assert.Equal(v, rec.Result().Header.Get(k))
			}

			if tc.Status != http.StatusOK {
				for _, i := range []string{
					headerCacheControl,
					headerContentEncoding,
					headerETag,
					headerVary,
				} {
					assert.Equal("", rec.Result().Header.Get(i))
				}
				return
			}

			if tc.Compressed {
				assert.Equal("gzip", rec.Result().Header.Get(headerContentEncoding))
				gr, err := gzip.NewReader(rec.Body)
				assert.NoError(err)
				var b bytes.Buffer
				_, err = io.Copy(&b, gr)
				assert.NoError(err)
				assert.Equal(tc.Body, b.String())
			} else {
				assert.Equal(tc.Body, rec.Body.String())
			}

			etag := rec.Result().Header.Get(headerETag)
			assert.True(etag != "")
			{
				req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
				for k, v := range tc.ReqHeaders {
					req.Header.Set(k, v)
				}
				req.Header.Set(headerIfNoneMatch, etag)
				rec := httptest.NewRecorder()
				server.ServeHTTP(rec, req)

				assert.Equal(http.StatusNotModified, rec.Code)
				for _, i := range []string{
					headerContentEncoding,
					headerContentType,
				} {
					assert.Equal("", rec.Result().Header.Get(i))
				}
			}
		})
	}

	t.Run("prevents disallowed methods", func(t *testing.T) {
		t.Parallel()

		assert := require.New(t)

		for _, i := range []string{
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
			http.MethodConnect,
			http.MethodOptions,
			http.MethodTrace,
		} {
			req := httptest.NewRequest(i, "/", nil)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			assert.Equal(http.StatusMethodNotAllowed, rec.Code)
		}
	})
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
