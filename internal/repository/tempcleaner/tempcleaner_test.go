package tempcleaner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testMaxAge = 24 * time.Hour

// testFile описывает файл или директорию, создаваемые в тестовой директории.
type testFile struct {
	name  string        // имя файла (может содержать подпуть, напр. "sub/old.txt")
	age   time.Duration // возраст модификации; применяется через os.Chtimes
	isDir bool          // создавать директорию вместо файла
}

// writeAgedFile создаёт файл и «состаривает» его, выставляя ModTime в прошлое.
func writeAgedFile(t *testing.T, dir, name string, age time.Duration) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("failed to create parent dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("temp"), 0o644); err != nil {
		t.Fatalf("failed to write file %s: %v", path, err)
	}

	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("failed to set mtime for %s: %v", path, err)
	}
}

func TestCleanOlderThan(t *testing.T) {
	tests := []struct {
		name      string
		files     []testFile
		maxAge    time.Duration // возраст для CleanOlderThan; 0 = testMaxAge
		wantErr   bool
		wantExist []string // файлы, которые должны остаться после очистки
		wantGone  []string // файлы, которые должны быть удалены
		wantCount int      // ожидаемое число записей в директории после очистки
	}{
		{
			name: "old files are removed",
			files: []testFile{
				{name: "old1.txt", age: 25 * time.Hour},
				{name: "old2.txt", age: 48 * time.Hour},
			},
			wantGone:  []string{"old1.txt", "old2.txt"},
			wantCount: 0,
		},
		{
			name: "fresh files remain",
			files: []testFile{
				{name: "fresh1.txt", age: 1 * time.Hour},
				{name: "fresh2.txt", age: 23 * time.Hour},
			},
			wantExist: []string{"fresh1.txt", "fresh2.txt"},
			wantCount: 2,
		},
		{
			name: "mixed old and fresh",
			files: []testFile{
				{name: "old.txt", age: 25 * time.Hour},
				{name: "fresh.txt", age: 1 * time.Hour},
			},
			wantExist: []string{"fresh.txt"},
			wantGone:  []string{"old.txt"},
			wantCount: 1,
		},
		{
			name: "subdirectories are not removed or traversed",
			files: []testFile{
				{name: "sub", isDir: true},
				{name: "sub/old.txt", age: 25 * time.Hour},
				{name: "top-old.txt", age: 25 * time.Hour},
			},
			wantExist: []string{"sub", "sub/old.txt"},
			wantGone:  []string{"top-old.txt"},
			// на верхнем уровне остаётся только поддиректория "sub"
			wantCount: 1,
		},
		{
			name:      "exactly maxAge remains",
			files:     []testFile{{name: "boundary.txt", age: 24 * time.Hour}},
			maxAge:    24*time.Hour + time.Second,
			wantExist: []string{"boundary.txt"},
			wantCount: 1,
		},
		{
			name:      "just over maxAge removed",
			files:     []testFile{{name: "over.txt", age: 24*time.Hour + time.Second}},
			wantExist: []string{},
			wantGone:  []string{"over.txt"},
			wantCount: 0,
		},
		{
			name:    "path is a regular file returns error",
			wantErr: true,
		},
		{
			name:      "empty directory",
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			for _, f := range tt.files {
				if f.isDir {
					if err := os.MkdirAll(filepath.Join(dir, f.name), 0o755); err != nil {
						t.Fatalf("failed to create dir %s: %v", f.name, err)
					}
					continue
				}
				writeAgedFile(t, dir, f.name, f.age)
			}

			cleanerDir := dir
			if tt.wantErr {
				// «Директорией» делаем обычный файл: NewTempCleaner не сможет его
				// создать как каталог, а CleanOlderThan получит ошибку от ReadDir.
				blocker := filepath.Join(dir, "not-a-dir")
				if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
					t.Fatalf("failed to write blocker file: %v", err)
				}
				cleanerDir = blocker
			}

			maxAge := tt.maxAge
			if maxAge == 0 {
				maxAge = testMaxAge
			}

			cleaner := NewTempCleaner(cleanerDir)
			err := cleaner.CleanOlderThan(maxAge)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			for _, name := range tt.wantExist {
				if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
					t.Errorf("expected %s to remain, got error: %v", name, err)
				}
			}
			for _, name := range tt.wantGone {
				if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Errorf("expected %s to be removed, got error: %v", name, err)
				}
			}

			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("failed to read dir: %v", err)
			}
			if len(entries) != tt.wantCount {
				t.Errorf("expected %d entries after cleanup, got %d", tt.wantCount, len(entries))
			}
		})
	}
}
