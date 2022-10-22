package cmd

import (
	"log"

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
		Short:             "generate documentation for fsserve",
		Long:              `generate documentation for fsserve in several formats`,
		DisableAutoGenTag: true,
	}
	docCmd.PersistentFlags().StringVarP(&c.docFlags.outputDir, "output", "o", ".", "documentation output path")

	docManCmd := &cobra.Command{
		Use:               "man",
		Short:             "generate man page documentation for fsserve",
		Long:              `generate man page documentation for fsserve`,
		Run:               c.execDocMan,
		DisableAutoGenTag: true,
	}
	docCmd.AddCommand(docManCmd)

	docMdCmd := &cobra.Command{
		Use:               "md",
		Short:             "generate markdown documentation for fsserve",
		Long:              `generate markdown documentation for fsserve`,
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
		log.Fatalln(err)
	}
}

func (c *Cmd) execDocMd(cmd *cobra.Command, args []string) {
	if err := doc.GenMarkdownTree(c.rootCmd, c.docFlags.outputDir); err != nil {
		log.Fatalln(err)
	}
}
