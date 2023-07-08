package serve

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
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
		"manifest.json":            `this is a test json file`,
		"index.html":               `this is a test index html file`,
	}
	filesToRm := map[string]string{
		"static/iwillbegone.txt": `I will be gone`,
		"static/another.txt":     `This will be gone`,
	}
	srcGzipFiles := []string{
		"static/testfile.js",
		"static/test.html",
		"manifest.json",
		"index.html",
	}
	gzipFilesToRm := []string{
		"static/iwillbegone.txt",
	}
	{
		var filemode fs.FileMode = 0o644
		for k, v := range srcFiles {
			name := filepath.FromSlash(path.Join(srcDir, k))
			dir := filepath.Dir(name)
			assert.NoError(os.MkdirAll(dir, 0o777))
			assert.NoError(os.WriteFile(name, []byte(v), filemode))
		}
		for k, v := range filesToRm {
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
		for _, i := range gzipFilesToRm {
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
	treeDBDir := path.Join(baseDir, "tree")
	assert.NoError(os.MkdirAll(filepath.Dir(filepath.FromSlash(treeDBFile)), 0o777))
	assert.NoError(os.MkdirAll(filepath.FromSlash(treeDBDir), 0o777))
	rwDB := db.NewSQLClient(klog.Discard{}, "file:"+filepath.FromSlash(treeDBFile)+"?mode=rwc")
	assert.NoError(rwDB.Init())
	rdb := db.NewSQLClient(klog.Discard{}, "file:"+filepath.FromSlash(treeDBFile)+"?mode=ro")
	assert.NoError(rdb.Init())

	for _, tc := range []struct {
		Name string
		RWDB TreeDB
		RDB  TreeDB
	}{
		{
			Name: "sqlite",
			RWDB: NewSQLiteTreeDB(
				rwDB,
				"content",
				"encoded",
			),
			RDB: NewSQLiteTreeDB(
				rdb,
				"content",
				"encoded",
			),
		},
	} {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			assert := require.New(t)

			contentDir := &kfstest.MapFS{
				Fsys: fstest.MapFS{
					"ishouldbegone": &fstest.MapFile{
						Data: []byte("hello world"),
					},
				},
			}

			tree := NewTree(
				klog.Discard{},
				tc.RWDB,
				contentDir,
			)

			assert.NoError(tree.Setup(context.Background()))

			assert.NoError(tree.Add(context.Background(), "static/testfile.js", "", filepath.FromSlash(path.Join(srcDir, "static/testfile.js")), []EncodedFile{
				{Code: "gzip", Name: filepath.FromSlash(path.Join(srcDir, "static/testfile.js.gz"))},
			}))
			assert.NoError(tree.Add(context.Background(), "static/iwillbegone.txt", "", filepath.FromSlash(path.Join(srcDir, "static/iwillbegone.txt")), []EncodedFile{
				{Code: "gzip", Name: filepath.FromSlash(path.Join(srcDir, "static/iwillbegone.txt.gz"))},
			}))
			assert.NoError(tree.Add(context.Background(), "static/another.txt", "", filepath.FromSlash(path.Join(srcDir, "static/another.txt")), nil))
			assert.NoError(tree.Rm(context.Background(), "static/another.txt"))

			server := NewServer(klog.Discard{},
				tc.RDB,
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

			assert.NoError(tree.SyncContent(context.Background(), SyncConfig{
				Dirs: []SyncDirConfig{
					{
						Dst:   "static",
						Src:   path.Join(srcDir, "static"),
						Match: `\.(?:html|js|png)$`,
						Alts: []EncodedAlts{
							{
								Code:   "gzip",
								Suffix: ".gz",
							},
						},
					},
					{
						Dst:   "static/fileunknownext",
						Exact: true,
						Src:   path.Join(srcDir, "static/fileunknownext"),
					},
					{
						Dst:   "manifest.json",
						Exact: true,
						Src:   path.Join(srcDir, "manifest.json"),
						Alts: []EncodedAlts{
							{
								Code: "gzip",
								Name: path.Join(srcDir, "manifest.json.gz"),
							},
						},
					},
					{
						Dst:   "index.html",
						Exact: true,
						Src:   path.Join(srcDir, "index.html"),
						Alts: []EncodedAlts{
							{
								Code: "gzip",
								Name: path.Join(srcDir, "index.html.gz"),
							},
						},
					},
				},
			}, true))
			assert.Equal(len(srcFiles)+len(srcGzipFiles), len(contentDir.Fsys))

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
						headerContentType:  "application/octet-stream",
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
					Name:   "cannot serve directory",
					Path:   "/subdir",
					Status: http.StatusNotFound,
				},
			} {
				tc := tc
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
		})
	}
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
