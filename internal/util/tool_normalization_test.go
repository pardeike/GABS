package util

import (
	"testing"
)

func TestNormalizeToolNameForOpenAI(t *testing.T) {
	testCases := []struct {
		name           string
		input          string
		maxLength      int
		expectedOutput string
		expectedNormalized bool
	}{
		{
			name:               "SimpleDotReplacement",
			input:              "minecraft.inventory.get",
			maxLength:          64,
			expectedOutput:     "minecraft_inventory_get",
			expectedNormalized: true,
		},
		{
			name:               "ComplexGameTool",
			input:              "rimworld.crafting.build",
			maxLength:          64,
			expectedOutput:     "rimworld_crafting_build",
			expectedNormalized: true,
		},
		{
			name:               "AlreadyNormalized",
			input:              "simple_tool_name",
			maxLength:          64,
			expectedOutput:     "simple_tool_name",
			expectedNormalized: false,
		},
		{
			name:               "HyphensPreserved",
			input:              "game-with-hyphens.tool-name",
			maxLength:          64,
			expectedOutput:     "game-with-hyphens_tool-name",
			expectedNormalized: true,
		},
		{
			name:               "LongNameTruncation",
			input:              "very.long.tool.name.that.exceeds.the.maximum.length.allowed.by.openai",
			maxLength:          64,
			expectedOutput:     "very_long_tool_name_that_exceeds_the_maximum_length_allowed_by",
			expectedNormalized: true,
		},
		{
			name:               "LongNameTruncationAtUnderscore",
			input:              "minecraft.inventory.get.all.items.from.player.backpack.detailed",
			maxLength:          50,
			expectedOutput:     "minecraft_inventory_get_all_items_from_player",
			expectedNormalized: true,
		},
		{
			name:               "SpecialCharacterCleaning",
			input:              "tool.with@special#chars!",
			maxLength:          64,
			expectedOutput:     "tool_with_special_chars",
			expectedNormalized: true,
		},
		{
			name:               "ConsecutiveUnderscores",
			input:              "tool...with....dots",
			maxLength:          64,
			expectedOutput:     "tool_with_dots",
			expectedNormalized: true,
		},
		{
			name:               "LeadingTrailingDots",
			input:              ".minecraft.inventory.",
			maxLength:          64,
			expectedOutput:     "minecraft_inventory",
			expectedNormalized: true,
		},
		{
			name:               "EmptyAfterCleaning",
			input:              "...",
			maxLength:          64,
			expectedOutput:     "tool",
			expectedNormalized: true,
		},
		{
			name:               "StartsWithNumber",
			input:              "123.invalid.start",
			maxLength:          64,
			expectedOutput:     "tool_123_invalid_start",
			expectedNormalized: true,
		},
		{
			name:               "StartsWithUnderscore",
			input:              "_invalid.start",
			maxLength:          64,
			expectedOutput:     "invalid_start",
			expectedNormalized: true,
		},
		{
			name:               "ShortMaxLength",
			input:              "minecraft.inventory.get",
			maxLength:          10,
			expectedOutput:     "minecraft",
			expectedNormalized: true,
		},
		{
			name:               "SlashConversion",
			input:              "inventory/get/all",
			maxLength:          64,
			expectedOutput:     "inventory_get_all",
			expectedNormalized: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := NormalizeToolNameForOpenAI(tc.input, tc.maxLength)

			if result.NormalizedName != tc.expectedOutput {
				t.Errorf("Expected normalized name '%s', got '%s'", tc.expectedOutput, result.NormalizedName)
			}

			if result.OriginalName != tc.input {
				t.Errorf("Expected original name '%s', got '%s'", tc.input, result.OriginalName)
			}

			if result.WasNormalized != tc.expectedNormalized {
				t.Errorf("Expected WasNormalized to be %v, got %v", tc.expectedNormalized, result.WasNormalized)
			}

			// Ensure all normalized names pass OpenAI validation
			if !ValidateOpenAIToolName(result.NormalizedName) {
				t.Errorf("Normalized name '%s' failed OpenAI validation", result.NormalizedName)
			}

			t.Logf("✓ '%s' -> '%s' (normalized: %v)", tc.input, result.NormalizedName, result.WasNormalized)
		})
	}
}

