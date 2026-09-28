package voiceout

import (
	"os"
	"regexp"
	"strings"
)

// Regexes are precompiled once: they run on every phrase the daemon speaks.
var (
	codeBlock  = regexp.MustCompile("(?s)```.*?```")
	urlRE      = regexp.MustCompile(`https?://\S+`)
	inlineCode = regexp.MustCompile("`([^`]*)`")
	heading    = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s*`)
	bullet     = regexp.MustCompile(`(?m)^\s*[-*+]\s+`)
	strong     = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	space      = regexp.MustCompile(`[ \t\r\n]+`)
)

// cleanMarkdown turns assistant markdown into text that sounds right when
// read aloud. A synthesizer pronounces "#", "```" and "https://" literally,
// so markup, code blocks, URLs, tables and shell noise are removed first.
// Short inline code survives — hearing the word "echo" is useful, hearing
// "tilde slash dot config" is not.
func cleanMarkdown(text string) string {
	text = codeBlock.ReplaceAllString(text, " ")
	text = urlRE.ReplaceAllString(text, " ")
	text = inlineCode.ReplaceAllStringFunc(text, func(s string) string {
		v := s[1 : len(s)-1]
		if strings.ContainsAny(v, "/\\~= ") || strings.Contains(v, "--") {
			return " "
		}
		return v
	})
	text = heading.ReplaceAllString(text, "")
	text = bullet.ReplaceAllString(text, "")
	text = strong.ReplaceAllString(text, "$1")
	lines := make([]string, 0)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "|") || strings.HasPrefix(line, "$ ") ||
			strings.HasPrefix(line, "#!") || strings.HasPrefix(line, "sudo ") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(space.ReplaceAllString(strings.Join(lines, "\n"), " "))
}

// applyLexicon substitutes "written = spoken" pairs from the profile's
// lexicon file, one per line, so names like "Claude" come out as "Клауд".
// A missing or unreadable file is a no-op on purpose: speech must never
// fail because a dictionary symlink went stale.
func applyLexicon(text, lexiconPath string) string {
	if lexiconPath == "" || text == "" {
		return text
	}
	rules, err := os.ReadFile(lexiconPath)
	if err != nil {
		return text
	}
	for _, line := range strings.Split(string(rules), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" {
			text = strings.ReplaceAll(text, strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}
	return strings.TrimSpace(text)
}

// splitStages groups sentences into stages of at most limit runes each.
// Sentences are never cut mid-way; a single sentence longer than limit gets
// its own (oversized) stage rather than being split mid-word.
func splitStages(text string, limit int) [][]string {
	if limit <= 0 {
		limit = maxStageChars
	}
	var result [][]string
	for _, para := range strings.Split(text, "\n\n") {
		var stage []string
		n := 0
		for _, sentence := range splitSentences(strings.TrimSpace(para)) {
			if sentence == "" {
				continue
			}
			// Budget the joined text, spaces included: the synthesizer
			// receives strings.Join(stage, " "), so that is the length
			// that must stay within the limit.
			cost := len([]rune(sentence))
			if n > 0 {
				cost++
			}
			if n > 0 && n+cost > limit {
				result = append(result, stage)
				stage, n = nil, 0
				cost--
			}
			stage = append(stage, sentence)
			n += cost
		}
		if len(stage) > 0 {
			result = append(result, stage)
		}
	}
	return result
}

// splitSentences cuts on ".", "!", "?" and "…" followed by whitespace or
// end of text. It is deliberately naive — abbreviations like "Dr." split —
// because a wrong split only changes where a pause lands, not what is said.
func splitSentences(s string) []string {
	var out []string
	runes := []rune(s)
	start := 0
	for i, r := range runes {
		boundary := r == '.' || r == '!' || r == '?' || r == '…'
		nextSpace := i+1 == len(runes) || runes[i+1] == ' ' || runes[i+1] == '\n' || runes[i+1] == '\r' || runes[i+1] == '\t'
		if boundary && nextSpace {
			out = append(out, strings.TrimSpace(string(runes[start:i+1])))
			start = i + 1
		}
	}
	if tail := strings.TrimSpace(string(runes[start:])); tail != "" {
		out = append(out, tail)
	}
	return out
}
