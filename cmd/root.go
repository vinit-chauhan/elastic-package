// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/elastic/elastic-package/internal/cobraext"
	"github.com/elastic/elastic-package/internal/logger"
	"github.com/elastic/elastic-package/internal/version"
	"github.com/elastic/elastic-package/internal/workdir"
)

func newCommands() []*cobraext.Command {
	return []*cobraext.Command{
		setupBenchmarkCommand(),
		setupBuildCommand(),
		setupChangelogCommand(),
		setupCheckCommand(),
		setupCleanCommand(),
		setupCreateCommand(),
		setupDumpCommand(),
		setupEditCommand(),
		setupExportCommand(),
		setupFindCommand(),
		setupFormatCommand(),
		setupForeachCommand(),
		setupInstallCommand(),
		setupLinksCommand(),
		setupLintCommand(),
		setupModifyCommand(),
		setupProfilesCommand(),
		setupReportsCommand(),
		setupServiceCommand(),
		setupStackCommand(),
		setupStatusCommand(),
		setupTestCommand(),
		setupUninstallCommand(),
		setupVersionCommand(),
	}
}

// RootCmd creates and returns root cmd for elastic-package.
// Each call returns a fresh, independent command tree safe for concurrent use.
func RootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:          "elastic-package",
		Short:        "elastic-package - Command line tool for developing Elastic Integrations",
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return cobraext.ComposeCommandActions(cmd, args,
				processPersistentFlags,
				checkVersionUpdate,
			)
		},
	}
	rootCmd.PersistentFlags().CountP(cobraext.VerboseFlagName, cobraext.VerboseFlagShorthand, cobraext.VerboseFlagDescription)
	rootCmd.PersistentFlags().StringP(cobraext.ChangeDirectoryFlagName, cobraext.ChangeDirectoryFlagShorthand, "", cobraext.ChangeDirectoryFlagDescription)

	for _, cmd := range newCommands() {
		rootCmd.AddCommand(cmd.Command)
	}
	return rootCmd
}

// Commands returns the list of commands that have been setup for elastic-package.
func Commands() []*cobraext.Command {
	cmds := newCommands()
	sort.SliceStable(cmds, func(i, j int) bool {
		return cmds[i].Name() < cmds[j].Name()
	})

	return cmds
}

func processPersistentFlags(cmd *cobra.Command, args []string) error {
	verbose, err := cmd.Flags().GetCount(cobraext.VerboseFlagName)
	if err != nil {
		return cobraext.FlagParsingError(err, cobraext.VerboseFlagName)
	}
	if verbose == 1 {
		logger.EnableDebugMode()
	} else if verbose > 1 {
		logger.EnableTraceMode()
	}

	// When the working directory is already set via context (e.g. by the
	// foreach command), skip os.Chdir so that concurrent goroutines don't
	// interfere with each other's working directories.
	if _, ok := workdir.DirFromContext(cmd.Context()); ok {
		return nil
	}

	changeDirectory, err := cmd.Flags().GetString(cobraext.ChangeDirectoryFlagName)
	if err != nil {
		return cobraext.FlagParsingError(err, cobraext.ChangeDirectoryFlagName)
	}
	if changeDirectory != "" {
		err := os.Chdir(changeDirectory)
		if err != nil {
			return fmt.Errorf("failed to change directory: %w", err)
		}
		logger.Debugf("Running command in directory \"%s\"", changeDirectory)
	}

	return nil
}

func checkVersionUpdate(cmd *cobra.Command, args []string) error {
	version.CheckUpdate(cmd.Context())
	return nil
}
