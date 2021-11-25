package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"xorkevin.dev/fsserve/serve"
)

var (
	servePort int
	serveBase string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serves a local file system with an http server",
	Long:  `Serves a local file system with an http server`,
	Run: func(cmd *cobra.Command, args []string) {
		var routes []*serve.Route
		if err := viper.UnmarshalKey("routes", &routes); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		s, err := serve.NewServer(serveBase, routes)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		s.Serve(context.Background(), servePort, serve.Opts{
			ReadTimeout:       readDurationConfig(viper.GetString("maxconnread"), seconds5),
			ReadHeaderTimeout: readDurationConfig(viper.GetString("maxconnheader"), seconds2),
			WriteTimeout:      readDurationConfig(viper.GetString("maxconnwrite"), seconds5),
			IdleTimeout:       readDurationConfig(viper.GetString("maxconnidle"), seconds5),
			MaxHeaderBytes:    readBytesConfig(viper.GetString("maxheadersize"), MEGABYTE),
			GracefulShutdown:  readDurationConfig(viper.GetString("gracefulshutdown"), seconds5),
		})
	},
	DisableAutoGenTag: true,
}

func init() {
	rootCmd.AddCommand(serveCmd)

	serveCmd.PersistentFlags().IntVarP(&servePort, "port", "p", 8080, "port to run the http server on")
	serveCmd.PersistentFlags().StringVarP(&serveBase, "base", "b", ".", "static files base")

	viper.SetDefault("routes", []serve.Route{})
	viper.SetDefault("maxheadersize", "1M")
	viper.SetDefault("maxconnread", "5s")
	viper.SetDefault("maxconnheader", "2s")
	viper.SetDefault("maxconnwrite", "5s")
	viper.SetDefault("maxconnidle", "5s")
	viper.SetDefault("gracefulshutdown", "5s")
}

const (
	seconds5 = 5 * time.Second
	seconds2 = 2 * time.Second
)

func readDurationConfig(s string, d time.Duration) time.Duration {
	t, err := time.ParseDuration(s)
	if err != nil {
		log.Printf("Invalid config time value: %s\n", s)
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

func readBytesConfig(s string, d int) int {
	b := strings.ToUpper(s)

	i := strings.IndexFunc(s, unicode.IsLetter)

	if i < 0 {
		log.Printf("Invalid config bytes value: %s\n", s)
		return d
	}

	bytesString, multiple := b[:i], b[i:]
	bytes, err := strconv.Atoi(bytesString)
	if err != nil || bytes < 0 {
		log.Printf("Invalid config bytes value: %s: %v\n", s, err)
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
		log.Printf("Invalid config bytes value: %s\n", s)
		return d
	}
}
