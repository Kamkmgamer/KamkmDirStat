package search

import (
	"strings"
	"unicode/utf8"
)

// token is one word of the query, in lower case. Every word must match.
type token struct {
	b    []byte
	wide bool // has bytes outside ASCII
}

func parseTokens(text string) []token {
	var toks []token
	for _, w := range strings.Fields(strings.ToLower(text)) {
		t := token{b: []byte(w)}
		for i := 0; i < len(w); i++ {
			if w[i] >= utf8.RuneSelf {
				t.wide = true
				break
			}
		}
		toks = append(toks, t)
	}
	return toks
}

// Scores, close to fzf's: each matched character earns scoreMatch, gaps cost, and
// characters that start a word or continue a run earn bonuses.
const (
	scoreMatch       = 16
	scoreGapStart    = -3
	scoreGapExtend   = -1
	bonusBoundary    = 8 // after a separator such as a space, dot or dash
	bonusCamel       = 7 // an upper case letter after a lower case one, or a digit after a letter
	bonusStart       = 10
	bonusConsecutive = 5
	bonusExact       = 24 // the word appears as written
)

// matchAll scores every token against one name and adds their scores. s is name, or name in
// lower case when a token has letters outside ASCII. With pos, the matched byte offsets are
// appended to it.
func matchAll(name, s []byte, toks []token, pos *[]int) (int, bool) {
	total := 0
	for _, t := range toks {
		score, ok := matchToken(name, s, t.b, pos)
		if !ok {
			return 0, false
		}
		total += score
	}
	return total, true
}

func lowerASCII(c byte) byte {
	if c-'A' < 26 {
		return c + 'a' - 'A'
	}
	return c
}

func isSeparator(c byte) bool {
	switch c {
	case ' ', '_', '-', '.', '(', ')', '[', ']', '{', '}', '/', '\\', ',', '+', '&', '\'', '~', '@', '#', '=':
		return true
	}
	return false
}

// bonusAt is the bonus for matching the character at i of s. name supplies letter case for
// camel case words when it lines up with s.
func bonusAt(name, s []byte, i int) int {
	if i == 0 {
		return bonusStart
	}
	prev, cur := s[i-1], s[i]
	if isSeparator(prev) {
		return bonusBoundary
	}
	if len(name) == len(s) {
		p, c := name[i-1], name[i]
		if p-'a' < 26 && c-'A' < 26 {
			return bonusCamel
		}
	}
	if cur-'0' < 10 && prev-'0' >= 10 {
		return bonusCamel
	}
	return 0
}

// matchToken finds tok's letters in order in s. It first finds the shortest window that
// ends at the earliest possible place, as fzf's fast algorithm does, then prefers an exact
// occurrence of the whole word when there is one.
func matchToken(name, s, tok []byte, pos *[]int) (int, bool) {
	n, m := len(s), len(tok)
	if m == 0 {
		return 0, true
	}
	if m > n {
		return 0, false
	}
	pi, end := 0, -1
	for i := 0; i < n; i++ {
		if lowerASCII(s[i]) == tok[pi] {
			pi++
			if pi == m {
				end = i + 1
				break
			}
		}
	}
	if end < 0 {
		return 0, false
	}
	start := 0
	pi = m - 1
	for i := end - 1; i >= 0; i-- {
		if lowerASCII(s[i]) == tok[pi] {
			pi--
			if pi < 0 {
				start = i
				break
			}
		}
	}
	best, bestAt := scoreWindow(name, s, tok, start, end, nil), -1
	// An exact occurrence, preferring one at a word start, usually beats a scattered match.
	for i := start; i+m <= n; i++ {
		if lowerASCII(s[i]) != tok[0] || !hasAt(s, tok, i) {
			continue
		}
		score := scoreWindow(name, s, tok, i, i+m, nil) + bonusExact
		if score > best {
			best, bestAt = score, i
		}
		if bonusAt(name, s, i) > 0 {
			break
		}
	}
	if pos != nil {
		if bestAt >= 0 {
			for k := range m {
				*pos = append(*pos, bestAt+k)
			}
		} else {
			scoreWindow(name, s, tok, start, end, pos)
		}
	}
	return best, true
}

func hasAt(s, tok []byte, i int) bool {
	for k := 1; k < len(tok); k++ {
		if lowerASCII(s[i+k]) != tok[k] {
			return false
		}
	}
	return true
}

// scoreWindow matches tok from the left of s[start:end] and scores it.
func scoreWindow(name, s, tok []byte, start, end int, pos *[]int) int {
	score, pi, last := 0, 0, -1
	for i := start; i < end && pi < len(tok); i++ {
		if lowerASCII(s[i]) != tok[pi] {
			continue
		}
		b := bonusAt(name, s, i)
		if pi == 0 {
			b *= 2
		}
		if last >= 0 {
			if gap := i - last - 1; gap > 0 {
				score += scoreGapStart + scoreGapExtend*(gap-1)
			} else {
				b = max(b, bonusConsecutive)
			}
		}
		score += scoreMatch + b
		if pos != nil {
			*pos = append(*pos, i)
		}
		last = i
		pi++
	}
	return score
}
