// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/elastic/elastic-package/internal/cobraext"
	"github.com/elastic/elastic-package/internal/filter"
	"github.com/elastic/elastic-package/internal/logger"
	"github.com/elastic/elastic-package/internal/multierror"
	"github.com/elastic/elastic-package/internal/packages"
	"github.com/elastic/elastic-package/internal/workdir"
)

const foreachLongDescription = `[Technical Preview]
Execute a command for each package matching the given query flags.

This command combines query capabilities with command execution, allowing you to run any elastic-package subcommand across multiple packages in a single operation.

The command uses the same query flags as the 'find' command to select packages, then executes the specified subcommand for each matched package.

Packages are processed concurrently using goroutines.`

// getAllowedSubCommands returns the list of allowed subcommands for the foreach command.
func getAllowedSubCommands() []string {
	return []string{
		"build",
		"check",
		"changelog",
		"clean",
		"format",
		"install",
		"lint",
		"test",
		"uninstall",
	}
}

func setupForeachCommand() *cobraext.Command {
	cmd := &cobra.Command{
		Use:   "foreach [flags] -- <SUBCOMMAND>",
		Short: "Execute a command for filtered packages [Technical Preview]",
		Long:  fmt.Sprintf(foreachLongDescription+"\n\nAllowed subcommands:\n%s", strings.Join(getAllowedSubCommands(), ", ")),
		Example: `  # Run system tests for packages with specific inputs
  elastic-package foreach --input tcp,udp -- test system -g`,
		RunE: foreachCommandAction,
		Args: cobra.MinimumNArgs(1),
	}

	// Add query flags
	filter.SetFilterFlags(cmd)

	return cobraext.NewCommand(cmd, cobraext.ContextPackage)
}

func foreachCommandAction(cmd *cobra.Command, args []string) error {
	if err := validateSubCommand(args[0]); err != nil {
		return fmt.Errorf("validating sub command failed: %w", err)
	}

	filtered, err := findPackage(cmd)
	if err != nil {
		return fmt.Errorf("filtering packages failed: %w", err)
	}

	if len(filtered) == 0 {
		logger.Infof("No packages matched the filter criteria")
		return nil
	}

	logger.Infof("Running command for %d packages", len(filtered))

	results := runParallel(filtered, args)

	successCount := len(filtered) - len(results)
	logger.Infof("Successfully executed command for %d packages", successCount)

	if len(results) > 0 {
		logger.Errorf("Errors occurred for %d packages", len(results))
		return fmt.Errorf("errors occurred while executing command for packages:\n%s", results.Error())
	}

	return nil
}

func runParallel(pkgs []packages.PackageDirNameAndManifest, args []string) multierror.Error {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs multierror.Error
	)

	for _, pkg := range pkgs {
		wg.Add(1)
		go func(pkg packages.PackageDirNameAndManifest) {
			defer wg.Done()

			logger.Infof("[%s] Executing: %s", pkg.DirName, strings.Join(args, " "))

			if err := executeForPackage(pkg, args); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("package %s: %w", pkg.DirName, err))
				mu.Unlock()
			}
		}(pkg)
	}

	wg.Wait()
	return errs
}

func executeForPackage(pkg packages.PackageDirNameAndManifest, args []string) error {
	rootCmd := RootCmd()

	ctx := workdir.WithDir(context.Background(), pkg.Path)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs(args)

	// Silence usage and errors — the foreach command handles error reporting.
	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true

	return rootCmd.Execute()
}

func validateSubCommand(subCommand string) error {
	if !slices.Contains(getAllowedSubCommands(), subCommand) {
		return fmt.Errorf("invalid subcommand: %s. Allowed subcommands are: [%s]", subCommand, strings.Join(getAllowedSubCommands(), ", "))
	}

	return nil
}
