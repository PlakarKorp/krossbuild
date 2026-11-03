package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecipeSemver(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		{"v1.1.2", "v1.1.2"},
		{"s3/v1.1.2", "v1.1.2"},
		{"a/b/v2.0.0", "v2.0.0"},
		{"", ""},
	}

	for _, tt := range tests {
		r := &Recipe{Version: tt.version}
		if got := r.Semver(); got != tt.want {
			t.Errorf("Semver(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

func TestTargetList(t *testing.T) {
	var targets targetList

	if got := targets.String(); got != "" {
		t.Errorf("empty String() = %q", got)
	}

	for _, v := range []string{"linux/amd64", "darwin/arm64"} {
		if err := targets.Set(v); err != nil {
			t.Fatal(err)
		}
	}

	if got := targets.String(); got != "linux/amd64,darwin/arm64" {
		t.Errorf("String() = %q", got)
	}
}

func TestParseTarget(t *testing.T) {
	tests := []struct {
		value   string
		want    Target
		wantErr bool
	}{
		{"linux/amd64", Target{GOOS: "linux", GOARCH: "amd64"}, false},
		{"openbsd/arm64", Target{GOOS: "openbsd", GOARCH: "arm64"}, false},
		{"linux", Target{}, true},
		{"linux/amd64/v3", Target{}, true},
		{"", Target{}, true},
	}

	for _, tt := range tests {
		got, err := parseTarget(tt.value)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseTarget(%q) err = %v, wantErr %v", tt.value, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("parseTarget(%q) = %+v, want %+v", tt.value, got, tt.want)
		}
	}
}

func TestArtifactPaths(t *testing.T) {
	job := Job{
		Recipe: Recipe{Name: "s3", Version: "s3/v1.1.2"},
		Target: Target{GOOS: "linux", GOARCH: "amd64"},
	}

	if got := artifactFilename(job); got != "s3_v1.1.2_linux_amd64.ptar" {
		t.Errorf("artifactFilename = %q", got)
	}

	if got := artifactDirectory("out", job); got != filepath.Join("out", "s3") {
		t.Errorf("artifactDirectory = %q", got)
	}

	if got := artifactPath("out", job); got != filepath.Join("out", "s3", "s3_v1.1.2_linux_amd64.ptar") {
		t.Errorf("artifactPath = %q", got)
	}

	if got := checksumPath("x.ptar"); got != "x.ptar.sum" {
		t.Errorf("checksumPath = %q", got)
	}
}

func TestSha256File(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "data")
	writeFile(t, path, "hello krossbuild\n")

	sum := sha256.Sum256([]byte("hello krossbuild\n"))
	want := hex.EncodeToString(sum[:])

	got, err := sha256File(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("sha256File = %q, want %q", got, want)
	}

	if _, err := sha256File(filepath.Join(dir, "missing")); err == nil {
		t.Error("expected error for a missing file")
	}

	// A directory opens fine but cannot be read, which fails the copy.
	if _, err := sha256File(dir); err == nil {
		t.Error("expected error when hashing a directory")
	}
}

func TestWriteChecksum(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "pkg.ptar")
	writeFile(t, path, "artifact")

	if err := writeChecksum(path); err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256([]byte("artifact"))
	want := fmt.Sprintf("SHA256 (pkg.ptar) = %s\n", hex.EncodeToString(sum[:]))

	if got := readFile(t, path+".sum"); got != want {
		t.Errorf("checksum file = %q, want %q", got, want)
	}

	if err := writeChecksum(filepath.Join(dir, "missing")); err == nil {
		t.Error("expected error for a missing artifact")
	}

	// The .sum destination is a directory: hashing works, writing does not.
	blocked := filepath.Join(dir, "blocked.ptar")
	writeFile(t, blocked, "artifact")
	mkdir(t, blocked+".sum")

	if err := writeChecksum(blocked); err == nil {
		t.Error("expected error when the checksum path is a directory")
	}
}

func TestStageRecipe(t *testing.T) {
	dir := t.TempDir()

	recipePath := filepath.Join(dir, "src", "s3", "recipe.yaml")
	writeFile(t, recipePath, "name: s3\nversion: v1.0.0\n")

	recipe := Recipe{Name: "s3", Version: "v1.0.0", Path: recipePath}

	t.Run("success", func(t *testing.T) {
		base := filepath.Join(dir, "artifacts")

		if err := stageRecipe(base, recipe); err != nil {
			t.Fatal(err)
		}

		staged := filepath.Join(base, "s3", "recipe.yaml")
		if got := readFile(t, staged); got != "name: s3\nversion: v1.0.0\n" {
			t.Errorf("staged recipe = %q", got)
		}

		if got := readFile(t, staged+".sum"); !strings.HasPrefix(got, "SHA256 (recipe.yaml) = ") {
			t.Errorf("staged checksum = %q", got)
		}
	})

	t.Run("base directory is a file", func(t *testing.T) {
		base := filepath.Join(dir, "notadir")
		writeFile(t, base, "")

		if err := stageRecipe(base, recipe); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("recipe missing", func(t *testing.T) {
		missing := recipe
		missing.Path = filepath.Join(dir, "nope", "recipe.yaml")

		if err := stageRecipe(filepath.Join(dir, "a"), missing); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("destination is a directory", func(t *testing.T) {
		base := filepath.Join(dir, "b")
		mkdir(t, filepath.Join(base, "s3", "recipe.yaml"))

		if err := stageRecipe(base, recipe); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("checksum destination is a directory", func(t *testing.T) {
		base := filepath.Join(dir, "c")
		mkdir(t, filepath.Join(base, "s3", "recipe.yaml.sum"))

		if err := stageRecipe(base, recipe); err == nil {
			t.Error("expected error")
		}
	})
}

func TestAcquireLock(t *testing.T) {
	dir := t.TempDir()

	lockPath := filepath.Join(dir, "krossbuild.lock")

	if err := acquireLock(lockPath); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, lockPath); got != fmt.Sprintf("%d\n", os.Getpid()) {
		t.Errorf("lock content = %q", got)
	}

	err := acquireLock(lockPath)
	if err == nil || !strings.Contains(err.Error(), "lock already exists") {
		t.Errorf("second acquire err = %v", err)
	}

	err = acquireLock(filepath.Join(dir, "missing", "krossbuild.lock"))
	if err == nil || strings.Contains(err.Error(), "lock already exists") {
		t.Errorf("acquire in missing directory err = %v", err)
	}
}
