// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package docs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/elastic/elastic-package/internal/llmagent/agent"
	"github.com/elastic/elastic-package/internal/llmagent/mcp"
	"github.com/elastic/elastic-package/internal/llmagent/providers"
	"github.com/elastic/elastic-package/internal/llmagent/tools"
	"github.com/elastic/elastic-package/internal/logger"
	"github.com/elastic/elastic-package/internal/packages"
	"github.com/elastic/elastic-package/internal/profile"
	"github.com/elastic/elastic-package/internal/tui"
)

type ResponseStatus int

const (
	// ResponseSuccess indicates the LLM response is valid and successful
	ResponseSuccess ResponseStatus = iota
	// ResponseError indicates the LLM encountered an error
	ResponseError
	// ResponseTokenLimit indicates the LLM hit a token/length limit
	ResponseTokenLimit
	// ResponseEmpty indicates the response was empty (may or may not indicate an error)
	ResponseEmpty
)

type ResponseAnalyzer struct {
	successIndicators    []string
	errorIndicators      []string
	errorMarkers         []string
	tokenLimitIndicators []string
}

// ResponseAnalysis contains the results of analyzing an LLM response
type ResponseAnalysis struct {
	Status  ResponseStatus
	Message string // Optional message explaining the status
}

// DocumentationAgent handles documentation updates for packages
type DocumentationAgent struct {
	agent                 *agent.Agent
	packageRoot           string
	targetDocFile         string // Target documentation file (e.g., README.md, vpc.md)
	profile               *profile.Profile
	originalReadmeContent *string // Stores original content for restoration on cancel
	manifest              *packages.PackageManifest
	responseAnalyzer      *ResponseAnalyzer
}

type PromptContext struct {
	Manifest       *packages.PackageManifest
	TargetDocFile  string
	Changes        string
	ServiceInfo    string
	HasServiceInfo bool
}

// NewDocumentationAgent creates a new documentation agent
func NewDocumentationAgent(provider providers.LLMProvider, packageRoot string, targetDocFile string, profile *profile.Profile) (*DocumentationAgent, error) {
	// Create tools for package operations
	packageTools := tools.PackageTools(packageRoot)

	// Load the mcp file
	servers := mcp.MCPTools()
	if servers != nil {
		for _, srv := range servers.Servers {
			if len(srv.Tools) > 0 {
				packageTools = append(packageTools, srv.Tools...)
			}
		}
	}

	// Create the agent
	llmAgent := agent.NewAgent(provider, packageTools)

	manifest, err := packages.ReadPackageManifestFromPackageRoot(packageRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to read package manifest: %w", err)
	}

	responseAnalyzer := NewResponseAnalyzer()
	return &DocumentationAgent{
		agent:            llmAgent,
		packageRoot:      packageRoot,
		targetDocFile:    targetDocFile,
		profile:          profile,
		manifest:         manifest,
		responseAnalyzer: responseAnalyzer,
	}, nil
}

// UpdateDocumentation runs the documentation update process
func (d *DocumentationAgent) UpdateDocumentation(ctx context.Context, nonInteractive bool) error {
	// Backup original README content before making any changes
	d.backupOriginalReadme()

	// Create the initial prompt
	promptCtx := d.createPromptContext(d.manifest, "")
	prompt := d.buildPrompt(PromptTypeInitial, promptCtx)

	if nonInteractive {
		return d.runNonInteractiveMode(ctx, prompt)
	}

	return d.runInteractiveMode(ctx, prompt)
}

// ModifyDocumentation runs the documentation modification process for targeted changes
func (d *DocumentationAgent) ModifyDocumentation(ctx context.Context, nonInteractive bool, modifyPrompt string) error {
	// Check if documentation file exists
	docPath := filepath.Join(d.packageRoot, "_dev", "build", "docs", d.targetDocFile)
	if _, err := os.Stat(docPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("cannot modify documentation: %s does not exist at _dev/build/docs/%s", d.targetDocFile, d.targetDocFile)
		}
		return fmt.Errorf("failed to check %s: %w", d.targetDocFile, err)
	}

	// Backup original README content before making any changes
	d.backupOriginalReadme()

	// Get modification instructions if not provided
	var instructions string
	if modifyPrompt != "" {
		instructions = modifyPrompt
	} else if !nonInteractive {
		// Prompt user for modification instructions
		var err error
		instructions, err = tui.AskTextArea("What changes would you like to make to the documentation?")
		if err != nil {
			// Check if user cancelled
			if errors.Is(err, tui.ErrCancelled) {
				fmt.Println("⚠️  Modification cancelled.")
				return nil
			}
			return fmt.Errorf("prompt failed: %w", err)
		}

		// Check if no changes were provided
		if strings.TrimSpace(instructions) == "" {
			return fmt.Errorf("no modification instructions provided")
		}
	} else {
		return fmt.Errorf("--modify-prompt flag is required in non-interactive mode")
	}

	// Create the revision prompt with modification instructions
	promptCtx := d.createPromptContext(d.manifest, instructions)
	prompt := d.buildPrompt(PromptTypeRevision, promptCtx)

	if nonInteractive {
		return d.runNonInteractiveMode(ctx, prompt)
	}

	return d.runInteractiveMode(ctx, prompt)
}