func TestValidateOpenAIToolName(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "ValidSimple",
			input:    "simple_tool",
			expected: true,
		},
		{
			name:     "ValidWithHyphens",
			input:    "tool-with-hyphens",
			expected: true,
		},
		{
			name:     "ValidWithNumbers",
			input:    "tool123",
			expected: true,
		},
		{
			name:     "ValidComplexNormalized",
			input:    "minecraft_inventory_get",
			expected: true,
		},
		{
			name:     "InvalidWithDots",
			input:    "minecraft.inventory.get",
			expected: false,
		},
		{
			name:     "InvalidStartsWithNumber",
			input:    "123tool",
			expected: false,
		},
		{
			name:     "InvalidStartsWithUnderscore",
			input:    "_tool",
			expected: false,
		},
		{
			name:     "InvalidStartsWithHyphen",
			input:    "-tool",
			expected: false,
		},
		{
			name:     "InvalidTooLong",
			input:    "this_is_a_very_long_tool_name_that_exceeds_the_sixty_four_character_limit_imposed_by_openai",
			expected: false,
		},
		{
			name:     "InvalidEmpty",
			input:    "",
			expected: false,
		},
		{
			name:     "InvalidSpecialChars",
			input:    "tool@with#special!chars",
			expected: false,
		},
		{
			name:     "ValidAtMaxLength",
			input:    "a123456789012345678901234567890123456789012345678901234567890123", // 64 chars
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := ValidateOpenAIToolName(tc.input)
			if result != tc.expected {
				t.Errorf("Expected validation result %v for '%s', got %v", tc.expected, tc.input, result)
			}
			t.Logf("✓ '%s' validation: %v (length: %d)", tc.input, result, len(tc.input))
		})
	}
}

func TestNormalizeToolNameBasic(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "SlashesToDots",
			input:    "inventory/get",
			expected: "inventory.get",
		},
		{
			name:     "MultipleSlashes",
			input:    "world/blocks/place",
			expected: "world.blocks.place",
		},
		{
			name:     "NoSlashes",
			input:    "simple.tool",
			expected: "simple.tool",
		},
		{
			name:     "MixedSeparators",
			input:    "player.stats/get",
			expected: "player.stats.get",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := NormalizeToolNameBasic(tc.input)
			if result != tc.expected {
				t.Errorf("Expected '%s', got '%s'", tc.expected, result)
			}
			t.Logf("✓ '%s' -> '%s'", tc.input, result)
		})
	}
}

// Test edge cases and boundary conditions
func TestNormalizationEdgeCases(t *testing.T) {
	t.Run("ZeroMaxLength", func(t *testing.T) {
		result := NormalizeToolNameForOpenAI("minecraft.inventory.get", 0)
		// Should use default length (64)
		if len(result.NormalizedName) > 64 {
			t.Errorf("Expected max length to default to 64, but got length %d", len(result.NormalizedName))
		}
	})

	t.Run("NegativeMaxLength", func(t *testing.T) {
		result := NormalizeToolNameForOpenAI("minecraft.inventory.get", -5)
		// Should use default length (64)
		if len(result.NormalizedName) > 64 {
			t.Errorf("Expected max length to default to 64, but got length %d", len(result.NormalizedName))
		}
	})

	t.Run("VeryShortMaxLength", func(t *testing.T) {
		result := NormalizeToolNameForOpenAI("minecraft.inventory.get", 1)
		// Should produce a valid 1-character name
		if len(result.NormalizedName) != 1 {
			t.Errorf("Expected length 1, got length %d: '%s'", len(result.NormalizedName), result.NormalizedName)
		}
		if !ValidateOpenAIToolName(result.NormalizedName) {
			t.Errorf("Result should still be valid OpenAI tool name: '%s'", result.NormalizedName)
		}
	})
}