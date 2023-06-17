package cmd

import (
	"github.com/spf13/cobra"
	"xorkevin.dev/fsserve/serve"
)

type (
	treeFlags struct {
		src string
		dst string
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
		Short:             "Adds content and updates the content tree",
		Long:              `Adds content and updates the content tree`,
		Run:               c.execTreeAdd,
		DisableAutoGenTag: true,
	}

	addCmd.PersistentFlags().StringVarP(&c.treeFlags.src, "src", "s", "", "file or dir to add")
	addCmd.PersistentFlags().StringVarP(&c.treeFlags.dst, "target", "t", "", "destination path")

	treeCmd.AddCommand(addCmd)

	return treeCmd
}

func (c *Cmd) execTreeAdd(cmd *cobra.Command, args []string) {
	contentDir, treedb, err := c.getTree("rw")
	if err != nil {
		c.logFatal(err)
		return
	}
	tree := serve.NewTree(c.log.Logger, treedb, contentDir)
	if err := tree.Add(c.treeFlags.src, c.treeFlags.dst); err != nil {
		c.logFatal(err)
		return
	}
}
