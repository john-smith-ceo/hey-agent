package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpdate_PreservesUnknownAndComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	orig := "# provider config\nexport OPENAI_API_KEY=old\n\nUNRELATED=keep-me\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, map[string]string{"OPENAI_API_KEY": "new-key", "HEY_AGENT_MODEL": "gpt-x"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# provider config\nexport OPENAI_API_KEY=new-key\n\nUNRELATED=keep-me\nHEY_AGENT_MODEL=gpt-x\n"
	if string(got) != want {
		t.Fatalf("file mismatch:\n%s", got)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestUpdate_CreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", ".env")
	if err := Update(path, map[string]string{"HEY_AGENT_API_KEY": "sk-1"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "HEY_AGENT_API_KEY=sk-1\n" {
		t.Fatalf("unexpected content %q", got)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", fi.Mode().Perm())
	}
}

func TestUpdate_TightensLoosePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("K=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, map[string]string{"K": "2"}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := Update(path, map[string]string{"A": "1", "B": "x y"}); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["A"] != "1" || m["B"] != "x y" {
		t.Fatalf("load = %v", m)
	}
}

func TestLoad_MissingIsEmpty(t *testing.T) {
	m, err := Load(filepath.Join(t.TempDir(), "none.env"))
	if err != nil || len(m) != 0 {
		t.Fatalf("load missing = %v, %v", m, err)
	}
}

func TestValidateValue(t *testing.T) {
	if err := ValidateValue("two\nlines"); err == nil {
		t.Fatal("multiline value accepted")
	}
	if err := ValidateValue("fine"); err != nil {
		t.Fatal(err)
	}
}
