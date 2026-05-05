# Strict-Safe MCP Tool Name Normalization

GABS normalizes public MCP tool names to a strict-safe subset accepted by
OpenAI, Claude variants, and other clients that reject dots or slashes in tool
names.

> **Quick Start:** Add this feature to your config as shown in the [Configuration Guide](CONFIGURATION.md). For AI integration setup, see the [AI Integration Guide](INTEGRATION.md).

## Problem

Several AI clients have strict requirements for tool names:
- Must be 1-64 characters long
- Can only contain letters, numbers, underscores, and hyphens
- Cannot contain dots (`.`) or slashes (`/`) even when those are valid in a
  protocol-specific layer

This causes issues when MCP tools with dotted names such as
`minecraft.inventory.get` or GABP names such as `inventory/get` are advertised
directly to those clients.

## Solution

GABS now includes configurable tool name normalization that:
1. **Replaces unsafe separators with underscores** for client compatibility
2. **Enforces length limits** (default: 64 characters)
3. **Preserves original names** in tool metadata and descriptions
4. **Maintains backward compatibility** by accepting old dotted and canonical
   slash aliases at call time

## Configuration

Add the `toolNormalization` section to your GABS config file:

The top-level `"version"` field shown here is the GABS config schema version,
not the GABP protocol version.

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

- **`enableOpenAINormalization`** (boolean): Enable/disable strict-safe MCP name normalization (default: `true` when `toolNormalization` is omitted)
- **`maxToolNameLength`** (integer): Maximum length for tool names (default: `64`)
- **`preserveOriginalName`** (boolean): Store original name in metadata and description (default: `true`)

## Examples

With normalization enabled:

| Original MCP Name | Normalized Name | OpenAI Valid |
|-------------------|-----------------|--------------|
| `minecraft.inventory.get` | `minecraft_inventory_get` | ✅ |
| `rimworld.crafting.build` | `rimworld_crafting_build` | ✅ |
| `rimworld/rimbridge/ping` | `rimworld_rimbridge_ping` | ✅ |
| `game.player@stats#get!` | `game_player_stats_get` | ✅ |
| `very.long.tool.name.that.exceeds.limit` | `very_long_tool_name_that_exceeds` | ✅ |
| `123.invalid.start` | `tool_123_invalid_start` | ✅ |

## Original Name Preservation

When normalization is applied and `preserveOriginalName` is enabled:

1. **Metadata**: Original name stored in `_meta.originalName`
2. **Description**: Original name appended to description (e.g., "Get inventory items (Original: minecraft.inventory.get)")

Mirrored game tools also include their canonical GABP name in metadata as
`_meta.gabpName`, for example `rimbridge/ping`.

## Backward Compatibility

- `tools/list` advertises strict-safe names by default.
- Existing dotted calls such as `games.call_tool` remain accepted as aliases.
- `games_call_tool` accepts strict-safe discovered names, qualified dotted names,
  and qualified slash names.
- Strict-safe names are descriptor-resolved. GABS does not guess whether an
  underscore should become a slash.

## Implementation Details

The normalization process:

1. **Replace unsafe separators** with underscores: `minecraft.inventory.get` -> `minecraft_inventory_get`
2. **Clean special characters**: Replace invalid characters with underscores
3. **Remove consecutive underscores**: `tool___name` → `tool_name`
4. **Trim underscores**: Remove leading/trailing underscores
5. **Ensure minimum length**: Add "tool" prefix if empty after cleaning
6. **Enforce length limit**: Truncate at word boundaries when possible
7. **Validate start character**: Add `tool_` prefix when needed for clients that require a letter

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
