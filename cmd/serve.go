package cmd

import (
	"context"
	"fmt"
	"io/fs"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"xorkevin.dev/fsserve/db"
	"xorkevin.dev/fsserve/serve"
	"xorkevin.dev/kerrors"
	"xorkevin.dev/klog"
)

type (
	serveFlags struct {
		port int
		base string
	}
)

func (c *Cmd) getServeCmd() *cobra.Command {
	serveCmd := &cobra.Command{
		Use:               "serve",
		Short:             "Serves a local file system with an http server",
		Long:              `Serves a local file system with an http server`,
		Run:               c.execServe,
		DisableAutoGenTag: true,
	}
	serveCmd.PersistentFlags().IntVarP(&c.serveFlags.port, "port", "p", 0, "port to run the http server on (default 8080)")
	serveCmd.PersistentFlags().StringVarP(&c.serveFlags.base, "base", "b", "", "static files base")
	viper.SetDefault("port", 8080)
	viper.SetDefault("base", "")
	viper.SetDefault("contentdir", "content")
	viper.SetDefault("dbengine", "fs")
	viper.SetDefault("treedb", "tree")
	viper.SetDefault("exttotype", []serve.MimeType{})
	viper.SetDefault("routes", []serve.Route{})
	viper.SetDefault("maxheadersize", "1M")
	viper.SetDefault("maxconnread", "5s")
	viper.SetDefault("maxconnheader", "2s")
	viper.SetDefault("maxconnwrite", "5s")
	viper.SetDefault("maxconnidle", "5s")
	viper.SetDefault("gracefulshutdown", "5s")
	return serveCmd
}

func (c *Cmd) execServe(cmd *cobra.Command, args []string) {
	var mimeTypes []serve.MimeType
	if err := viper.UnmarshalKey("exttotype", &mimeTypes); err != nil {
		c.logFatal(kerrors.WithMsg(err, "Failed to read config exttotype"))
		return
	}
	if err := serve.AddMimeTypes(mimeTypes); err != nil {
		c.logFatal(kerrors.WithMsg(err, "Failed to set ext to mime types"))
		return
	}
	c.log.Info(context.Background(), "Added ext mime types",
		klog.AAny("mimetypes", mimeTypes),
	)

	var routes []serve.Route
	if err := viper.UnmarshalKey("routes", &routes); err != nil {
		c.logFatal(kerrors.WithMsg(err, "Failed to read config routes"))
		return
	}

	instance, err := serve.NewSnowflake(8)
	if err != nil {
		c.logFatal(kerrors.WithMsg(err, "Failed to generate instance id"))
		return
	}

	proxystrs := viper.GetStringSlice("proxies")
	proxies := make([]netip.Prefix, 0, len(proxystrs))
	for _, i := range proxystrs {
		k, err := netip.ParsePrefix(i)
		if err != nil {
			c.logFatal(kerrors.WithMsg(err, "Invalid proxy CIDR"))
			return
		}
		proxies = append(proxies, k)
	}
	c.log.Info(context.Background(), "Trusted proxies",
		klog.AAny("realip.proxies", proxystrs),
	)

	base := c.serveFlags.base
	if base == "" {
		base = viper.GetString("base")
		if base == "" {
			base = "."
		}
	}

	contentdir := viper.GetString("contentdir")
	rootDir := os.DirFS(filepath.FromSlash(base))
	contentDir, err := fs.Sub(rootDir, contentdir)
	if err != nil {
		c.logFatal(kerrors.WithMsg(err, "Invalid content dir"))
		return
	}

	c.log.Info(context.Background(), "Serving directory at base",
		klog.AString("fs.base", base),
		klog.AString("fs.contentdir", path.Join(base, contentdir)),
	)

	var treedb serve.TreeDB
	switch viper.GetString("dbengine") {
	case "sqlite":
		{
			// url must be in the form of
			// file:rel/path/to/file.db?optquery=value&otheroptquery=value
			u, err := url.Parse(viper.GetString("treedb"))
			if err != nil {
				c.logFatal(kerrors.WithMsg(err, "Invalid tree db sqlite dsn"))
				return
			}
			if u.Opaque == "" {
				c.logFatal(kerrors.WithMsg(err, "Tree db sqlite dsn must be relative"))
				return
			}
			u.Opaque = path.Join(base, u.Opaque)
			q := u.Query()
			q.Set("mode", "ro")
			u.RawQuery = q.Encode()
			d := db.NewSQLClient(c.log.Logger.Sublogger("db"), u.String())
			treedb = serve.NewSQLiteTreeDB(d, "content")

			c.log.Info(context.Background(), "Using dbengine",
				klog.AString("db.engine", "sqlite"),
				klog.AString("db.file", u.Opaque),
			)
		}
	case "fs", "":
		{
			treedir := viper.GetString("treedb")
			treeDir, err := fs.Sub(rootDir, treedir)
			if err != nil {
				c.logFatal(kerrors.WithMsg(err, "Invalid tree dir"))
				return
			}
			treedb = serve.NewFSTreeDB(treeDir)

			c.log.Info(context.Background(), "Using dbengine",
				klog.AString("db.engine", "fs"),
				klog.AString("db.dir", treedir),
			)
		}
	default:
		c.logFatal(kerrors.WithMsg(err, "Invalid db engine"))
		return
	}

	s := serve.NewServer(
		c.log.Logger,
		treedb,
		contentDir,
		serve.Config{
			Instance: instance,
			Proxies:  proxies,
		},
	)
	if err := s.Mount(routes); err != nil {
		c.logFatal(kerrors.WithMsg(err, "Failed to mount server routes"))
		return
	}

	port := c.serveFlags.port
	if port == 0 {
		port = viper.GetInt("port")
		if port == 0 {
			port = 8080
		}
	}

	opts := serve.Opts{
		ReadTimeout:       c.readDurationConfig(viper.GetString("maxconnread"), seconds5),
		ReadHeaderTimeout: c.readDurationConfig(viper.GetString("maxconnheader"), seconds2),
		WriteTimeout:      c.readDurationConfig(viper.GetString("maxconnwrite"), seconds5),
		IdleTimeout:       c.readDurationConfig(viper.GetString("maxconnidle"), seconds5),
		MaxHeaderBytes:    c.readBytesConfig(viper.GetString("maxheadersize"), MEGABYTE),
		GracefulShutdown:  c.readDurationConfig(viper.GetString("gracefulshutdown"), seconds5),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		s.Serve(ctx, c.serveFlags.port, opts)
	}()

	waitForInterrupt(ctx)

	cancel()
	wg.Wait()
}

