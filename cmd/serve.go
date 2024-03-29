package cmd

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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

	instance, err := serve.NewRandSnowflake()
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

	contentDir := c.getBaseFS()

	s := serve.NewServer(
		c.log.Logger,
		contentDir,
		serve.Config{
			Instance: instance.Base64(),
			Proxies:  proxies,
		},
	)
	if err := s.Mount(routes); err != nil {
		c.logFatal(kerrors.WithMsg(err, "Failed to mount server routes"))
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
		s.Serve(ctx, port, opts)
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