// runNonInteractiveMode handles the non-interactive documentation update flow
func (d *DocumentationAgent) runNonInteractiveMode(ctx context.Context, prompt string) error {
	fmt.Println("Starting non-interactive documentation update process...")
	fmt.Println("The LLM agent will analyze your package and generate documentation automatically.")
	fmt.Println()

	// First attempt
	result, err := d.executeTaskWithLogging(ctx, prompt)
	if err != nil {
		return err
	}

	// Show the result
	fmt.Println("\n📝 Agent Response:")
	fmt.Println(strings.Repeat("-", 50))
	fmt.Println(result.FinalContent)
	fmt.Println(strings.Repeat("-", 50))

	analysis := d.responseAnalyzer.AnalyzeResponse(result.FinalContent, result.Conversation)

	switch analysis.Status {
	case ResponseTokenLimit:
		logger.Debug("Recieved limit hit response from LLM.")
		fmt.Println("\n⚠️  LLM hit token limits. Switching to section-based generation...")
		newPrompt, err := d.handleTokenLimitResponse(result.FinalContent)
		if err != nil {
			return fmt.Errorf("failed to handle token limit: %w", err)
		}

		// Retry with section-based approach
		if _, err := d.executeTaskWithLogging(ctx, newPrompt); err != nil {
			return fmt.Errorf("section-based retry failed: %w", err)
		}

		// Check if documentation file was successfully updated after retry
		if updated, err := d.handleReadmeUpdate(); updated {
			fmt.Printf("\n📄 %s was updated successfully with section-based approach!\n", d.targetDocFile)
			return err
		}
	case ResponseError:
		logger.Error("Recieved error response from LLM.")
		fmt.Println("\n❌ Error detected in LLM response.")
		fmt.Println("In non-interactive mode, exiting due to error.")
		return fmt.Errorf("LLM agent encountered an error: %s", result.FinalContent)
	}

	// Check if documentation file was successfully updated
	if updated, _ := d.handleReadmeUpdate(); updated {
		fmt.Printf("\n📄 %s was updated successfully!\n", d.targetDocFile)
		return nil
	}

	// Second attempt with specific instructions
	fmt.Printf("⚠️  %s was not updated. Trying again with specific instructions...\n", d.targetDocFile)
	specificPrompt := fmt.Sprintf("You haven't updated the %s file yet. Please write the %s file in the _dev/build/docs/ directory based on your analysis. This is required to complete the task.", d.targetDocFile, d.targetDocFile)

	if _, err := d.executeTaskWithLogging(ctx, specificPrompt); err != nil {
		return fmt.Errorf("second attempt failed: %w", err)
	}

	// Final check
	if updated, _ := d.handleReadmeUpdate(); updated {
		fmt.Printf("\n📄 %s was updated on second attempt!\n", d.targetDocFile)
		return nil
	}

	return fmt.Errorf("failed to create %s after two attempts", d.targetDocFile)
}

// runInteractiveMode handles the interactive documentation update flow
func (d *DocumentationAgent) runInteractiveMode(ctx context.Context, prompt string) error {
	fmt.Println("Starting documentation update process...")
	fmt.Println("The LLM agent will analyze your package and update the documentation.")
	fmt.Println()

	for {
		// Execute the task
		result, err := d.executeTaskWithLogging(ctx, prompt)
		if err != nil {
			return err
		}

		analysis := d.responseAnalyzer.AnalyzeResponse(result.FinalContent, result.Conversation)

		switch analysis.Status {
		case ResponseTokenLimit:
			fmt.Println("\n⚠️  LLM hit token limits. Switching to section-based generation...")
			newPrompt, err := d.handleTokenLimitResponse(result.FinalContent)
			if err != nil {
				return err
			}
			prompt = newPrompt
			continue
		case ResponseError:
			newPrompt, shouldContinue, err := d.handleInteractiveError()
			if err != nil {
				return err
			}
			if !shouldContinue {
				d.restoreOriginalReadme()
				return fmt.Errorf("user chose to exit due to LLM error")
			}
			prompt = newPrompt
			continue
		}

		// Display README content if updated
		readmeUpdated := d.displayReadmeIfUpdated()

		// Get and handle user action
		action, err := d.getUserAction()
		if err != nil {
			return err
		}
		newPrompt, shouldContinue, shouldExit, err := d.handleUserAction(action, readmeUpdated)
		if err != nil {
			return err
		}
		if shouldExit {
			return nil
		}
		if shouldContinue {
			prompt = newPrompt
			continue
		}
	}
}

