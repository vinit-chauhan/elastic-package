// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package providers

import "strings"

// maskAPIKey masks an API key for secure logging
func maskAPIKey(apiKey string) string {
	if len(apiKey) <= 12 {
		return strings.Repeat("*", len(apiKey))
	}
	return apiKey[:6] + strings.Repeat("*", len(apiKey)-6)
}
