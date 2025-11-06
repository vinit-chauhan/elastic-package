// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
