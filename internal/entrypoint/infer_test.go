package entrypoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInferPrefersExecutableNameOverContainingDirectory(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "WinRAR")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"Rar.exe",
		"RarExtInstaller.exe",
		"UnRAR.exe",
		"Uninstall.exe",
		"WinRAR.exe",
	} {
		if err := os.WriteFile(filepath.Join(appDir, name), []byte("MZ"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Infer([]Root{{Path: root}}, "RARLab.WinRAR", "WinRAR")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(appDir, "WinRAR.exe")
	if got != want {
		t.Errorf("entry = %q, want the exact executable-name match %q", got, want)
	}
}

func TestInferUsesPackageDirectoryForGenericExecutable(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "Demo", "bin", "runtime.exe")
	other := filepath.Join(root, "Unrelated", "short.exe")
	for _, path := range []string{want, other} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("MZ"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Infer([]Root{{Path: root}}, "Vendor.Demo", "Demo")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("entry = %q, want package-directory match %q", got, want)
	}
}