// logAgentResponse logs debug information about the agent response
func (d *DocumentationAgent) logAgentResponse(result *agent.TaskResult) {
	logger.Debugf("DEBUG: Full agent task response follows (may contain sensitive content)")
	logger.Debugf("Agent task response - Success: %t", result.Success)
	logger.Debugf("Agent task response - FinalContent: %s", result.FinalContent)
	logger.Debugf("Agent task response - Conversation entries: %d", len(result.Conversation))
	for i, entry := range result.Conversation {
		logger.Debugf("Agent task response - Conversation[%d]: type=%s, content_length=%d",
			i, entry.Type, len(entry.Content))
		logger.Tracef("Agent task response - Conversation[%d]: content=%s", i, entry.Content)
	}
}

// executeTaskWithLogging executes a task and logs the result
func (d *DocumentationAgent) executeTaskWithLogging(ctx context.Context, prompt string) (*agent.TaskResult, error) {
	fmt.Println("🤖 LLM Agent is working...")

	result, err := d.agent.ExecuteTask(ctx, prompt)
	if err != nil {
		fmt.Println("❌ Agent task failed")
		fmt.Printf("❌ result is %v\n", result)
		return nil, fmt.Errorf("agent task failed: %w", err)
	}

	fmt.Println("✅ Task completed")
	d.logAgentResponse(result)
	return result, nil
}

// NewResponseAnalyzer creates a new ResponseAnalyzer with default patterns
//
// These responses should be chosen to represent LLM responses to states, but are unlikely to appear in generated
// documentation, which could trigger false positives.
func NewResponseAnalyzer() *ResponseAnalyzer {
	return &ResponseAnalyzer{
		successIndicators: []string{
			"✅ success",
			"successfully wrote",
			"completed successfully",
		},
		errorIndicators: []string{
			"I encountered an error",
			"I'm experiencing an error",
			"I cannot complete",
			"I'm unable to complete",
			"Something went wrong",
			"There was an error",
			"I'm having trouble",
			"I failed to",
			"Error occurred",
			"Task did not complete within maximum iterations",
		},
		errorMarkers: []string{
			"❌ error",
			"failed:",
		},
		tokenLimitIndicators: []string{
			"I reached the maximum response length",
			"maximum response length",
			"reached the token limit",
			"response is too long",
			"breaking this into smaller tasks",
			"due to length constraints",
			"response length limit",
			"token limit reached",
			"output limit exceeded",
			"maximum length exceeded",
		},
	}
}

// AnalyzeResponse will detect the LLM state based on it's response to us.
func (ra *ResponseAnalyzer) AnalyzeResponse(content string, conversation []agent.ConversationEntry) ResponseAnalysis {
	// Check for empty content
	if strings.TrimSpace(content) == "" {
		// Empty content might be okay if recent tools succeeded
		if conversation != nil && ra.hasRecentSuccessfulTools(conversation) {
			return ResponseAnalysis{
				Status:  ResponseSuccess,
				Message: "Empty response after successful tool execution",
			}
		}
		return ResponseAnalysis{
			Status:  ResponseEmpty,
			Message: "Empty response without tool success context",
		}
	}

	// Check for token limit first - this is NOT an error, it's recoverable
	if ra.containsAnyIndicator(content, ra.tokenLimitIndicators) {
		return ResponseAnalysis{
			Status:  ResponseTokenLimit,
			Message: "LLM hit token/length limits",
		}
	}

	// Check for error indicators
	if ra.containsAnyIndicator(content, ra.errorIndicators) {
		// However, if recent tools succeeded, this might be a false error report
		if conversation != nil && ra.hasRecentSuccessfulTools(conversation) {
			return ResponseAnalysis{
				Status:  ResponseSuccess,
				Message: "Error message detected but recent tools succeeded (likely false error)",
			}
		}
		return ResponseAnalysis{
			Status:  ResponseError,
			Message: "LLM reported an error",
		}
	}

	// Default: success
	return ResponseAnalysis{
		Status:  ResponseSuccess,
		Message: "Normal response",
	}
}

// containsAnyIndicator checks if content contains any of the given indicators (case-insensitive)
func (ra *ResponseAnalyzer) containsAnyIndicator(content string, indicators []string) bool {
	contentLower := strings.ToLower(content)
	for _, indicator := range indicators {
		if strings.Contains(contentLower, strings.ToLower(indicator)) {
			return true
		}
	}
	return false
}

// hasRecentSuccessfulTools checks if recent tool executions were successful
func (ra *ResponseAnalyzer) hasRecentSuccessfulTools(conversation []agent.ConversationEntry) bool {
	// Look at the last 5 conversation entries for tool results
	lookbackCount := 5
	startIdx := len(conversation) - lookbackCount
	if startIdx < 0 {
		startIdx = 0
	}

	for i := len(conversation) - 1; i >= startIdx; i-- {
		entry := conversation[i]
		if entry.Type == "tool_result" {
			// Check for success indicators first
			if ra.containsAnyIndicator(entry.Content, ra.successIndicators) {
				return true
			}

			// If we hit an actual error marker, stop looking
			if ra.containsAnyIndicator(entry.Content, ra.errorMarkers) {
				return false
			}
		}
	}
	return false
}
