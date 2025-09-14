# OpenAI Tool Name Normalization

GABS now supports automatic normalization of MCP tool names to be compatible with OpenAI's API requirements.

> **Quick Start:** Add this feature to your config as shown in the [Configuration Guide](CONFIGURATION.md). For AI integration setup, see the [AI Integration Guide](INTEGRATION.md).

## Problem

OpenAI's API has strict requirements for tool names:
- Must be 1-64 characters long
- Must start with a letter
- Can only contain letters, numbers, underscores, and hyphens
- **Cannot contain dots (.) which are commonly used in MCP tool names**

This causes issues when MCP tools with dotted names (like `minecraft.inventory.get`) are registered directly with OpenAI's API, resulting in validation errors.

## Solution

GABS now includes configurable tool name normalization that:
1. **Replaces dots (.) with underscores (_)** for OpenAI compatibility
2. **Enforces length limits** (default: 64 characters)
3. **Preserves original names** in tool metadata and descriptions
4. **Maintains backward compatibility** - disabled by default

## Configuration

Add the `toolNormalization` section to your GABS config file:

```json
{
  "version": "1.0",
  "toolNormalization": {
    "enableOpenAINormalization": true,
    "maxToolNameLength": 64,
    "preserveOriginalName": true
  },
  "games": {
    // ... your game configurations
  }
}
```

### Configuration Options

- **`enableOpenAINormalization`** (boolean): Enable/disable OpenAI-compatible normalization (default: `false`)
- **`maxToolNameLength`** (integer): Maximum length for tool names (default: `64`)
- **`preserveOriginalName`** (boolean): Store original name in metadata and description (default: `true`)

## Examples

With OpenAI normalization enabled:

| Original MCP Name | Normalized Name | OpenAI Valid |
|-------------------|-----------------|--------------|
| `minecraft.inventory.get` | `minecraft_inventory_get` | ✅ |
| `rimworld.crafting.build` | `rimworld_crafting_build` | ✅ |
| `game.player@stats#get!` | `game_player_stats_get` | ✅ |
| `very.long.tool.name.that.exceeds.limit` | `very_long_tool_name_that_exceeds` | ✅ |
| `123.invalid.start` | `tool_123_invalid_start` | ✅ |

## Original Name Preservation

When normalization is applied and `preserveOriginalName` is enabled:

1. **Metadata**: Original name stored in `_meta.originalName`
2. **Description**: Original name appended to description (e.g., "Get inventory items (Original: minecraft.inventory.get)")

This allows clients to display meaningful names to users while using OpenAI-compatible names for API calls.

## Backward Compatibility

- **Default behavior unchanged**: Normalization is disabled by default
- **Existing configurations continue to work** without modification
- **Legacy `RegisterTool()` method** continues to work without normalization
- **New `RegisterToolWithConfig()`** method supports normalization when enabled

## Implementation Details

The normalization process:

1. **Replace dots** with underscores: `minecraft.inventory.get` → `minecraft_inventory_get`
2. **Clean special characters**: Replace invalid characters with underscores
3. **Remove consecutive underscores**: `tool___name` → `tool_name`
4. **Trim underscores**: Remove leading/trailing underscores
5. **Ensure minimum length**: Add "tool" prefix if empty after cleaning
6. **Enforce length limit**: Truncate at word boundaries when possible
7. **Validate start character**: Add "tool_" prefix if doesn't start with letter

## Automatic Detection

GABS automatically detects when normalization might be needed and logs the transformations:

```
DEBUG normalized tool name for OpenAI compatibility original=minecraft.inventory.get normalized=minecraft_inventory_get
```

This helps with debugging and understanding what transformations are being applied.

## Testing

Comprehensive test coverage includes:
- Basic dot replacement scenarios
- Complex special character handling
- Length limit enforcement
- Edge cases (empty names, invalid starts)
- Integration with MCP server registration
- Backward compatibility verification

Run tests with:
```bash
go test ./internal/util/... -run TestNormalizeToolNameForOpenAI
go test ./internal/mcp/... -run TestOpenAI
```