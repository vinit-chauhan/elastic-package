// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package llmagent

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/elastic/elastic-package/internal/configuration/locations"
	"github.com/elastic/elastic-package/internal/packages/archetype"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The embedded example_readme is an example of a high-quality integration readme, following the static template archetype,
// which will help the LLM follow an example.
//
//go:embed _static/example_readme.md
var exampleReadmeContent string

var (
	// should these be done per-tool?
	ctx       = context.Background()
	transport mcp.Transport
)

type MCPServer struct {
	Command *string            `json:"command"`
	Args    []string           `json:"args"`
	Env     *map[string]string `json:"env"`
	Url     *string            `json:"url"`
	Headers *map[string]string `json:"headers"`

	session *mcp.ClientSession
	Tools   []Tool
}

type MCPJson struct {
	InitialPrompt  *string              `json:"initialPromptFile"`
	RevisionPrompt *string              `json:"revisionPromptFile"`
	Servers        map[string]MCPServer `json:"mcpServers"`
}

// need an MCP struct that holds an array of close functions and also an array of tools
func (s *MCPServer) Connect() error {
	ctx := context.Background()
	var transport mcp.Transport

	transport = &mcp.StreamableClientTransport{Endpoint: *(s.Url)}

	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-client", Version: "v1.0.0"}, nil)

	fmt.Printf("attempt to connect to %s\n", *(s.Url))

	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return err
	}

	s.session = cs

	// type ToolHandler func(ctx context.Context, arguments string) (*ToolResult, error)
	// need to iterate over the tools and then return those
	if (*s.session).InitializeResult().Capabilities.Tools != nil {
		for feat, err := range (*s.session).Tools(ctx, nil) {
			if err != nil {
				log.Fatal(err)
			}

			// pull out the properties and required
			required := feat.InputSchema.(map[string]any)["required"]
			if required == nil {
				required = []string{}
			}

			properties := feat.InputSchema.(map[string]interface{})["properties"]

			s.Tools = append(s.Tools, Tool{
				Name:        feat.Name,
				Description: feat.Description,
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": properties,
					"required":   required,
				},
				Handler: func(ctx context.Context, arguments string) (*ToolResult, error) {
					myCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()
					res, err := s.session.CallTool(myCtx, &mcp.CallToolParams{Name: feat.Name, Arguments: json.RawMessage(arguments)})
					if err != nil {
						fmt.Printf("failed to call tool with error %v", err)
						return nil, err
					}
					data, err := json.Marshal(res)
					if err != nil {
						return nil, err
					}
					return &ToolResult{Content: string(data)}, nil
				},
			})
		}
	}

	return nil
}

// PackageTools creates the tools available to the LLM for package operations.
// These tools do not allow access to `docs/`, to prevent the LLM from confusing the generated and non-generated README versions.
func MCPTools() *MCPJson {
	// what MCP servers can we connect to?
	// the handler will have a connection to the endpoint already established
	// we will create an mcp.StreamableClientTransport{Endpoint: url} for each endpoint
	// we will then list all the tools and read the description and arguments
	// look in the elastic-package config dir for mcp.json
	// LocationManager MCPJson() --> path/to/.elastic-package/mcp.json file
	lm, err := locations.NewLocationManager()
	if err != nil {
		return nil
	}

	// if the file doesn't exist, just bail
	mcpFile, err := os.Open(lm.MCPJson())
	if err != nil {
		return nil
	}
	defer mcpFile.Close()

	byteValue, err := ioutil.ReadAll(mcpFile)
	if err != nil {
		return nil
	}

	var mcpJson MCPJson
	json.Unmarshal(byteValue, &mcpJson)

	// handle the url thing only for now
	for key, value := range mcpJson.Servers {
		if value.Url != nil {
			err = value.Connect()
			mcpJson.Servers[key] = value
		}
	}

	return &mcpJson
}

