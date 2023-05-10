package main

import (
	"kubectl-forward-exec/pkg/cmd"
	"os"

	"github.com/spf13/pflag"

	"k8s.io/cli-runtime/pkg/genericiooptions"
)

func main() {
	flags := pflag.NewFlagSet("kubectl-forward-exec", pflag.ExitOnError)
	pflag.CommandLine = flags

	root := cmd.NewForwardExecCmd(genericiooptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr})
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
