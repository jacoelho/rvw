package config

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const maxTextMateNodes = 100_000

func parseTextMateTheme(contents []byte) (map[SyntaxColor]Color, error) {
	if !utf8.Valid(contents) {
		return nil, errors.New("TextMate theme is not valid UTF-8")
	}
	decoder := xml.NewDecoder(strings.NewReader(string(contents)))
	decoder.Strict = true
	start, err := nextXMLStart(decoder)
	if err != nil {
		return nil, err
	}
	if start.Name.Local != "plist" {
		return nil, errors.New("TextMate theme root is not plist")
	}
	valueStart, err := nextXMLStart(decoder)
	if err != nil {
		return nil, err
	}
	nodes := 0
	value, err := decodePlistValue(decoder, valueStart, 0, &nodes)
	if err != nil {
		return nil, err
	}
	if err := requireXMLEnd(decoder, "plist"); err != nil {
		return nil, err
	}
	if _, err := nextXMLContent(decoder); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("TextMate theme has content after plist")
		}
		return nil, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("TextMate theme plist value is not a dictionary")
	}
	rawSettings, ok := root["settings"].([]any)
	if !ok || len(rawSettings) == 0 {
		return nil, errors.New("TextMate theme has no settings array")
	}
	colors := make(map[SyntaxColor]Color)
	for _, rawEntry := range rawSettings {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			return nil, errors.New("TextMate theme setting is not a dictionary")
		}
		scope, ok := entry["scope"].(string)
		if !ok {
			continue
		}
		settings, ok := entry["settings"].(map[string]any)
		if !ok {
			continue
		}
		foreground, ok := settings["foreground"].(string)
		if !ok || foreground == "" {
			continue
		}
		color, err := validateColor(foreground)
		if err != nil {
			return nil, fmt.Errorf("invalid TextMate foreground: %w", err)
		}
		for selector := range strings.SplitSeq(scope, ",") {
			selector = strings.TrimSpace(selector)
			switch {
			case strings.HasPrefix(selector, "comment"):
				colors[SyntaxComment] = color
			case strings.HasPrefix(selector, "string"):
				colors[SyntaxString] = color
			case strings.HasPrefix(selector, "constant.numeric"):
				colors[SyntaxNumber] = color
			case strings.HasPrefix(selector, "keyword"),
				strings.HasPrefix(selector, "storage"):
				colors[SyntaxKeyword] = color
			}
		}
	}
	return colors, nil
}

func decodePlistValue(
	decoder *xml.Decoder,
	start xml.StartElement,
	depth int,
	nodes *int,
) (any, error) {
	*nodes++
	if *nodes > maxTextMateNodes || depth > 64 {
		return nil, errors.New("TextMate theme structure exceeds its bound")
	}
	switch start.Name.Local {
	case "dict":
		return decodePlistDictionary(decoder, depth, nodes)
	case "array":
		return decodePlistArray(decoder, depth, nodes)
	case "string", "key", "integer", "real", "date", "data":
		return decodeXMLText(decoder, start)
	case "true":
		if err := requireXMLEnd(decoder, "true"); err != nil {
			return nil, err
		}
		return true, nil
	case "false":
		if err := requireXMLEnd(decoder, "false"); err != nil {
			return nil, err
		}
		return false, nil
	default:
		return nil, fmt.Errorf("unsupported TextMate plist element %q", start.Name.Local)
	}
}

func decodePlistDictionary(decoder *xml.Decoder, depth int, nodes *int) (map[string]any, error) {
	result := make(map[string]any)
	for {
		token, err := nextXMLContent(decoder)
		if err != nil {
			return nil, err
		}
		if end, ok := token.(xml.EndElement); ok {
			if end.Name.Local != "dict" {
				return nil, errors.New("TextMate dictionary ended unexpectedly")
			}
			return result, nil
		}
		keyStart, ok := token.(xml.StartElement)
		if !ok || keyStart.Name.Local != "key" {
			return nil, errors.New("TextMate dictionary expected a key")
		}
		key, err := decodeXMLText(decoder, keyStart)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("TextMate dictionary repeats key %q", key)
		}
		valueStart, err := nextXMLStart(decoder)
		if err != nil {
			return nil, err
		}
		result[key], err = decodePlistValue(decoder, valueStart, depth+1, nodes)
		if err != nil {
			return nil, err
		}
	}
}

func decodePlistArray(decoder *xml.Decoder, depth int, nodes *int) ([]any, error) {
	var result []any
	for {
		token, err := nextXMLContent(decoder)
		if err != nil {
			return nil, err
		}
		if end, ok := token.(xml.EndElement); ok {
			if end.Name.Local != "array" {
				return nil, errors.New("TextMate array ended unexpectedly")
			}
			return result, nil
		}
		valueStart, ok := token.(xml.StartElement)
		if !ok {
			return nil, errors.New("TextMate array value is malformed")
		}
		value, err := decodePlistValue(decoder, valueStart, depth+1, nodes)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
}

func nextXMLStart(decoder *xml.Decoder) (xml.StartElement, error) {
	token, err := nextXMLContent(decoder)
	if err != nil {
		return xml.StartElement{}, err
	}
	start, ok := token.(xml.StartElement)
	if !ok {
		return xml.StartElement{}, errors.New("TextMate theme expected an element")
	}
	return start, nil
}

func nextXMLContent(decoder *xml.Decoder) (xml.Token, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if characters, ok := token.(xml.CharData); ok &&
			strings.TrimSpace(string(characters)) == "" {
			continue
		}
		switch token.(type) {
		case xml.ProcInst, xml.Directive, xml.Comment:
			continue
		}
		return token, nil
	}
}

func decodeXMLText(decoder *xml.Decoder, start xml.StartElement) (string, error) {
	var value strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", err
		}
		switch token := token.(type) {
		case xml.CharData:
			value.Write(token)
		case xml.EndElement:
			if token.Name.Local != start.Name.Local {
				return "", errors.New("TextMate text element ended unexpectedly")
			}
			return value.String(), nil
		case xml.StartElement:
			return "", errors.New("TextMate text element contains nested markup")
		}
	}
}

func requireXMLEnd(decoder *xml.Decoder, name string) error {
	token, err := nextXMLContent(decoder)
	if err != nil {
		return err
	}
	end, ok := token.(xml.EndElement)
	if !ok || end.Name.Local != name {
		return fmt.Errorf("TextMate element %q is not terminated", name)
	}
	return nil
}