func waitForInterrupt(ctx context.Context) {
	notifyCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-notifyCtx.Done()
}

const (
	seconds5 = 5 * time.Second
	seconds2 = 2 * time.Second
)

func (c *Cmd) readDurationConfig(s string, d time.Duration) time.Duration {
	t, err := time.ParseDuration(s)
	if err != nil {
		c.log.WarnErr(context.Background(), kerrors.WithMsg(err, fmt.Sprintf("Invalid config time value: %s", s)))
		return d
	}
	return t
}

// Byte constants for every 2^(10*n) bytes
const (
	BYTE = 1 << (10 * iota)
	KILOBYTE
	MEGABYTE
	GIGABYTE
)

func (c *Cmd) readBytesConfig(s string, d int) int {
	b := strings.ToUpper(s)

	i := strings.IndexFunc(s, unicode.IsLetter)

	if i < 0 {
		c.log.WarnErr(context.Background(), kerrors.WithMsg(nil, fmt.Sprintf("Invalid config bytes value: %s", s)))
		return d
	}

	bytesString, multiple := b[:i], b[i:]
	bytes, err := strconv.Atoi(bytesString)
	if err != nil {
		c.log.WarnErr(context.Background(), kerrors.WithMsg(err, fmt.Sprintf("Invalid config bytes value: %s", s)))
		return d
	}
	if bytes < 0 {
		c.log.WarnErr(context.Background(), kerrors.WithMsg(nil, fmt.Sprintf("Invalid config bytes value: %s", s)))
		return d
	}

	switch multiple {
	case "G", "GB", "GIB":
		return bytes * GIGABYTE
	case "M", "MB", "MIB":
		return bytes * MEGABYTE
	case "K", "KB", "KIB":
		return bytes * KILOBYTE
	case "B":
		return bytes
	default:
		c.log.WarnErr(context.Background(), kerrors.WithMsg(nil, fmt.Sprintf("Invalid config bytes value: %s", s)))
		return d
	}
}
