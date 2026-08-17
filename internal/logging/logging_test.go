package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestOpenCreeLeRepertoire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "app.log")

	l, err := Open(path, 0, 0)
	if err != nil {
		t.Fatalf("Open : %v", err)
	}
	defer l.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("journal absent : %v", err)
	}
	if l.Path() != path {
		t.Errorf("Path() = %q, attendu %q", l.Path(), path)
	}
	if l.maxBytes != DefaultMaxBytes {
		t.Errorf("maxBytes = %d, attendu le défaut %d", l.maxBytes, DefaultMaxBytes)
	}
}

func TestWriteAjouteAuFichierExistant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("ancien\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := Open(path, DefaultMaxBytes, DefaultKeep)
	if err != nil {
		t.Fatalf("Open : %v", err)
	}
	defer l.Close()

	if _, err := l.Write([]byte("nouveau\n")); err != nil {
		t.Fatalf("Write : %v", err)
	}

	if got := readFile(t, path); got != "ancien\nnouveau\n" {
		t.Errorf("contenu = %q, attendu \"ancien\\nnouveau\\n\"", got)
	}
}

func TestRotationDecaleLesArchives(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	// Seuil de 6 octets : chaque ligne en fait 5, donc la suivante déclenche la
	// rotation — un fichier par ligne.
	l, err := Open(path, 6, 2)
	if err != nil {
		t.Fatalf("Open : %v", err)
	}
	defer l.Close()

	for _, line := range []string{"aaaa\n", "bbbb\n", "cccc\n", "dddd\n"} {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatalf("Write(%q) : %v", line, err)
		}
	}

	// « dddd » est le journal courant, les deux archives contiennent les lignes
	// précédentes du plus récent au plus ancien, et « aaaa » est tombé au-delà
	// de keep=2.
	for _, want := range []struct{ path, content string }{
		{path, "dddd\n"},
		{path + ".1", "cccc\n"},
		{path + ".2", "bbbb\n"},
	} {
		if got := readFile(t, want.path); got != want.content {
			t.Errorf("%s = %q, attendu %q", filepath.Base(want.path), got, want.content)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("archive .3 présente alors que keep = 2 (err = %v)", err)
	}
}

func TestRotationSansArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")

	l, err := Open(path, 6, 0)
	if err != nil {
		t.Fatalf("Open : %v", err)
	}
	defer l.Close()

	for _, line := range []string{"aaaa\n", "bbbb\n"} {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatalf("Write(%q) : %v", line, err)
		}
	}

	if got := readFile(t, path); got != "bbbb\n" {
		t.Errorf("contenu = %q, attendu \"bbbb\\n\"", got)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Errorf("archive .1 créée alors que keep = 0 (err = %v)", err)
	}
}

func TestEcritureLongueNonTronquee(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")

	l, err := Open(path, 8, 1)
	if err != nil {
		t.Fatalf("Open : %v", err)
	}
	defer l.Close()

	// Un enregistrement plus grand que le seuil (une pile de panique, typiquement)
	// est écrit d'un bloc : le journal dépasse maxBytes plutôt que de couper.
	long := []byte("pile de panique très longue\n")
	if _, err := l.Write([]byte("amorce\n")); err != nil {
		t.Fatalf("Write : %v", err)
	}
	if _, err := l.Write(long); err != nil {
		t.Fatalf("Write : %v", err)
	}

	if got := readFile(t, path); got != string(long) {
		t.Errorf("contenu = %q, attendu %q", got, string(long))
	}
}

func TestWriteConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")

	l, err := Open(path, 64, 2)
	if err != nil {
		t.Fatalf("Open : %v", err)
	}
	defer l.Close()

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			for j := range 10 {
				if _, err := fmt.Fprintf(l, "goroutine %d ligne %d\n", i, j); err != nil {
					t.Errorf("Write : %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestCloseIdempotent(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "app.log"), 0, 0)
	if err != nil {
		t.Fatalf("Open : %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("premier Close : %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close : %v", err)
	}
}

func TestWriteApresCloseReouvre(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")

	l, err := Open(path, 0, 0)
	if err != nil {
		t.Fatalf("Open : %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close : %v", err)
	}
	if _, err := l.Write([]byte("après fermeture\n")); err != nil {
		t.Fatalf("Write : %v", err)
	}
	defer l.Close()

	if got := readFile(t, path); got != "après fermeture\n" {
		t.Errorf("contenu = %q, attendu \"après fermeture\\n\"", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("lecture de %s : %v", path, err)
	}
	return string(b)
}
