// Package logging fournit un fichier de journal à rotation par taille, sans
// dépendance externe.
//
// Le serveur journalise via slog, dont le handler par défaut écrit sur la sortie
// du logger standard : rediriger celle-ci vers un *File suffit à écrire dans un
// fichier tournant sans changer le format des messages (cf. main.go).
//
// La rotation est indispensable pour un service au long cours : chaque requête
// HTTP produit une ligne et rien, côté launchd (contrairement à journald sous
// Linux), ne borne la taille du fichier de sortie.
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	// DefaultMaxBytes est la taille au-delà de laquelle le journal est archivé.
	DefaultMaxBytes int64 = 5 << 20 // 5 Mio

	// DefaultKeep est le nombre d'archives conservées (fichiers « .1 », « .2 »…).
	DefaultKeep = 3
)

// File est un fichier de journal qui s'archive de lui-même une fois maxBytes
// atteint. Il est utilisable par plusieurs goroutines à la fois.
type File struct {
	path     string
	maxBytes int64
	keep     int

	mu   sync.Mutex
	f    *os.File
	size int64 // taille courante de f, tenue à jour pour éviter un Stat par écriture
}

// Open ouvre (ou crée) le journal path en ajout, en créant au besoin son
// répertoire parent. maxBytes ≤ 0 et keep < 0 retombent sur les valeurs par
// défaut.
func Open(path string, maxBytes int64, keep int) (*File, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if keep < 0 {
		keep = DefaultKeep
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("création du répertoire de journal %s : %w", dir, err)
		}
	}
	l := &File{path: path, maxBytes: maxBytes, keep: keep}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

// Path renvoie le chemin du journal courant.
func (l *File) Path() string { return l.path }

// Write écrit p dans le journal, en l'archivant d'abord si l'écriture le ferait
// dépasser maxBytes.
func (l *File) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Le fichier peut être fermé si une rotation précédente n'a pas pu le
	// rouvrir (disque plein, permissions) : on retente ici plutôt que de perdre
	// définitivement la journalisation.
	if l.f == nil {
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	// Rotation *avant* l'écriture, et jamais sur un fichier vide : un
	// enregistrement est écrit d'un bloc, on préfère dépasser légèrement le
	// seuil plutôt que de couper une ligne (ou une pile de panique) en deux.
	if l.size > 0 && l.size+int64(len(p)) > l.maxBytes {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// Close ferme le journal. Les écritures n'étant pas tamponnées, l'oublier ne
// perd aucun message.
func (l *File) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f, l.size = nil, 0
	return err
}

// open ouvre le journal en ajout et amorce la taille courante. Appelé avec le
// verrou tenu (ou avant publication de l'objet).
func (l *File) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("ouverture du journal %s : %w", l.path, err)
	}
	var size int64
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}
	l.f, l.size = f, size
	return nil
}

// rotate décale les archives (.1 → .2 …), archive le journal courant puis en
// ouvre un neuf. Appelé avec le verrou tenu.
func (l *File) rotate() error {
	if err := l.f.Close(); err != nil {
		return fmt.Errorf("fermeture du journal avant rotation : %w", err)
	}
	l.f, l.size = nil, 0

	if l.keep == 0 {
		// Aucune archive demandée : on repart d'un fichier vide.
		if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("suppression du journal %s : %w", l.path, err)
		}
		return l.open()
	}

	// La plus ancienne archive est écrasée ; les renommages intermédiaires
	// peuvent échouer parce que le fichier n'existe pas encore (journal jeune),
	// ce qui n'est pas une erreur.
	_ = os.Remove(l.archive(l.keep))
	for i := l.keep - 1; i >= 1; i-- {
		_ = os.Rename(l.archive(i), l.archive(i+1))
	}
	if err := os.Rename(l.path, l.archive(1)); err != nil {
		return fmt.Errorf("archivage du journal %s : %w", l.path, err)
	}
	return l.open()
}

// archive renvoie le nom de la n-ième archive (« journal.log.1 »).
func (l *File) archive(n int) string { return fmt.Sprintf("%s.%d", l.path, n) }
