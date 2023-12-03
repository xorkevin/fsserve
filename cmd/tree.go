package cmd

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"xorkevin.dev/fsserve/serve"
	"xorkevin.dev/kerrors"
)

type (
	treeFlags struct {
		ctype   string
		src     string
		enc     []string
		dst     string
		rmAfter bool
		full    bool
	}
)

func (c *Cmd) getTreeCmd() *cobra.Command {
	treeCmd := &cobra.Command{
		Use:               "tree",
		Short:             "Manages the server content tree",
		Long:              `Manages the server content tree`,
		DisableAutoGenTag: true,
	}

	addCmd := &cobra.Command{
		Use:               "add",
		Short:             "Adds a content blob and updates the tree",
		Long:              `Adds a content blob and updates the tree`,
		Run:               c.execTreeAdd,
		DisableAutoGenTag: true,
	}
	addCmd.PersistentFlags().StringVar(&c.treeFlags.ctype, "contenttype", "", "content type of src")
	addCmd.PersistentFlags().StringVarP(&c.treeFlags.src, "src", "s", "", "file to add")
	addCmd.PersistentFlags().StringArrayVarP(&c.treeFlags.enc, "enc", "e", nil, "encoded versions of the file in the form of (code:filename)")
	addCmd.PersistentFlags().StringVarP(&c.treeFlags.dst, "file", "f", "", "destination filepath")
	treeCmd.AddCommand(addCmd)

	rmCmd := &cobra.Command{
		Use:               "rm",
		Short:             "Removes a content blob and updates the tree",
		Long:              `Removes a content blob and updates the tree`,
		Run:               c.execTreeRm,
		DisableAutoGenTag: true,
	}
	rmCmd.PersistentFlags().StringVarP(&c.treeFlags.dst, "file", "f", "", "filepath")
	treeCmd.AddCommand(rmCmd)

	initCmd := &cobra.Command{
		Use:               "init",
		Short:             "Initializes the content tree db",
		Long:              `Initializes the content tree db`,
		Run:               c.execTreeInit,
		DisableAutoGenTag: true,
	}
	treeCmd.AddCommand(initCmd)

	syncCmd := &cobra.Command{
		Use:               "sync",
		Short:             "Syncs the content tree db",
		Long:              `Syncs the content tree db`,
		Run:               c.execTreeSync,
		DisableAutoGenTag: true,
	}
	syncCmd.PersistentFlags().BoolVar(&c.treeFlags.rmAfter, "rm", false, "removes unsynced content")
	treeCmd.AddCommand(syncCmd)

	gcCmd := &cobra.Command{
		Use:               "gc",
		Short:             "GCs the content blob dir",
		Long:              `GCs the content blob dir`,
		Run:               c.execTreeGC,
		DisableAutoGenTag: true,
	}
	gcCmd.PersistentFlags().BoolVar(&c.treeFlags.full, "full", false, "perform full gc")
	treeCmd.AddCommand(gcCmd)

	return treeCmd
}

func (c *Cmd) execTreeAdd(cmd *cobra.Command, args []string) {
	enc := make([]serve.EncodedFile, 0, len(c.treeFlags.enc))
	for _, i := range c.treeFlags.enc {
		code, name, ok := strings.Cut(i, ":")
		if !ok {
			c.logFatal(kerrors.WithMsg(nil, "Invalid encoded file"))
			return
		}
		enc = append(enc, serve.EncodedFile{
			Code: code,
			Name: name,
		})
	}
	blobDir, treedb, err := c.getTree("rw")
	if err != nil {
		c.logFatal(err)
		return
	}
	tree := serve.NewTree(c.log.Logger, treedb, blobDir)
	if err := tree.Add(context.Background(), c.treeFlags.dst, c.treeFlags.ctype, c.treeFlags.src, enc); err != nil {
		c.logFatal(err)
		return
	}
}

func (c *Cmd) execTreeRm(cmd *cobra.Command, args []string) {
	blobDir, treedb, err := c.getTree("rw")
	if err != nil {
		c.logFatal(err)
		return
	}
	tree := serve.NewTree(c.log.Logger, treedb, blobDir)
	if err := tree.Rm(context.Background(), c.treeFlags.dst); err != nil {
		c.logFatal(err)
		return
	}
}

func (c *Cmd) execTreeInit(cmd *cobra.Command, args []string) {
	blobDir, treedb, err := c.getTree("rwc")
	if err != nil {
		c.logFatal(err)
		return
	}
	tree := serve.NewTree(c.log.Logger, treedb, blobDir)
	if err := tree.Setup(context.Background()); err != nil {
		c.logFatal(err)
		return
	}
}

func (c *Cmd) execTreeSync(cmd *cobra.Command, args []string) {
	var cfg serve.SyncConfig
	if err := viper.UnmarshalKey("sync", &cfg); err != nil {
		c.logFatal(kerrors.WithMsg(err, "Failed to read config sync"))
		return
	}
	blobDir, treedb, err := c.getTree("rw")
	if err != nil {
		c.logFatal(err)
		return
	}
	tree := serve.NewTree(c.log.Logger, treedb, blobDir)
	if err := tree.SyncContent(context.Background(), cfg, c.treeFlags.rmAfter); err != nil {
		c.logFatal(err)
		return
	}
}

func (c *Cmd) execTreeGC(cmd *cobra.Command, args []string) {
	blobDir, treedb, err := c.getTree("ro")
	if err != nil {
		c.logFatal(err)
		return
	}
	tree := serve.NewTree(c.log.Logger, treedb, blobDir)
	if err := tree.GCBlobDir(context.Background(), c.treeFlags.full); err != nil {
		c.logFatal(err)
		return
	}
}