// PackageTools creates the tools available to the LLM for package operations.
// These tools do not allow access to `docs/`, to prevent the LLM from confusing the generated and non-generated README versions.
func PackageTools(packageRoot string) []Tool {
	return []Tool{
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
func listDirectoryHandler(packageRoot string) ToolHandler {
	return func(ctx context.Context, arguments string) (*ToolResult, error) {
		var args struct {
			Path string `json:"path"`
		}

		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to parse arguments: %v", err)}, nil
		}

		// Construct the full path
		fullPath := filepath.Join(packageRoot, args.Path)

		// Security check: ensure we stay within package root
		// Use filepath.Clean to resolve any "../" sequences, then check if it's still under packageRoot
		cleanPath := filepath.Clean(fullPath)
		cleanRoot := filepath.Clean(packageRoot)
		relPath, relErr := filepath.Rel(cleanRoot, cleanPath)
		if relErr != nil || strings.HasPrefix(relPath, "..") {
			return &ToolResult{Error: "access denied: path outside package root"}, nil
		}

		entries, err := os.ReadDir(fullPath)
		if err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to read directory: %v", err)}, nil
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

		return &ToolResult{Content: result.String()}, nil
	}
}

// readFileHandler returns a handler for the read_file tool
func readFileHandler(packageRoot string) ToolHandler {
	return func(ctx context.Context, arguments string) (*ToolResult, error) {
		var args struct {
			Path string `json:"path"`
		}

		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to parse arguments: %v", err)}, nil
		}

		// Block access to generated artifacts in docs/ directory, except docs/knowledge_base/
		// which contains authoritative service information
		if strings.HasPrefix(args.Path, "docs/") && !strings.HasPrefix(args.Path, "docs/knowledge_base/") {
			return &ToolResult{Error: "access denied: invalid path"}, nil
		}

		// Construct the full path
		fullPath := filepath.Join(packageRoot, args.Path)

		// Security check: ensure we stay within package root
		// Use filepath.Clean to resolve any "../" sequences, then check if it's still under packageRoot
		cleanPath := filepath.Clean(fullPath)
		cleanRoot := filepath.Clean(packageRoot)
		relPath, relErr := filepath.Rel(cleanRoot, cleanPath)
		if relErr != nil || strings.HasPrefix(relPath, "..") {
			return &ToolResult{Error: "access denied: path outside package root"}, nil
		}

		content, err := os.ReadFile(fullPath)
		if err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to read file: %v", err)}, nil
		}

		return &ToolResult{Content: string(content)}, nil
	}
}

// writeFileHandler returns a handler for the write_file tool
func writeFileHandler(packageRoot string) ToolHandler {
	return func(ctx context.Context, arguments string) (*ToolResult, error) {
		var args struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}

		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to parse arguments: %v", err)}, nil
		}

		// Construct the full path
		fullPath := filepath.Join(packageRoot, args.Path)

		// Security check: ensure we stay within package root, and only write in "_dev/build/docs"
		allowedDir := filepath.Join(packageRoot, "_dev", "build", "docs")
		cleanPath := filepath.Clean(fullPath)
		cleanAllowed := filepath.Clean(allowedDir)
		relPath, relErr := filepath.Rel(cleanAllowed, cleanPath)
		if relErr != nil || strings.HasPrefix(relPath, "..") {
			return &ToolResult{Error: "access denied: path outside allowed directory"}, nil
		}

		// Create directory if it doesn't exist
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to create directory: %v", err)}, nil
		}

		// Write the file
		if err := os.WriteFile(fullPath, []byte(args.Content), 0o644); err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to write file: %v", err)}, nil
		}

		return &ToolResult{Content: fmt.Sprintf("Successfully wrote %d bytes to %s", len(args.Content), args.Path)}, nil
	}
}

// getReadmeTemplateHandler returns a handler for the get_readme_template tool
func getReadmeTemplateHandler() ToolHandler {
	return func(ctx context.Context, arguments string) (*ToolResult, error) {
		// Get the embedded template content
		templateContent := archetype.GetPackageDocsReadmeTemplate()
		return &ToolResult{Content: templateContent}, nil
	}
}

// getExampleReadmeHandler returns a handler for the get_example_readme tool
func getExampleReadmeHandler() ToolHandler {
	return func(ctx context.Context, arguments string) (*ToolResult, error) {
		// Get the embedded example content
		return &ToolResult{Content: exampleReadmeContent}, nil
	}
}
