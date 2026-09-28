package voiceout

import (
	"strings"
	"unicode"
)

// Cyrillic in latin letters, spelled the way an English voice will read it.
// Ported from agent-voice-over's translit.go (itself a port of
// lib/translit.py): the goal is how it sounds, not transliteration
// accuracy — "и" is "ee", "у" is "oo". Latin runs are left untouched.
// Used only by profiles with translit: true (English piper voices reading
// Russian text).

var translitMap = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo",
	'ж': "zh", 'з': "z", 'и': "ee", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "oo",
	'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

func isCyrillicVowel(r rune) bool {
	switch unicode.ToLower(r) {
	case 'а', 'е', 'ё', 'и', 'о', 'у', 'ы', 'э', 'ю', 'я':
		return true
	}
	return false
}

func isCyrillic(r rune) bool {
	_, ok := translitMap[unicode.ToLower(r)]
	return ok
}

func translitWord(w []rune) string {
	var b strings.Builder
	for i, ch := range w {
		low := unicode.ToLower(ch)
		piece, ok := translitMap[low]
		if !ok {
			b.WriteRune(ch)
			continue
		}
		// "е" at the start of a word or after a vowel sounds like "ye".
		if low == 'е' && (i == 0 || isCyrillicVowel(w[i-1])) {
			piece = "ye"
		}
		if unicode.IsUpper(ch) && piece != "" {
			piece = strings.ToUpper(piece[:1]) + piece[1:]
		}
		b.WriteString(piece)
	}
	return b.String()
}

func translit(text string) string {
	runes := []rune(text)
	var b strings.Builder
	for i := 0; i < len(runes); {
		if !isCyrillic(runes[i]) {
			b.WriteRune(runes[i])
			i++
			continue
		}
		j := i
		for j < len(runes) && isCyrillic(runes[j]) {
			j++
		}
		b.WriteString(translitWord(runes[i:j]))
		i = j
	}
	return b.String()
}
