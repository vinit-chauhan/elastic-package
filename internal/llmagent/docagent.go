// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package llmagent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/elastic/elastic-package/internal/configuration/locations"
	"github.com/elastic/elastic-package/internal/docs"
	"github.com/elastic/elastic-package/internal/environment"
	"github.com/elastic/elastic-package/internal/logger"
	"github.com/elastic/elastic-package/internal/packages"
	"github.com/elastic/elastic-package/internal/profile"
	"github.com/elastic/elastic-package/internal/tui"
)

//go:embed _static/initial_prompt.txt
var initialPrompt string

//go:embed _static/revision_prompt.txt
var revisionPrompt string

//go:embed _static/limit_hit_prompt.txt
var limitHitPrompt string

// loadPromptFile loads a prompt file from external location if enabled, otherwise uses embedded content
func loadPromptFile(filename string, embeddedContent string, profile *profile.Profile) string {
	// Check if external prompt files are enabled
	envVar := environment.WithElasticPackagePrefix("LLM_EXTERNAL_PROMPTS")
	configKey := "llm.external_prompts"
	useExternal := getConfigValue(profile, envVar, configKey, "false") == "true"

	if !useExternal {
		return embeddedContent
	}

	// Check in profile directory first if profile is available
	if profile != nil {
		profilePath := filepath.Join(profile.ProfilePath, "prompts", filename)
		if content, err := os.ReadFile(profilePath); err == nil {
			logger.Debugf("Loaded external prompt file from profile: %s", profilePath)
			return string(content)
		}
	}

	// Try to load from .elastic-package directory
	loc, err := locations.NewLocationManager()
	if err != nil {
		logger.Debugf("Failed to get location manager, using embedded prompt: %v", err)
		return embeddedContent
	}

	// Check in .elastic-package directory
	elasticPackagePath := filepath.Join(loc.RootDir(), "prompts", filename)
	if content, err := os.ReadFile(elasticPackagePath); err == nil {
		logger.Debugf("Loaded external prompt file from .elastic-package: %s", elasticPackagePath)
		return string(content)
	}

	// Fall back to embedded content
	logger.Debugf("External prompt file not found, using embedded content for: %s", filename)
	return embeddedContent
}

// getConfigValue retrieves a configuration value with fallback from environment variable to profile config
func getConfigValue(profile *profile.Profile, envVar, configKey, defaultValue string) string {
	// First check environment variable
	if envValue := os.Getenv(envVar); envValue != "" {
		return envValue
	}

	// Then check profile configuration
	if profile != nil {
		return profile.Config(configKey, defaultValue)
	}

	return defaultValue
}

// readServiceInfo reads the service_info.md file if it exists in docs/knowledge_base/
// Returns the content and whether the file exists
func (d *DocumentationAgent) readServiceInfo() (string, bool) {
	serviceInfoPath := filepath.Join(d.packageRoot, "docs", "knowledge_base", "service_info.md")
	content, err := os.ReadFile(serviceInfoPath)
	if err != nil {
		return "", false
	}
	return string(content), true
}

// DocumentationAgent handles documentation updates for packages
type DocumentationAgent struct {
	agent                 *Agent
	packageRoot           string
	targetDocFile         string // Target documentation file (e.g., README.md, vpc.md)
	profile               *profile.Profile
	originalReadmeContent *string // Stores original content for restoration on cancel
}

// NewDocumentationAgent creates a new documentation agent
func NewDocumentationAgent(provider LLMProvider, packageRoot string, targetDocFile string, profile *profile.Profile) (*DocumentationAgent, error) {
	// Create tools for package operations
	tools := PackageTools(packageRoot)

	// Load the mcp file
	servers := MCPTools()
	if servers != nil {
		for _, srv := range servers.Servers {
			if len(srv.Tools) > 0 {
				tools = append(tools, srv.Tools...)
			}
		}
	}

	// Create the agent
	agent := NewAgent(provider, tools)

	return &DocumentationAgent{
		agent:         agent,
		packageRoot:   packageRoot,
		targetDocFile: targetDocFile,
		profile:       profile,
	}, nil
}

