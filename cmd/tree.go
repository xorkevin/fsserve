package cmd

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
	"xorkevin.dev/fsserve/serve"
	"xorkevin.dev/kerrors"
)

type (
	treeFlags struct {
		ctype string
		src   string
		enc   []string
		dst   string
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
	addCmd.PersistentFlags().StringVar(&c.treeFlags.src, "contenttype", "", "content type of src")
	addCmd.PersistentFlags().StringVarP(&c.treeFlags.src, "src", "s", "", "file to add")
	addCmd.PersistentFlags().StringArrayVarP(&c.treeFlags.enc, "enc", "e", nil, "encoded versions of the file in the form of (code:filename)")
	addCmd.PersistentFlags().StringVarP(&c.treeFlags.dst, "file", "f", "", "destination filepath")
	treeCmd.AddCommand(addCmd)

	rmCmd := &cobra.Command{
		Use:               "rm",
		Short:             "Removes content and updates the content tree",
		Long:              `Removes content and updates the content tree`,
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
			Code: strings.TrimSpace(code),
			Name: name,
		})
	}
	contentDir, treedb, err := c.getTree("rw")
	if err != nil {
		c.logFatal(err)
		return
	}
	tree := serve.NewTree(c.log.Logger, treedb, contentDir)
	if err := tree.Add(context.Background(), c.treeFlags.dst, c.treeFlags.ctype, c.treeFlags.src, enc); err != nil {
		c.logFatal(err)
		return
	}
}

func (c *Cmd) execTreeRm(cmd *cobra.Command, args []string) {
	contentDir, treedb, err := c.getTree("rw")
	if err != nil {
		c.logFatal(err)
		return
	}
	tree := serve.NewTree(c.log.Logger, treedb, contentDir)
	if err := tree.Rm(context.Background(), c.treeFlags.dst); err != nil {
		c.logFatal(err)
		return
	}
}

func (c *Cmd) execTreeInit(cmd *cobra.Command, args []string) {
	contentDir, treedb, err := c.getTree("rw")
	if err != nil {
		c.logFatal(err)
		return
	}
	tree := serve.NewTree(c.log.Logger, treedb, contentDir)
	if err := tree.Setup(context.Background()); err != nil {
		c.logFatal(err)
		return
	}
}
