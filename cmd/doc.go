package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

var (
	docOutputDir string
)

var (
	// docCmd represents the doc command
	docCmd = &cobra.Command{
		Use:               "doc",
		Short:             "generate documentation for fsserve",
		Long:              `generate documentation for fsserve in several formats`,
		DisableAutoGenTag: true,
	}

	docManCmd = &cobra.Command{
		Use:   "man",
		Short: "generate man page documentation for fsserve",
		Long:  `generate man page documentation for fsserve`,
		Run: func(cmd *cobra.Command, args []string) {
			if err := doc.GenManTree(rootCmd, &doc.GenManHeader{
				Title:   "fsserve",
				Section: "1",
			}, docOutputDir); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		},
		DisableAutoGenTag: true,
	}

	docMdCmd = &cobra.Command{
		Use:   "md",
		Short: "generate markdown documentation for fsserve",
		Long:  `generate markdown documentation for fsserve`,
		Run: func(cmd *cobra.Command, args []string) {
			if err := doc.GenMarkdownTree(rootCmd, docOutputDir); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		},
		DisableAutoGenTag: true,
	}
)

func init() {
	rootCmd.AddCommand(docCmd)
	docCmd.AddCommand(docManCmd)
	docCmd.AddCommand(docMdCmd)

	docCmd.PersistentFlags().StringVarP(&docOutputDir, "output", "o", ".", "documentation output path")
}