// UpdateDocumentation runs the documentation update process
func (d *DocumentationAgent) UpdateDocumentation(ctx context.Context, nonInteractive bool) error {
	// Read package manifest for context
	manifest, err := packages.ReadPackageManifestFromPackageRoot(d.packageRoot)
	if err != nil {
		return fmt.Errorf("failed to read package manifest: %w", err)
	}

	// Backup original README content before making any changes
	d.backupOriginalReadme()

	// Create the initial prompt
	prompt := d.buildInitialPrompt(manifest)

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
	prompt := d.buildRevisionPrompt(instructions)

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

	// Check for token limit messages first - these need special handling
	if isTokenLimitMessage(result.FinalContent) {
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
	}

	// Check for errors in response using enhanced detection with conversation context
	if isTaskResultError(result.FinalContent, result.Conversation) {
		fmt.Println("\n❌ Error detected in LLM response.")
		fmt.Println("In non-interactive mode, exiting due to error.")
		return fmt.Errorf("LLM agent encountered an error: %s", result.FinalContent)
	}

	// Check if documentation file was successfully updated
	if updated, err := d.handleReadmeUpdate(); updated {
		fmt.Printf("\n📄 %s was updated successfully!\n", d.targetDocFile)
		return err
	}

	// Second attempt with specific instructions
	fmt.Printf("⚠️  No %s was updated. Trying again with specific instructions...\n", d.targetDocFile)
	specificPrompt := fmt.Sprintf("You haven't updated a %s file yet. Please create the %s file in the _dev/build/docs/ directory based on your analysis. This is required to complete the task.", d.targetDocFile, d.targetDocFile)

	if _, err := d.executeTaskWithLogging(ctx, specificPrompt); err != nil {
		return fmt.Errorf("second attempt failed: %w", err)
	}

	// Final check
	if updated, err := d.handleReadmeUpdate(); updated {
		fmt.Printf("\n📄 %s was updated on second attempt!\n", d.targetDocFile)
		return err
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

		// Check for token limit messages first - these need special handling
		if isTokenLimitMessage(result.FinalContent) {
			fmt.Println("\n⚠️  LLM hit token limits. Switching to section-based generation...")
			newPrompt, err := d.handleTokenLimitResponse(result.FinalContent)
			if err != nil {
				return err
			}
			prompt = newPrompt
			continue
		}

		// Handle error responses using enhanced detection with conversation context
		if isTaskResultError(result.FinalContent, result.Conversation) {
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

		// Get user action
		action, err := d.getUserAction()
		if err != nil {
			return err
		}

		// Handle user action
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
func (d *DocumentationAgent) logAgentResponse(result *TaskResult) {
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
func (d *DocumentationAgent) executeTaskWithLogging(ctx context.Context, prompt string) (*TaskResult, error) {
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

// handleReadmeUpdate checks if documentation file was updated and reports the result
func (d *DocumentationAgent) handleReadmeUpdate() (bool, error) {
	readmeUpdated := d.checkReadmeUpdated()
	if !readmeUpdated {
		return false, nil
	}

	content, err := d.readCurrentReadme()
	if err != nil || content == "" {
		return false, err
	}

	fmt.Printf("✅ Documentation update completed! (%d characters written to %s)\n", len(content), d.targetDocFile)
	return true, nil
}

// handleInteractiveError handles error responses in interactive mode
func (d *DocumentationAgent) handleInteractiveError() (string, bool, error) {
	fmt.Println("\n❌ Error detected in LLM response.")

	errorPrompt := tui.NewSelect("What would you like to do?", []string{
		"Try again",
		"Exit",
	}, "Try again")

	var errorAction string
	err := tui.AskOne(errorPrompt, &errorAction)
	if err != nil {
		return "", false, fmt.Errorf("prompt failed: %w", err)
	}

	if errorAction == "Exit" {
		fmt.Println("⚠️  Exiting due to LLM error.")
		return "", false, nil
	}

	// Continue with retry prompt
	newPrompt := d.buildRevisionPrompt("The previous attempt encountered an error. Please try a different approach to analyze the package and create/update the documentation.")
	return newPrompt, true, nil
}

// handleUserAction processes the user's chosen action
func (d *DocumentationAgent) handleUserAction(action string, readmeUpdated bool) (string, bool, bool, error) {
	switch action {
	case "Accept and finalize":
		return d.handleAcceptAction(readmeUpdated)
	case "Request changes":
		return d.handleRequestChanges()
	case "Cancel":
		fmt.Println("❌ Documentation update cancelled.")
		d.restoreOriginalReadme()
		return "", false, true, nil
	default:
		return "", false, false, fmt.Errorf("unknown action: %s", action)
	}
}

// handleAcceptAction handles the "Accept and finalize" action
func (d *DocumentationAgent) handleAcceptAction(readmeUpdated bool) (string, bool, bool, error) {
	if readmeUpdated {
		// Validate preserved sections if we had original content
		if d.originalReadmeContent != nil {
			if newContent, err := d.readCurrentReadme(); err == nil {
				warnings := d.validatePreservedSections(*d.originalReadmeContent, newContent)
				if len(warnings) > 0 {
					fmt.Println("⚠️  Warning: Some human-edited sections may not have been preserved:")
					for _, warning := range warnings {
						fmt.Printf("   - %s\n", warning)
					}
					fmt.Println("   Please review the documentation to ensure important content wasn't lost.")
				}
			}
		}

		fmt.Println("✅ Documentation update completed!")
		return "", false, true, nil
	}

	// Documentation file wasn't updated - ask user what to do
	continuePrompt := tui.NewSelect(fmt.Sprintf("%s file wasn't updated. What would you like to do?", d.targetDocFile), []string{
		"Try again",
		"Exit anyway",
	}, "Try again")

	var continueChoice string
	err := tui.AskOne(continuePrompt, &continueChoice)
	if err != nil {
		return "", false, false, fmt.Errorf("prompt failed: %w", err)
	}

	if continueChoice == "Exit anyway" {
		fmt.Printf("⚠️  Exiting without creating %s file.\n", d.targetDocFile)
		d.restoreOriginalReadme()
		return "", false, true, nil
	}

	fmt.Printf("🔄 Trying again to create %s...\n", d.targetDocFile)
	newPrompt := d.buildRevisionPrompt(fmt.Sprintf("You haven't written a %s file yet. Please write the %s file in the _dev/build/docs/ directory based on your analysis.", d.targetDocFile, d.targetDocFile))
	return newPrompt, true, false, nil
}

// handleRequestChanges handles the "Request changes" action
func (d *DocumentationAgent) handleRequestChanges() (string, bool, bool, error) {
	changes, err := tui.AskTextArea("What changes would you like to make to the documentation?")
	if err != nil {
		// Check if user cancelled
		if errors.Is(err, tui.ErrCancelled) {
			fmt.Println("⚠️  Changes request cancelled.")
			return "", true, false, nil // Continue the loop
		}
		return "", false, false, fmt.Errorf("prompt failed: %w", err)
	}

	// Check if no changes were provided
	if strings.TrimSpace(changes) == "" {
		fmt.Println("⚠️  No changes specified. Please try again.")
		return "", true, false, nil // Continue the loop
	}

	newPrompt := d.buildRevisionPrompt(changes)
	return newPrompt, true, false, nil
}

// buildInitialPrompt creates the initial prompt for the LLM
func (d *DocumentationAgent) buildInitialPrompt(manifest *packages.PackageManifest) string {
	promptContent := loadPromptFile("initial_prompt.txt", initialPrompt, d.profile)
	basePrompt := fmt.Sprintf(promptContent,
		d.targetDocFile, // Line 5: file path in task description
		manifest.Name,
		manifest.Title,
		manifest.Type,
		manifest.Version,
		manifest.Description,
		d.targetDocFile, // Line 16: file restriction directive
		d.targetDocFile, // Line 33: tool usage guideline
		d.targetDocFile, // Line 43: initial analysis step
		d.targetDocFile) // Line 69: write results step

	// Check if service_info.md exists and append it to the prompt
	if serviceInfo, exists := d.readServiceInfo(); exists {
		basePrompt += fmt.Sprintf("\n\nKNOWLEDGE BASE - SERVICE INFORMATION (SOURCE OF TRUTH):\nThe following information is from docs/knowledge_base/service_info.md and should be treated as the authoritative source.\nIf you find conflicting information from other sources (web search, etc.), prefer the information below.\n\n---\n%s\n---\n", serviceInfo)
	}

	return basePrompt
}

// buildRevisionPrompt creates a comprehensive prompt for document revisions that includes all necessary context
func (d *DocumentationAgent) buildRevisionPrompt(changes string) string {
	// Read package manifest for context
	manifest, err := packages.ReadPackageManifestFromPackageRoot(d.packageRoot)
	if err != nil {
		// Fallback to a simpler prompt if we can't read the manifest
		return fmt.Sprintf("Please make the following changes to the documentation:\n\n%s", changes)
	}

	promptContent := loadPromptFile("revision_prompt.txt", revisionPrompt, d.profile)
	basePrompt := fmt.Sprintf(promptContent,
		d.targetDocFile, // Line 5: target documentation file label
		manifest.Name,
		manifest.Title,
		manifest.Type,
		manifest.Version,
		manifest.Description,
		d.targetDocFile, // Line 15: file restriction directive
		d.targetDocFile, // Line 17: read current content directive
		d.targetDocFile, // Line 35: tool usage guideline
		d.targetDocFile, // Line 38: step 1 - read current file
		d.targetDocFile, // Line 44: step 7 - write documentation
		changes)         // Line 47: user-requested changes

	// Check if service_info.md exists and append it to the prompt
	if serviceInfo, exists := d.readServiceInfo(); exists {
		basePrompt += fmt.Sprintf("\n\nKNOWLEDGE BASE - SERVICE INFORMATION (SOURCE OF TRUTH):\nThe following information is from docs/knowledge_base/service_info.md and should be treated as the authoritative source.\nIf you find conflicting information from other sources (web search, etc.), prefer the information below.\n\n---\n%s\n---\n", serviceInfo)
	}

	return basePrompt
}

// handleTokenLimitResponse creates a section-based prompt when LLM hits token limits
func (d *DocumentationAgent) handleTokenLimitResponse(originalResponse string) (string, error) {
	// Read package manifest for context
	manifest, err := packages.ReadPackageManifestFromPackageRoot(d.packageRoot)
	if err != nil {
		return "", fmt.Errorf("failed to read package manifest: %w", err)
	}

	// Create a section-based generation prompt
	sectionBasedPrompt := d.buildSectionBasedPrompt(manifest)
	return sectionBasedPrompt, nil
}

// buildSectionBasedPrompt creates a prompt for generating documentation in sections
func (d *DocumentationAgent) buildSectionBasedPrompt(manifest *packages.PackageManifest) string {
	promptContent := loadPromptFile("limit_hit_prompt.txt", limitHitPrompt, d.profile)
	return fmt.Sprintf(promptContent,
		d.targetDocFile, // Line 3: task description
		d.targetDocFile, // Line 5: target documentation file label
		manifest.Name,
		manifest.Title,
		manifest.Type,
		manifest.Version,
		manifest.Description,
		d.targetDocFile, // Line 33: write_file tool description
		d.targetDocFile) // Line 42: step 2 - read current file
}

// displayReadmeIfUpdated shows documentation content if it was updated
func (d *DocumentationAgent) displayReadmeIfUpdated() bool {
	readmeUpdated := d.checkReadmeUpdated()
	if !readmeUpdated {
		fmt.Printf("\n⚠️  %s file not updated\n", d.targetDocFile)
		return false
	}

	sourceContent, err := d.readCurrentReadme()
	if err != nil || sourceContent == "" {
		fmt.Printf("\n⚠️  %s file exists but could not be read or is empty\n", d.targetDocFile)
		return false
	}

	// Try to render the content
	renderedContent, shouldBeRendered, err := docs.GenerateReadme(d.targetDocFile, d.packageRoot)
	if err != nil || !shouldBeRendered {
		fmt.Printf("\n⚠️  The generated %s could not be rendered.\n", d.targetDocFile)
		fmt.Println("It's recommended that you do not accept this version (ask for revisions or cancel).")
		return true
	}

	// Show the processed/rendered content
	processedContentStr := string(renderedContent)
	fmt.Printf("📊 Processed %s stats: %d characters, %d lines\n", d.targetDocFile, len(processedContentStr), strings.Count(processedContentStr, "\n")+1)

	// Try to open in browser first
	if tryBrowserPreview(processedContentStr) {
		fmt.Println("🌐 Opening documentation preview in your web browser...")
		fmt.Println("💡 Return here to accept or request changes.")
	} else {
		// Fallback to terminal display if browser preview fails
		title := fmt.Sprintf("📄 Processed %s (as generated by elastic-package build)", d.targetDocFile)
		if err := tui.ShowContent(title, processedContentStr); err != nil {
			// Fallback to simple print if viewer fails
			fmt.Printf("\n%s:\n", title)
			fmt.Println(strings.Repeat("=", 70))
			fmt.Println(processedContentStr)
			fmt.Println(strings.Repeat("=", 70))
		}
	}

	return true
}

// getUserAction prompts the user for their next action
func (d *DocumentationAgent) getUserAction() (string, error) {
	selectPrompt := tui.NewSelect("What would you like to do?", []string{
		"Accept and finalize",
		"Request changes",
		"Cancel",
	}, "Accept and finalize")

	var action string
	err := tui.AskOne(selectPrompt, &action)
	if err != nil {
		return "", fmt.Errorf("prompt failed: %w", err)
	}

	return action, nil
}

// checkReadmeUpdated checks if the documentation file has been updated by comparing current content to originalReadmeContent
func (d *DocumentationAgent) checkReadmeUpdated() bool {
	docPath := filepath.Join(d.packageRoot, "_dev", "build", "docs", d.targetDocFile)

	// Check if file exists
	if _, err := os.Stat(docPath); err != nil {
		return false
	}

	// Read current content
	currentContent, err := os.ReadFile(docPath)
	if err != nil {
		return false
	}

	currentContentStr := string(currentContent)

	// If there was no original content, any new content means it's updated
	if d.originalReadmeContent == nil {
		return currentContentStr != ""
	}

	// Compare current content with original content
	return currentContentStr != *d.originalReadmeContent
}

// readCurrentReadme reads the current documentation file content
func (d *DocumentationAgent) readCurrentReadme() (string, error) {
	docPath := filepath.Join(d.packageRoot, "_dev", "build", "docs", d.targetDocFile)
	content, err := os.ReadFile(docPath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// validatePreservedSections checks if human-edited sections are preserved in the new content
func (d *DocumentationAgent) validatePreservedSections(originalContent, newContent string) []string {
	var warnings []string

	// Extract preserved sections from original content
	preservedSections := d.extractPreservedSections(originalContent)

	// Check if each preserved section exists in the new content
	for marker, content := range preservedSections {
		if !strings.Contains(newContent, content) {
			warnings = append(warnings, fmt.Sprintf("Human-edited section '%s' was not preserved", marker))
		}
	}

	return warnings
}

// isErrorResponse detects if the LLM response indicates an error occurred
// This is now a wrapper that calls the more sophisticated analysis function
func isErrorResponse(content string) bool {
	// Use the enhanced error detection that considers conversation context
	return isTaskResultError(content, nil)
}

// isTaskResultError provides sophisticated error detection considering conversation context
func isTaskResultError(content string, conversation []ConversationEntry) bool {
	// Empty content is not necessarily an error - it might be after successful tool execution
	if strings.TrimSpace(content) == "" {
		// If we have conversation context, check if recent tools succeeded
		if conversation != nil && hasRecentSuccessfulTools(conversation) {
			return false
		}
		// Empty content without context might indicate a problem, but let's be lenient
		return false
	}

	// Check for token limit messages - these are NOT errors, they're recoverable conditions
	if isTokenLimitMessage(content) {
		return false
	}

	errorIndicators := []string{
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
	}

	contentLower := strings.ToLower(content)

	// Check for explicit error indicators
	hasErrorIndicator := false
	for _, indicator := range errorIndicators {
		if strings.Contains(contentLower, strings.ToLower(indicator)) {
			hasErrorIndicator = true
			break
		}
	}

	if !hasErrorIndicator {
		return false
	}

	// If we have conversation context and recent tools succeeded, this might be a false error
	if conversation != nil && hasRecentSuccessfulTools(conversation) {
		return false
	}

	return true
}

// isTokenLimitMessage detects if the LLM response indicates it hit token limits
func isTokenLimitMessage(content string) bool {
	tokenLimitIndicators := []string{
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
	}

	contentLower := strings.ToLower(content)
	for _, indicator := range tokenLimitIndicators {
		if strings.Contains(contentLower, strings.ToLower(indicator)) {
			return true
		}
	}
	return false
}

// hasRecentSuccessfulTools checks if recent tool executions in the conversation were successful
func hasRecentSuccessfulTools(conversation []ConversationEntry) bool {
	// Look at the last few conversation entries for successful tool results
	for i := len(conversation) - 1; i >= 0 && i >= len(conversation)-5; i-- {
		entry := conversation[i]
		if entry.Type == "tool_result" {
			content := strings.ToLower(entry.Content)
			// Check for success indicators
			if strings.Contains(content, "✅ success") ||
				strings.Contains(content, "successfully wrote") ||
				strings.Contains(content, "completed successfully") {
				return true
			}
			// If we hit an actual error, stop looking
			if strings.Contains(content, "❌ error") ||
				strings.Contains(content, "failed:") ||
				strings.Contains(content, "access denied") {
				return false
			}
		}
	}
	return false
}

// extractPreservedSections extracts all human-edited sections from content
func (d *DocumentationAgent) extractPreservedSections(content string) map[string]string {
	sections := make(map[string]string)

	// Define marker pairs
	markers := []struct {
		start, end string
		name       string
	}{
		{"<!-- PRESERVE START -->", "<!-- PRESERVE END -->", "PRESERVE"},
	}

	for _, marker := range markers {
		startIdx := 0
		sectionNum := 1

		for {
			start := strings.Index(content[startIdx:], marker.start)
			if start == -1 {
				break
			}
			start += startIdx

			end := strings.Index(content[start:], marker.end)
			if end == -1 {
				break
			}
			end += start

			// Extract the full section including markers
			sectionContent := content[start : end+len(marker.end)]
			sectionKey := fmt.Sprintf("%s-%d", marker.name, sectionNum)
			sections[sectionKey] = sectionContent

			startIdx = end + len(marker.end)
			sectionNum++
		}
	}

	return sections
}

// backupOriginalReadme stores the current documentation file content for potential restoration and comparison to the generated version
func (d *DocumentationAgent) backupOriginalReadme() {
	docPath := filepath.Join(d.packageRoot, "_dev", "build", "docs", d.targetDocFile)

	// Check if documentation file exists
	if _, err := os.Stat(docPath); err == nil {
		// Read and store the original content
		if content, err := os.ReadFile(docPath); err == nil {
			contentStr := string(content)
			d.originalReadmeContent = &contentStr
			fmt.Printf("📋 Backed up original %s (%d characters)\n", d.targetDocFile, len(contentStr))
		} else {
			fmt.Printf("⚠️  Could not read original %s for backup: %v\n", d.targetDocFile, err)
		}
	} else {
		d.originalReadmeContent = nil
		fmt.Printf("📋 No existing %s found - will create new one\n", d.targetDocFile)
	}
}

// restoreOriginalReadme restores the documentation file to its original state
func (d *DocumentationAgent) restoreOriginalReadme() {
	docPath := filepath.Join(d.packageRoot, "_dev", "build", "docs", d.targetDocFile)

	if d.originalReadmeContent != nil {
		// Restore original content
		if err := os.WriteFile(docPath, []byte(*d.originalReadmeContent), 0o644); err != nil {
			fmt.Printf("⚠️  Failed to restore original %s: %v\n", d.targetDocFile, err)
		} else {
			fmt.Printf("🔄 Restored original %s (%d characters)\n", d.targetDocFile, len(*d.originalReadmeContent))
		}
	} else {
		// No original file existed, so remove any file that was created
		if err := os.Remove(docPath); err != nil {
			if !os.IsNotExist(err) {
				fmt.Printf("⚠️  Failed to remove created %s: %v\n", d.targetDocFile, err)
			}
		} else {
			fmt.Printf("🗑️  Removed created %s file - restored to original state (no file)\n", d.targetDocFile)
		}
	}
}
