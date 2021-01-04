// Copyright 2020 Vectorized, Inc.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.md
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0

package cmd

import (
	"os"
	"vectorized/pkg/cli"
	"vectorized/pkg/config"

	"github.com/Shopify/sarama"
	"github.com/fatih/color"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh/terminal"
)

const feedbackMsg = `We'd love to hear about your experience with redpanda:
https://vectorized.io/feedback`

func Execute() {
	verbose := false
	fs := afero.NewOsFs()
	mgr := config.NewManager(fs)

	if !terminal.IsTerminal(int(os.Stdout.Fd())) {
		color.NoColor = true
	}
	log.SetFormatter(cli.NewRpkLogFormatter())
	cobra.OnInitialize(func() {
		// This is only executed when a subcommand (e.g. rpk check) is
		// specified.
		if verbose {
			log.SetLevel(log.DebugLevel)
			// Make sure we enable verbose logging for sarama client
			// we configure the Sarama logger only for verbose output as sarama
			// logger use no severities. It is either enabled or disabled.
			sarama.Logger = &log.Logger{
				Out:		os.Stderr,
				Formatter:	cli.NewRpkLogFormatter(),
				Hooks:		make(log.LevelHooks),
				Level:		log.DebugLevel,
				ExitFunc:	os.Exit,
				ReportCaller:	false,
			}
		} else {
			log.SetLevel(log.InfoLevel)
		}
	})

	rootCmd := &cobra.Command{
		Use:	"rpk",
		Short:	"rpk is the Redpanda CLI & toolbox",
		Long:	"",
	}
	rootCmd.SilenceUsage = true
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose",
		"v", false, "enable verbose logging (default false)")

	rootCmd.AddCommand(NewModeCommand(mgr))
	rootCmd.AddCommand(NewConfigCommand(fs, mgr))
	rootCmd.AddCommand(NewStatusCommand(fs, mgr))
	rootCmd.AddCommand(NewGenerateCommand(mgr))
	rootCmd.AddCommand(NewVersionCommand())
	rootCmd.AddCommand(NewApiCommand(fs, mgr))
	rootCmd.AddCommand(NewWasmCommand(fs))
	rootCmd.AddCommand(NewContainerCommand())

	addPlatformDependentCmds(fs, mgr, rootCmd)

	err := rootCmd.Execute()
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "check":
			fallthrough
		case "tune":
			log.Info(feedbackMsg)
		}
	}
	if err != nil {
		os.Exit(1)
	}
}
