package cmd

import (
	"fmt"
	"os"
	"github.com/spf13/cobra"
)

// RootCmd represents the base command when called without any subcommands
var RootCmd = &cobra.Command{
	Use:   "xrutil",
	Short: "Exports and imports object OpenShift object definitions",
}

// Execute adds all child commands to the root command sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(-1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)
	RootCmd.PersistentFlags().Bool( "preserve-git", false, "Specify to prevent git repository directory cleanup")
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
}
