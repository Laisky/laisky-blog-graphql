package rag

import (
	"strings"
	"unicode"
)

// ChunkFragment represents a cleaned portion of the input materials.
type ChunkFragment struct {
	Index   int
	Text    string
	Cleaned string
	Tokens  []string
}

// Chunker splits materials into bounded fragments.
type Chunker interface {
	Split(materials string, maxChars int) []ChunkFragment
}

// ParagraphChunker implements a newline-aware splitter.
type ParagraphChunker struct{}

// Split divides the materials into roughly paragraph-sized fragments while enforcing the maxChars bound.
func (ParagraphChunker) Split(materials string, maxChars int) []ChunkFragment {
	normalized := normalizeWhitespace(materials)
	blocks := strings.Split(normalized, "\n\n")
	fragments := make([]ChunkFragment, 0, len(blocks))
	idx := 0
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		segments := splitBlock(block, maxChars)
		for _, segment := range segments {
			cleaned := normalizeWhitespace(segment)
			tokens := tokenize(cleaned)
			fragments = append(fragments, ChunkFragment{
				Index:   idx,
				Text:    segment,
				Cleaned: cleaned,
				Tokens:  tokens,
			})
			idx++
		}
	}
	return fragments
}

func splitBlock(block string, maxChars int) []string {
	if maxChars <= 0 || len(block) <= maxChars {
		return []string{block}
	}

	sentences := strings.Split(block, ". ")
	segments := make([]string, 0)
	var current strings.Builder
	for _, sentence := range sentences {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			continue
		}
		if current.Len()+len(sentence)+1 > maxChars {
			if current.Len() > 0 {
				segments = append(segments, strings.TrimSpace(current.String()))
				current.Reset()
			}
			if len(sentence) > maxChars {
				for len(sentence) > maxChars {
					segments = append(segments, sentence[:maxChars])
					sentence = sentence[maxChars:]
				}
			}
			current.WriteString(sentence)
		} else {
			if current.Len() > 0 {
				current.WriteString(" ")
			}
			current.WriteString(sentence)
		}
	}
	if current.Len() > 0 {
		segments = append(segments, strings.TrimSpace(current.String()))
	}
	if len(segments) == 0 {
		return []string{block[:maxChars]}
	}
	return segments
}

func normalizeWhitespace(input string) string {
	var b strings.Builder
	b.Grow(len(input))
	lastSpace := false
	for _, r := range input {
		if unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
			continue
		}
		lastSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
