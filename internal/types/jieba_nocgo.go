//go:build !cgo

package types

import (
	"strings"
	"unicode"
)

type fallbackJiebaTokenizer struct{}

func newJieba() JiebaTokenizer {
	return fallbackJiebaTokenizer{}
}

func (fallbackJiebaTokenizer) Cut(sentence string, hmm bool) []string {
	return fallbackTokenize(sentence)
}

func (fallbackJiebaTokenizer) CutForSearch(sentence string, hmm bool) []string {
	return fallbackTokenize(sentence)
}

func fallbackTokenize(sentence string) []string {
	var tokens []string
	var ascii strings.Builder
	flushASCII := func() {
		if ascii.Len() == 0 {
			return
		}
		tokens = append(tokens, ascii.String())
		ascii.Reset()
	}
	for _, r := range sentence {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			flushASCII()
			continue
		}
		if r <= unicode.MaxASCII {
			ascii.WriteRune(unicode.ToLower(r))
			continue
		}
		flushASCII()
		tokens = append(tokens, string(r))
	}
	flushASCII()
	return tokens
}
