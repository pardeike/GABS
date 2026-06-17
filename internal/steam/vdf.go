package steam

import (
	"fmt"
	"strings"
	"unicode"
)

func parseVDF(data []byte) (map[string]interface{}, error) {
	tokens, err := scanVDFTokens(string(data))
	if err != nil {
		return nil, err
	}
	index := 0
	root, err := parseVDFObject(tokens, &index)
	if err != nil {
		return nil, err
	}
	if index < len(tokens) {
		return nil, fmt.Errorf("unexpected token %q", tokens[index])
	}
	return root, nil
}

func scanVDFTokens(input string) ([]string, error) {
	tokens := make([]string, 0, 32)
	for i := 0; i < len(input); {
		r := rune(input[i])
		if unicode.IsSpace(r) {
			i++
			continue
		}
		if input[i] == '/' && i+1 < len(input) && input[i+1] == '/' {
			i += 2
			for i < len(input) && input[i] != '\n' {
				i++
			}
			continue
		}
		if input[i] == '/' && i+1 < len(input) && input[i+1] == '*' {
			i += 2
			for i+1 < len(input) && !(input[i] == '*' && input[i+1] == '/') {
				i++
			}
			if i+1 >= len(input) {
				return nil, fmt.Errorf("unterminated block comment")
			}
			i += 2
			continue
		}
		if input[i] == '{' || input[i] == '}' {
			tokens = append(tokens, input[i:i+1])
			i++
			continue
		}
		if input[i] == '"' {
			var builder strings.Builder
			closed := false
			i++
			for i < len(input) {
				if input[i] == '"' {
					i++
					tokens = append(tokens, builder.String())
					closed = true
					break
				}
				if input[i] == '\\' && i+1 < len(input) {
					switch input[i+1] {
					case '"', '\\':
						builder.WriteByte(input[i+1])
						i += 2
						continue
					}
				}
				builder.WriteByte(input[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quoted string")
			}
			continue
		}

		start := i
		for i < len(input) && !unicode.IsSpace(rune(input[i])) && input[i] != '{' && input[i] != '}' {
			i++
		}
		tokens = append(tokens, input[start:i])
	}
	return tokens, nil
}

func parseVDFObject(tokens []string, index *int) (map[string]interface{}, error) {
	object := make(map[string]interface{})
	for *index < len(tokens) {
		token := tokens[*index]
		if token == "}" {
			*index = *index + 1
			return object, nil
		}
		if token == "{" {
			return nil, fmt.Errorf("unexpected object start")
		}

		key := token
		*index = *index + 1
		if *index >= len(tokens) {
			return nil, fmt.Errorf("missing value for key %q", key)
		}

		if tokens[*index] == "{" {
			*index = *index + 1
			value, err := parseVDFObject(tokens, index)
			if err != nil {
				return nil, err
			}
			object[key] = value
			continue
		}
		if tokens[*index] == "}" {
			return nil, fmt.Errorf("missing value for key %q", key)
		}
		object[key] = tokens[*index]
		*index = *index + 1
	}
	return object, nil
}

func nestedMap(root map[string]interface{}, key string) (map[string]interface{}, bool) {
	value, ok := root[key]
	if !ok {
		return nil, false
	}
	nested, ok := value.(map[string]interface{})
	return nested, ok
}

func stringValue(root map[string]interface{}, key string) (string, bool) {
	value, ok := root[key]
	if !ok {
		return "", false
	}
	stringValue, ok := value.(string)
	return stringValue, ok
}
