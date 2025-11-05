// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/elastic/elastic-package/internal/logger"
)

// BedrockProvider implements LLMProvider for Amazon Bedrock
type BedrockProvider struct {
	apiKey    string
	region    string
	modelID   string
	endpoint  string
	maxTokens int
	client    *http.Client
}

// BedrockConfig holds configuration for the Bedrock provider
type BedrockConfig struct {
	APIKey    string
	Region    string
	ModelID   string
	Endpoint  string
	MaxTokens int
}

// Bedrock-specific types for API communication
type bedrockRequest struct {
	Messages  []bedrockMessage `json:"messages"`
	MaxTokens int              `json:"max_tokens"`
	Tools     []bedrockTool    `json:"tools,omitempty"`
}

type bedrockMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type bedrockTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type bedrockResponse struct {
	Content    string            `json:"content"`
	StopReason string            `json:"stop_reason"`
	ToolCalls  []bedrockToolCall `json:"tool_calls,omitempty"`
}

type bedrockToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

// NewBedrockProvider creates a new Bedrock LLM provider
func NewBedrockProvider(config BedrockConfig) *BedrockProvider {
	if config.ModelID == "" {
		config.ModelID = "anthropic.claude-3-5-sonnet-20240620-v1:0" // Default model
	}
	if config.Region == "" {
		config.Region = "us-east-1" // Default region
	}
	if config.Endpoint == "" {
		config.Endpoint = fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", config.Region)
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = 8192 // Increased for documentation generation
	}

	// Debug logging with masked API key for security
	logger.Debugf("Creating Bedrock provider with model: %s, region: %s, endpoint: %s",
		config.ModelID, config.Region, config.Endpoint)
	logger.Debugf("API key (masked for security): %s", maskAPIKey(config.APIKey))

	return &BedrockProvider{
		apiKey:    config.APIKey,
		region:    config.Region,
		modelID:   config.ModelID,
		endpoint:  config.Endpoint,
		maxTokens: config.MaxTokens,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Name returns the provider name
func (b *BedrockProvider) Name() string {
	return "Amazon Bedrock"
}

// GenerateResponse sends a prompt to Bedrock and returns the response
func (b *BedrockProvider) GenerateResponse(ctx context.Context, prompt string, tools []Tool) (*LLMResponse, error) {
	// Convert tools to Bedrock format
	bedrockTools := make([]bedrockTool, len(tools))
	for i, tool := range tools {
		bedrockTools[i] = bedrockTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.Parameters,
		}
	}

	// Prepare request payload
	requestPayload := bedrockRequest{
		Messages: []bedrockMessage{
			{
				Role:    "user",
				Content: prompt,
			},
		},
		MaxTokens: b.maxTokens,
		Tools:     bedrockTools,
	}

	jsonPayload, err := json.Marshal(requestPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	url := fmt.Sprintf("%s/model/%s/invoke", b.endpoint, b.modelID)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.apiKey)
	req.Header.Set("X-Region", b.region)

	// Send request
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bedrock API returned status %d", resp.StatusCode)
	}

	// Parse response
	var bedrockResp bedrockResponse
	if err := json.NewDecoder(resp.Body).Decode(&bedrockResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Debug logging for the full response
	logger.Debugf("Bedrock API response - Content: %s", bedrockResp.Content)
	logger.Debugf("Bedrock API response - StopReason: %s", bedrockResp.StopReason)
	logger.Debugf("Bedrock API response - ToolCalls count: %d", len(bedrockResp.ToolCalls))
	for i, toolCall := range bedrockResp.ToolCalls {
		logger.Debugf("Bedrock API response - ToolCall[%d]: name=%s, id=%s, input=%s",
			i, toolCall.Name, toolCall.ID, toolCall.Input)
	}

	// Convert to our format
	response := &LLMResponse{
		Content:   bedrockResp.Content,
		ToolCalls: make([]ToolCall, len(bedrockResp.ToolCalls)),
		Finished:  bedrockResp.StopReason == "end_turn",
	}

	for i, toolCall := range bedrockResp.ToolCalls {
		response.ToolCalls[i] = ToolCall{
			ID:        toolCall.ID,
			Name:      toolCall.Name,
			Arguments: toolCall.Input,
		}
	}

	return response, nil
}
