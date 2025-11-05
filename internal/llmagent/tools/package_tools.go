// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/elastic/elastic-package/internal/llmagent/providers"
	"github.com/elastic/elastic-package/internal/packages/archetype"
)

// PackageTools creates the tools available to the LLM for package operations.
// These tools do not allow access to `docs/`, to prevent the LLM from confusing the generated and non-generated README versions.
func PackageTools(packageRoot string) []providers.Tool {
	return []providers.Tool{
		{
			Name:        "list_directory",
			Description: "List files and directories in a given path within the package",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Directory path relative to package root (empty string for package root)",
					},
				},
				"required": []string{"path"},
			},
			Handler: listDirectoryHandler(packageRoot),
		},
		{
			Name:        "read_file",
			Description: "Read the contents of a file within the package.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "File path relative to package root",
					},
				},
				"required": []string{"path"},
			},
			Handler: readFileHandler(packageRoot),
		},
		{
			Name:        "write_file",
			Description: "Write content to a file within the package. This tool can only write in _dev/build/docs/.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "File path relative to package root",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "Content to write to the file",
					},
				},
				"required": []string{"path", "content"},
			},
			Handler: writeFileHandler(packageRoot),
		},
		{
			Name:        "get_readme_template",
			Description: "Get the README.md template that should be used as the structure for generating package documentation. This template contains the required sections and format.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []string{},
			},
			Handler: getReadmeTemplateHandler(),
		},
		{
			Name:        "get_example_readme",
			Description: "Get a high-quality example README.md that demonstrates the target quality, level of detail, and formatting. Use this as a reference for style and content structure.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []string{},
			},
			Handler: getExampleReadmeHandler(),
		},
	}
}

// listDirectoryHandler returns a handler for the list_directory tool
func listDirectoryHandler(packageRoot string) providers.ToolHandler {
	return func(ctx context.Context, arguments string) (*providers.ToolResult, error) {
		var args struct {
			Path string `json:"path"`
		}

		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return &providers.ToolResult{Error: fmt.Sprintf("failed to parse arguments: %v", err)}, nil
		}

		// Construct the full path
		fullPath := filepath.Join(packageRoot, args.Path)

		// Security check: ensure we stay within package root
		// Use filepath.Clean to resolve any "../" sequences, then check if it's still under packageRoot
		cleanPath := filepath.Clean(fullPath)
		cleanRoot := filepath.Clean(packageRoot)
		relPath, relErr := filepath.Rel(cleanRoot, cleanPath)
		if relErr != nil || strings.HasPrefix(relPath, "..") {
			return &providers.ToolResult{Error: "access denied: path outside package root"}, nil
		}

		entries, err := os.ReadDir(fullPath)
		if err != nil {
			return &providers.ToolResult{Error: fmt.Sprintf("failed to read directory: %v", err)}, nil
		}

		var result strings.Builder
		result.WriteString(fmt.Sprintf("Contents of %s:\n", args.Path))

		for _, entry := range entries {
			// Hide docs/ directory from LLM - it contains generated artifacts
			if entry.Name() == "docs" {
				continue
			}

			if entry.IsDir() {
				result.WriteString(fmt.Sprintf("  %s/ (directory)\n", entry.Name()))
			} else {
				info, err := entry.Info()
				if err == nil {
					result.WriteString(fmt.Sprintf("  %s (file, %d bytes)\n", entry.Name(), info.Size()))
				} else {
					result.WriteString(fmt.Sprintf("  %s (file)\n", entry.Name()))
				}
			}
		}

		return &providers.ToolResult{Content: result.String()}, nil
	}
}

// readFileHandler returns a handler for the read_file tool
func readFileHandler(packageRoot string) providers.ToolHandler {
	return func(ctx context.Context, arguments string) (*providers.ToolResult, error) {
		var args struct {
			Path string `json:"path"`
		}

		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return &providers.ToolResult{Error: fmt.Sprintf("failed to parse arguments: %v", err)}, nil
		}

		// Block access to generated artifacts in docs/ directory, except docs/knowledge_base/
		// which contains authoritative service information
		if strings.HasPrefix(args.Path, "docs/") && !strings.HasPrefix(args.Path, "docs/knowledge_base/") {
			return &providers.ToolResult{Error: "access denied: invalid path"}, nil
		}

		// Construct the full path
		fullPath := filepath.Join(packageRoot, args.Path)

		// Security check: ensure we stay within package root
		// Use filepath.Clean to resolve any "../" sequences, then check if it's still under packageRoot
		cleanPath := filepath.Clean(fullPath)
		cleanRoot := filepath.Clean(packageRoot)
		relPath, relErr := filepath.Rel(cleanRoot, cleanPath)
		if relErr != nil || strings.HasPrefix(relPath, "..") {
			return &providers.ToolResult{Error: "access denied: path outside package root"}, nil
		}

		content, err := os.ReadFile(fullPath)
		if err != nil {
			return &providers.ToolResult{Error: fmt.Sprintf("failed to read file: %v", err)}, nil
		}

		return &providers.ToolResult{Content: string(content)}, nil
	}
}

// writeFileHandler returns a handler for the write_file tool
func writeFileHandler(packageRoot string) providers.ToolHandler {
	return func(ctx context.Context, arguments string) (*providers.ToolResult, error) {
		var args struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}

		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return &providers.ToolResult{Error: fmt.Sprintf("failed to parse arguments: %v", err)}, nil
		}

		// Construct the full path
		fullPath := filepath.Join(packageRoot, args.Path)

		// Security check: ensure we stay within package root, and only write in "_dev/build/docs"
		allowedDir := filepath.Join(packageRoot, "_dev", "build", "docs")
		cleanPath := filepath.Clean(fullPath)
		cleanAllowed := filepath.Clean(allowedDir)
		relPath, relErr := filepath.Rel(cleanAllowed, cleanPath)
		if relErr != nil || strings.HasPrefix(relPath, "..") {
			return &providers.ToolResult{Error: "access denied: path outside allowed directory"}, nil
		}

		// Create directory if it doesn't exist
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return &providers.ToolResult{Error: fmt.Sprintf("failed to create directory: %v", err)}, nil
		}

		// Write the file
		if err := os.WriteFile(fullPath, []byte(args.Content), 0o644); err != nil {
			return &providers.ToolResult{Error: fmt.Sprintf("failed to write file: %v", err)}, nil
		}

		return &providers.ToolResult{Content: fmt.Sprintf("Successfully wrote %d bytes to %s", len(args.Content), args.Path)}, nil
	}
}

// getReadmeTemplateHandler returns a handler for the get_readme_template tool
func getReadmeTemplateHandler() providers.ToolHandler {
	return func(ctx context.Context, arguments string) (*providers.ToolResult, error) {
		// Get the embedded template content
		templateContent := archetype.GetPackageDocsReadmeTemplate()
		return &providers.ToolResult{Content: templateContent}, nil
	}
}

// getExampleReadmeHandler returns a handler for the get_example_readme tool
func getExampleReadmeHandler() providers.ToolHandler {
	return func(ctx context.Context, arguments string) (*providers.ToolResult, error) {
		// Get the embedded example content
		return &providers.ToolResult{Content: ExampleReadmeContent}, nil
	}
}
