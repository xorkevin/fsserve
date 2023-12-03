package cmd

import (
	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

type (
	docFlags struct {
		outputDir string
	}
)

func (c *Cmd) getDocCmd() *cobra.Command {
	docCmd := &cobra.Command{
		Use:               "doc",
		Short:             "Generate documentation for fsserve",
		Long:              `Generate documentation for fsserve`,
		DisableAutoGenTag: true,
	}
	docCmd.PersistentFlags().StringVarP(&c.docFlags.outputDir, "output", "o", ".", "documentation output path")

	docManCmd := &cobra.Command{
		Use:               "man",
		Short:             "Generate man page documentation for fsserve",
		Long:              `Generate man page documentation for fsserve`,
		Run:               c.execDocMan,
		DisableAutoGenTag: true,
	}
	docCmd.AddCommand(docManCmd)

	docMdCmd := &cobra.Command{
		Use:               "md",
		Short:             "Generate markdown documentation for fsserve",
		Long:              `Generate markdown documentation for fsserve`,
		Run:               c.execDocMd,
		DisableAutoGenTag: true,
	}
	docCmd.AddCommand(docMdCmd)

	return docCmd
}

func (c *Cmd) execDocMan(cmd *cobra.Command, args []string) {
	if err := doc.GenManTree(c.rootCmd, &doc.GenManHeader{
		Title:   "fsserve",
		Section: "1",
	}, c.docFlags.outputDir); err != nil {
		c.logFatal(err)
		return
	}
}

func (c *Cmd) execDocMd(cmd *cobra.Command, args []string) {
	if err := doc.GenMarkdownTree(c.rootCmd, c.docFlags.outputDir); err != nil {
		c.logFatal(err)
		return
	}
}
