package voiceout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanMarkdown(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"heading", "# Заголовок\nТекст.", "Заголовок Текст."},
		{"bullet list", "- one\n- two", "one two"},
		{"bold", "Это **важно** здесь.", "Это важно здесь."},
		{"link", "Смотри https://example.com/page тут.", "Смотри тут."},
		{"code fence", "До.\n```bash\nsudo rm -rf /\n```\nПосле.", "До. После."},
		{"inline code kept", "Команда `echo` и `flag`.", "Команда echo и flag."},
		{"inline path dropped", "Используй `rm -rf /dir` или `~/.config`.", "Используй или ."},
		{"table lines", "| a | b |\n|---|---|\nРеальная строка.", "Реальная строка."},
		{"shell noise", "$ sudo apt install\nsudo rm x\nОсталось.", "Осталось."},
		// Ported quirk: the heading regex eats the leading '#' before the
		// '#!' line filter runs, so a shebang survives as "!/bin/sh".
		{"shebang quirk", "#!/bin/sh\nОсталось.", "!/bin/sh Осталось."},
		{"star bullets", "* один\n* два", "один два"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cleanMarkdown(c.in); got != c.want {
				t.Fatalf("cleanMarkdown(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSplitSentences(t *testing.T) {
	got := splitSentences("Первое. Второе! Третье?")
	if len(got) != 3 {
		t.Fatalf("expected 3 sentences, got %d: %#v", len(got), got)
	}
	got = splitSentences("Без финальной точки")
	if len(got) != 1 || got[0] != "Без финальной точки" {
		t.Fatalf("tail sentence lost: %#v", got)
	}
}

func TestSplitStagesStaysWithinLimit(t *testing.T) {
	var parts []string
	for i := 0; i < 40; i++ {
		parts = append(parts, "Короткая фраза.")
	}
	stages := splitStages(strings.Join(parts, " "), maxStageChars)
	if len(stages) < 2 {
		t.Fatalf("expected several stages, got %d", len(stages))
	}
	total := 0
	for _, stage := range stages {
		n := len([]rune(strings.Join(stage, " ")))
		if n > maxStageChars {
			t.Fatalf("stage exceeds %d runes: %d", maxStageChars, n)
		}
		for _, sentence := range stage {
			// Words are never cut: every staged sentence still ends with
			// its punctuation mark.
			if !strings.HasSuffix(sentence, ".") {
				t.Fatalf("stage carries a torn sentence %q", sentence)
			}
			total++
		}
	}
	if total != 40 {
		t.Fatalf("sentences lost in staging: %d of 40", total)
	}
}

func TestSplitStagesLongSentenceGetsOwnStage(t *testing.T) {
	long := strings.Repeat("слово ", 100) // one sentence far beyond the limit
	stages := splitStages("Сначала. "+long+". Потом.", maxStageChars)
	if len(stages) != 3 {
		t.Fatalf("expected 3 stages, got %d: %#v", len(stages), stages)
	}
	if len([]rune(stages[1][0])) <= maxStageChars {
		t.Fatalf("the oversized sentence was not kept whole: %#v", stages[1])
	}
}

func TestApplyLexicon(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lexicon.txt")
	rules := "# comment line\n\nClaude = Клауд\nAPI = А Пэ И\n"
	if err := os.WriteFile(path, []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
	got := applyLexicon("Claude отвечает через API", path)
	if got != "Клауд отвечает через А Пэ И" {
		t.Fatalf("lexicon not applied: %q", got)
	}
}

func TestApplyLexiconMissingFileIsNoop(t *testing.T) {
	got := applyLexicon("текст как есть", filepath.Join(t.TempDir(), "absent.txt"))
	if got != "текст как есть" {
		t.Fatalf("missing lexicon changed text: %q", got)
	}
	if got := applyLexicon("текст", ""); got != "текст" {
		t.Fatalf("empty lexicon path changed text: %q", got)
	}
}

func TestTranslit(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Да", "Da"},
		{"нет", "net"},
		{"Ел", "Yel"},
		{"Привет", "Preevet"},
		{"ASCII и микс", "ASCII ee meeks"},
	}
	for _, c := range cases {
		if got := translit(c.in); got != c.want {
			t.Fatalf("translit(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
