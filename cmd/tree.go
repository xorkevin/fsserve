package cmd

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"xorkevin.dev/fsserve/serve"
	"xorkevin.dev/kerrors"
)

type (
	treeFlags struct {
		force bool
	}
)

func (c *Cmd) getTreeCmd() *cobra.Command {
	treeCmd := &cobra.Command{
		Use:               "tree",
		Short:             "Manages the server content tree",
		Long:              `Manages the server content tree`,
		DisableAutoGenTag: true,
	}

	checksumCmd := &cobra.Command{
		Use:               "checksum",
		Short:             "Checksums the content tree",
		Long:              `Checksums the content tree`,
		Run:               c.execTreeChecksum,
		DisableAutoGenTag: true,
	}
	checksumCmd.PersistentFlags().BoolVar(&c.treeFlags.force, "force", false, "recomputes checksums for files with existing checksums")
	treeCmd.AddCommand(checksumCmd)

	return treeCmd
}

func (c *Cmd) execTreeChecksum(cmd *cobra.Command, args []string) {
	var routes []serve.Route
	if err := viper.UnmarshalKey("routes", &routes); err != nil {
		c.logFatal(kerrors.WithMsg(err, "Failed to read config routes"))
		return
	}

	contentDir := c.getBaseFS()

	tree := serve.NewTree(c.log.Logger, contentDir)
	if err := tree.Checksum(context.Background(), routes, c.treeFlags.force); err != nil {
		c.logFatal(err)
		return
	}
}
