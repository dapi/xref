package xrefgo

import (
	"bytes"
	"strings"
)

// scanTemplate extracts Ruby fragments without asking a Ruby or Haml runtime to
// execute the template. Incomplete Ruby control-flow fragments are normal in
// templates, so only malformed template delimiters count as extraction errors.
func scanTemplate(path string, src []byte, models map[string]ModelInfo) ([]Ref, int, int) {
	refs := []Ref{}
	errors, segments := 0, 0
	scan := func(code []byte, line int) {
		if len(bytes.TrimSpace(code)) == 0 {
			return
		}
		found, _ := scanRuby(path, code, models)
		for _, r := range found {
			r.Line += line - 1
			refs = append(refs, r)
		}
		segments++
	}
	if strings.HasSuffix(path, ".erb") {
		pos := 0
		for pos < len(src) {
			start := bytes.Index(src[pos:], []byte("<%"))
			if start < 0 {
				break
			}
			start += pos
			if start+2 < len(src) && src[start+2] == '%' { // escaped literal <%%
				pos = start + 3
				continue
			}
			end := bytes.Index(src[start+2:], []byte("%>"))
			if end < 0 {
				errors++
				break
			}
			end += start + 2
			code := src[start+2 : end]
			if len(code) > 0 && code[0] != '#' {
				if code[0] == '=' {
					code = bytes.TrimLeft(code, "=")
				} else if code[0] == '-' {
					code = code[1:]
				}
				if len(code) > 0 && code[len(code)-1] == '-' {
					code = code[:len(code)-1]
				}
				scan(code, bytes.Count(src[:start], []byte{'\n'})+1)
			}
			pos = end + 2
		}
		return dedup(refs), errors, segments
	}
	lines := bytes.Split(src, []byte{'\n'})
	commentIndent := -1
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := bytes.TrimLeft(line, " \t")
		indent := len(line) - len(trimmed)
		if commentIndent >= 0 {
			if len(trimmed) == 0 || indent > commentIndent {
				continue
			}
			commentIndent = -1
		}
		if bytes.HasPrefix(trimmed, []byte("-#")) || bytes.HasPrefix(trimmed, []byte("/")) {
			commentIndent = indent
			continue
		}
		marker := 0
		switch {
		case bytes.HasPrefix(trimmed, []byte("!=")), bytes.HasPrefix(trimmed, []byte("&=")), bytes.HasPrefix(trimmed, []byte("!~")):
			marker = 2
		case bytes.HasPrefix(trimmed, []byte("=")), bytes.HasPrefix(trimmed, []byte("-")), bytes.HasPrefix(trimmed, []byte("~")):
			marker = 1
		}
		if marker > 0 {
			code := append([]byte(nil), trimmed[marker:]...)
			startLine := i + 1
			for needsRubyContinuation(code) && i+1 < len(lines) {
				i++
				code = append(code, '\n')
				code = append(code, bytes.TrimSpace(lines[i])...)
			}
			scan(code, startLine)
			continue
		}
		// Haml's { ... } attribute hash contains ordinary Ruby expressions.
		lineNumber := i + 1
		hasAttributes := false
		if len(trimmed) > 0 && !bytes.HasPrefix(trimmed, []byte("#{")) && (trimmed[0] == '%' || trimmed[0] == '.' || trimmed[0] == '#') {
			if open := bytes.IndexByte(trimmed, '{'); open >= 0 && bytes.IndexAny(trimmed[:open], " \t") < 0 {
				hasAttributes = true
				block := append([]byte(nil), trimmed[open:]...)
				startLine := i + 1
				for i+1 < len(lines) {
					if _, ok := balancedRuby(block, '{', '}'); ok {
						break
					}
					i++
					block = append(block, '\n')
					block = append(block, lines[i]...)
				}
				if end, ok := balancedRuby(block, '{', '}'); ok {
					scan(block[:end], startLine)
				} else {
					errors++
				}
			}
			if code := hamlInlineCode(trimmed); len(code) > 0 {
				scan(code, lineNumber)
			}
		}
		if hasAttributes {
			continue
		}
		// Ruby interpolation is valid in Haml text and in plain tag attributes.
		for pos := 0; pos < len(trimmed); {
			start := bytes.Index(trimmed[pos:], []byte("#{"))
			if start < 0 {
				break
			}
			start += pos
			end, ok := balancedRuby(trimmed[start+1:], '{', '}')
			if !ok {
				errors++
				break
			}
			scan(trimmed[start+2:start+1+end-1], i+1)
			pos = start + 1 + end
		}
	}
	return dedup(refs), errors, segments
}

// A tag can render a Ruby expression directly: %span= Product.where(...).
// Ignore '=' inside Haml attributes and after the beginning of plain text.
func hamlInlineCode(line []byte) []byte {
	depth := 0
	quote := byte(0)
	for i, c := range line {
		if quote != 0 {
			if c == quote && (i == 0 || line[i-1] != '\\') {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '{', '(':
			depth++
		case '}', ')':
			depth--
		case '=':
			if depth == 0 {
				return line[i+1:]
			}
		case ' ', '\t':
			if depth == 0 {
				return nil
			}
		}
	}
	return nil
}

// balancedRuby returns the byte offset just after the matching closing brace.
// Quotes and escapes are skipped so braces in ordinary strings do not terminate
// a fragment. This is a delimiter scanner, not a full Ruby lexer.
func balancedRuby(src []byte, open, close byte) (int, bool) {
	depth, quote := 0, byte(0)
	escape := false
	for i, c := range src {
		if quote != 0 {
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
		} else if c == open {
			depth++
		} else if c == close {
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

func needsRubyContinuation(code []byte) bool {
	s := strings.TrimSpace(string(code))
	if strings.HasSuffix(s, ",") || strings.HasSuffix(s, "\\") {
		return true
	}
	stack := []byte{}
	quote, escape := byte(0), false
	for _, c := range code {
		if quote != 0 {
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(', '[', '{':
			stack = append(stack, c)
		case ')', ']', '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return len(stack) > 0
}
