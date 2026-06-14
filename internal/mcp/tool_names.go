package mcp

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"unicode"
)

const (
	toolMetaGABPName          = "gabpName"
	toolMetaQualifiedGABPName = "qualifiedGABPName"
	toolMetaLegacyName        = "legacyName"
	toolMetaAliases           = "aliases"
	toolMetaTags              = "tags"
)

type gameToolAlias struct {
	GameID  string
	GABP    string
	Exposed string
}

func safeMCPToolName(gameID, gabpName string, maxLength int) string {
	base := strings.Builder{}
	base.Grow(len(gameID) + 1 + len(gabpName))

	lastUnderscore := false
	for _, r := range gameID + "_" + gabpName {
		valid := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' ||
			r == '_'
		if valid {
			base.WriteRune(r)
			lastUnderscore = false
			continue
		}

		if !lastUnderscore {
			base.WriteByte('_')
			lastUnderscore = true
		}
	}

	name := strings.Trim(base.String(), "_")
	if name == "" {
		name = "tool"
	}
	if first := rune(name[0]); !unicode.IsLetter(first) && first != '_' {
		name = "tool_" + name
	}
	if maxLength <= 0 {
		maxLength = 64
	}
	if len(name) <= maxLength {
		return name
	}

	hash := stableToolNameHash(gameID + "/" + gabpName)
	suffix := "_" + hash
	prefixLength := maxLength - len(suffix)
	if prefixLength <= 0 {
		return hash[:maxLength]
	}
	return strings.TrimRight(name[:prefixLength], "_-") + suffix
}

func stableToolNameHash(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])[:8]
}

func safeMCPToolNameWithCollisionSuffix(gameID, gabpName string, maxLength int) string {
	base := safeMCPToolName(gameID, gabpName, maxLength)
	hash := stableToolNameHash(gameID + "/" + gabpName)
	suffix := "_" + hash
	if len(base)+len(suffix) <= maxLength {
		return base + suffix
	}
	prefixLength := maxLength - len(suffix)
	if prefixLength <= 0 {
		return hash[:maxLength]
	}
	return strings.TrimRight(base[:prefixLength], "_-") + suffix
}

func legacyMCPToolName(gameID, gabpName string) string {
	return gameID + "." + strings.ReplaceAll(gabpName, "/", ".")
}

func localLegacyMCPToolName(gabpName string) string {
	return strings.ReplaceAll(gabpName, "/", ".")
}

func qualifiedGABPToolName(gameID, gabpName string) string {
	return gameID + "/" + gabpName
}

func canonicalGABPToolName(name string) string {
	return strings.ReplaceAll(strings.TrimSpace(name), ".", "/")
}

func gabpToolNameFromDelimitedRequest(gameID, requested string) []string {
	requested = strings.TrimSpace(requested)
	if requested == "" || !strings.ContainsAny(requested, "./") {
		return nil
	}

	candidates := make([]string, 0, 2)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		value = strings.ReplaceAll(value, ".", "/")
		for _, existing := range candidates {
			if existing == value {
				return
			}
		}
		candidates = append(candidates, value)
	}

	add(requested)
	for _, prefix := range []string{gameID + ".", gameID + "/"} {
		if strings.HasPrefix(requested, prefix) {
			add(strings.TrimPrefix(requested, prefix))
		}
	}

	return candidates
}

func toolMetaString(tool Tool, key string) string {
	if tool.Meta == nil {
		return ""
	}
	value, ok := tool.Meta[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func toolMetaStringSlice(tool Tool, key string) []string {
	if tool.Meta == nil {
		return nil
	}
	raw, ok := tool.Meta[key]
	if !ok {
		return nil
	}

	switch typed := raw.(type) {
	case []string:
		return typed
	case []interface{}:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
				values = append(values, strings.TrimSpace(value))
			}
		}
		return values
	default:
		return nil
	}
}

func toolNameAliases(gameID string, tool Tool) []string {
	values := make([]string, 0, 8)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range values {
			if existing == value {
				return
			}
		}
		values = append(values, value)
	}
	addWithGamePrefixStripped := func(value string) {
		add(value)
		for _, prefix := range []string{gameID + ".", gameID + "/"} {
			if gameID != "" && strings.HasPrefix(value, prefix) {
				add(strings.TrimPrefix(value, prefix))
			}
		}
	}

	addWithGamePrefixStripped(tool.Name)
	if gabpName := toolMetaString(tool, toolMetaGABPName); gabpName != "" {
		add(gabpName)
		add(localLegacyMCPToolName(gabpName))
		add(legacyMCPToolName(gameID, gabpName))
		add(qualifiedGABPToolName(gameID, gabpName))
	}
	addWithGamePrefixStripped(toolMetaString(tool, toolMetaQualifiedGABPName))
	addWithGamePrefixStripped(toolMetaString(tool, toolMetaLegacyName))
	addWithGamePrefixStripped(toolMetaString(tool, "originalName"))
	for _, alias := range toolMetaStringSlice(tool, toolMetaAliases) {
		add(alias)
	}

	return values
}
