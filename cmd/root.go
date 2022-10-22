package cmd

import (
	"log"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type (
	Cmd struct {
		rootCmd    *cobra.Command
		version    string
		rootFlags  rootFlags
		serveFlags serveFlags
		docFlags   docFlags
	}

	rootFlags struct {
		cfgFile   string
		debugMode bool
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
	rootCmd.PersistentFlags().StringVar(&c.rootFlags.cfgFile, "config", "", "config file (default is $XDG_CONFIG_HOME/.fsserve.yaml)")
	rootCmd.PersistentFlags().BoolVar(&c.rootFlags.debugMode, "debug", false, "turn on debug output")
	c.rootCmd = rootCmd

	rootCmd.AddCommand(c.getServeCmd())
	rootCmd.AddCommand(c.getDocCmd())

	if err := rootCmd.Execute(); err != nil {
		log.Fatalln(err)
	}
}

// initConfig reads in config file and ENV variables if set.
func (c *Cmd) initConfig(cmd *cobra.Command, args []string) {
	if c.rootFlags.cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(c.rootFlags.cfgFile)
	} else {
		viper.SetConfigName(".fsserve")
		viper.AddConfigPath(".")
		viper.AddConfigPath("config")

		// Search config in XDG_CONFIG_HOME directory with name ".fsserve" (without extension).
		if cfgdir, err := os.UserConfigDir(); err == nil {
			viper.AddConfigPath(cfgdir)
		}
	}

	viper.SetEnvPrefix("FSSERVE")
	viper.AutomaticEnv() // read in environment variables that match
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "__"))

	// If a config file is found, read it in.
	configErr := viper.ReadInConfig()
	if c.rootFlags.debugMode {
		if configErr == nil {
			log.Printf("Using config file: %s\n", viper.ConfigFileUsed())
		} else {
			log.Printf("Failed reading config file: %v\n", configErr)
		}
	}
}
