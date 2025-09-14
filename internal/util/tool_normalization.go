package util

import (
	"regexp"
	"strings"
)

// ToolNameNormalizationResult contains both normalized and original tool names
type ToolNameNormalizationResult struct {
	// NormalizedName is the name safe for use with OpenAI API (underscores, limited length)
	NormalizedName string
	// OriginalName is the original MCP tool name (with dots)
	OriginalName string
	// WasNormalized indicates if any normalization was applied
	WasNormalized bool
}

// NormalizeToolNameForOpenAI normalizes an MCP tool name to be OpenAI API compatible
// - Replaces dots (.) with underscores (_) since OpenAI doesn't allow dots
// - Restricts length to maxLength characters (typically 64 for OpenAI)
// - Keeps only alphanumeric characters, underscores, and hyphens
// - Preserves the original name for reference
func NormalizeToolNameForOpenAI(originalName string, maxLength int) ToolNameNormalizationResult {
	if maxLength <= 0 {
		maxLength = 64 // OpenAI default limit
	}

	normalized := originalName
	wasNormalized := false

	// Step 1: Replace dots with underscores for OpenAI compatibility
	if strings.Contains(normalized, ".") {
		normalized = strings.ReplaceAll(normalized, ".", "_")
		wasNormalized = true
	}

	// Step 2: Replace other problematic characters with underscores
	// Keep only alphanumeric, underscores, and hyphens (OpenAI requirement)
	cleanPattern := regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	cleaned := cleanPattern.ReplaceAllString(normalized, "_")
	if cleaned != normalized {
		normalized = cleaned
		wasNormalized = true
	}

	// Step 3: Remove consecutive underscores to make names cleaner
	multiUnderscorePattern := regexp.MustCompile(`_{2,}`)
	deduped := multiUnderscorePattern.ReplaceAllString(normalized, "_")
	if deduped != normalized {
		normalized = deduped
		wasNormalized = true
	}

	// Step 4: Trim leading/trailing underscores
	trimmed := strings.Trim(normalized, "_")
	if trimmed != normalized {
		normalized = trimmed
		wasNormalized = true
	}

	// Step 5: Ensure minimum length of 1
	if len(normalized) == 0 {
		normalized = "tool"
		wasNormalized = true
	}

	// Step 6: Truncate to maxLength if necessary
	if len(normalized) > maxLength {
		// Try to truncate at a reasonable boundary (underscore) if possible
		truncated := normalized[:maxLength]
		if lastUnderscore := strings.LastIndex(truncated, "_"); lastUnderscore > maxLength/2 {
			// If there's an underscore in the second half, truncate there for better readability
			truncated = truncated[:lastUnderscore]
		}
		normalized = truncated
		wasNormalized = true
	}

	// Final validation: ensure the result starts with a letter (OpenAI requirement)
	if len(normalized) > 0 && !regexp.MustCompile(`^[a-zA-Z]`).MatchString(normalized) {
		normalized = "tool_" + normalized
		wasNormalized = true
		// Re-check length after adding prefix
		if len(normalized) > maxLength {
			normalized = normalized[:maxLength]
		}
	}

	return ToolNameNormalizationResult{
		NormalizedName: normalized,
		OriginalName:   originalName,
		WasNormalized:  wasNormalized,
	}
}

// ValidateOpenAIToolName checks if a tool name meets OpenAI API requirements
func ValidateOpenAIToolName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}

	// Must start with a letter
	if !regexp.MustCompile(`^[a-zA-Z]`).MatchString(name) {
		return false
	}

	// Only letters, numbers, underscores, and hyphens allowed
	return regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(name)
}

// NormalizeToolNameBasic performs basic MCP tool name normalization (existing logic)
// This converts slashes to dots for reverse domain notation
func NormalizeToolNameBasic(toolName string) string {
	// Convert slashes to dots to follow reverse domain notation
	result := strings.ReplaceAll(toolName, "/", ".")
	return result
}