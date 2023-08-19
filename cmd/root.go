package cmd

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"xorkevin.dev/fsserve/db"
	"xorkevin.dev/fsserve/serve"
	"xorkevin.dev/kerrors"
	"xorkevin.dev/kfs"
	"xorkevin.dev/klog"
)

type (
	Cmd struct {
		rootCmd    *cobra.Command
		log        *klog.LevelLogger
		version    string
		rootFlags  rootFlags
		serveFlags serveFlags
		treeFlags  treeFlags
		docFlags   docFlags
	}

	rootFlags struct {
		cfgFile  string
		logLevel string
		logPlain bool
	}
)

func New() *Cmd {
	return &Cmd{}
}

func (c *Cmd) Execute() {
	buildinfo := ReadVCSBuildInfo()
	c.version = buildinfo.ModVersion
	rootCmd := &cobra.Command{
		Use:               "fsserve",
		Short:             "A file system http server",
		Long:              `A file system http server`,
		Version:           c.version,
		PersistentPreRun:  c.initConfig,
		DisableAutoGenTag: true,
	}
	rootCmd.PersistentFlags().StringVar(&c.rootFlags.cfgFile, "config", "", "config file (default is .fsserve.json)")
	rootCmd.PersistentFlags().StringVar(&c.rootFlags.logLevel, "log-level", "info", "log level")
	rootCmd.PersistentFlags().BoolVar(&c.rootFlags.logPlain, "log-plain", false, "output plain text logs")

	rootCmd.PersistentFlags().StringVarP(&c.serveFlags.base, "base", "b", "", "static files base")

	viper.SetDefault("port", 8080)
	viper.SetDefault("base", "")
	viper.SetDefault("contentdir", "content")
	viper.SetDefault("treedb", "tree.db")
	viper.SetDefault("sync", serve.SyncConfig{})
	viper.SetDefault("exttotype", []serve.MimeType{})
	viper.SetDefault("routes", []serve.Route{})
	viper.SetDefault("maxheadersize", "1M")
	viper.SetDefault("maxconnread", "5s")
	viper.SetDefault("maxconnheader", "2s")
	viper.SetDefault("maxconnwrite", "5s")
	viper.SetDefault("maxconnidle", "5s")
	viper.SetDefault("gracefulshutdown", "5s")

	c.rootCmd = rootCmd

	rootCmd.AddCommand(c.getServeCmd())
	rootCmd.AddCommand(c.getTreeCmd())
	rootCmd.AddCommand(c.getDocCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
		return
	}
}

func (c *Cmd) getBaseFS() (fs.FS, string) {
	base := c.serveFlags.base
	if base == "" {
		base = viper.GetString("base")
		if base == "" {
			base = "."
		}
	}
	return kfs.DirFS(base), base
}

func (c *Cmd) getContentFS(rootDir fs.FS, base string) (fs.FS, error) {
	contentdir := viper.GetString("contentdir")
	contentDir, err := fs.Sub(rootDir, contentdir)
	if err != nil {
		return nil, kerrors.WithMsg(err, "Invalid content dir")
	}
	c.log.Info(context.Background(), "Serving directory at base",
		klog.AString("fs.contentdir", path.Join(base, contentdir)),
	)
	return contentDir, nil
}

func (c *Cmd) getTreeDB(rootDir fs.FS, base string, mode string) (serve.TreeDB, error) {
	// url must be in the form of
	// file:rel/path/to/file.db?optquery=value&otheroptquery=value
	u, err := url.Parse(viper.GetString("treedb"))
	if err != nil {
		return nil, kerrors.WithMsg(err, "Invalid tree db sqlite dsn")
	}
	if u.Opaque == "" {
		return nil, kerrors.WithMsg(err, "Tree db sqlite dsn must be relative")
	}
	u.Opaque = path.Join(base, u.Opaque)
	q := u.Query()
	q.Set("mode", mode)
	u.RawQuery = q.Encode()
	d := db.NewSQLClient(c.log.Logger.Sublogger("db"), u.String())
	if err := d.Init(); err != nil {
		return nil, kerrors.WithMsg(err, "Failed to init sqlite db client")
	}

	c.log.Info(context.Background(), "Using treedb",
		klog.AString("db.engine", "sqlite"),
		klog.AString("db.file", u.Opaque),
	)

	return serve.NewSQLiteTreeDB(d, "content", "encoded", "content_gc"), nil
}

func (c *Cmd) getTree(mode string) (fs.FS, serve.TreeDB, error) {
	rootDir, base := c.getBaseFS()
	contentDir, err := c.getContentFS(rootDir, base)
	if err != nil {
		return nil, nil, err
	}
	treedb, err := c.getTreeDB(rootDir, base, mode)
	if err != nil {
		return nil, nil, err
	}
	return contentDir, treedb, nil
}

// initConfig reads in config file and ENV variables if set.
func (c *Cmd) initConfig(cmd *cobra.Command, args []string) {
	logWriter := klog.NewSyncWriter(os.Stderr)
	var handler *klog.SlogHandler
	if c.rootFlags.logPlain {
		handler = klog.NewTextSlogHandler(logWriter)
		handler.FieldTimeInfo = ""
		handler.FieldCaller = ""
		handler.FieldMod = ""
	} else {
		handler = klog.NewJSONSlogHandler(logWriter)
	}
	c.log = klog.NewLevelLogger(klog.New(
		klog.OptHandler(handler),
		klog.OptMinLevelStr(c.rootFlags.logLevel),
	))

	if c.rootFlags.cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(c.rootFlags.cfgFile)
	} else {
		viper.SetConfigName("fsserve")
		viper.AddConfigPath(".")
	}

	viper.SetEnvPrefix("FSSERVE")
	viper.AutomaticEnv() // read in environment variables that match
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "__"))

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err != nil {
		c.log.Debug(context.Background(), "Failed reading config", klog.AString("err", err.Error()))
	} else {
		c.log.Debug(context.Background(), "Read config", klog.AString("file", viper.ConfigFileUsed()))
	}
}

func (c *Cmd) logFatal(err error) {
	c.log.Err(context.Background(), err)
	os.Exit(1)
}
