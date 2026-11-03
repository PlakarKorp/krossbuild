package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// writeTree lays out a source tree with two recipes and returns its root.
func writeTree(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "s3", "recipe.yaml"), "name: s3\nversion: s3/v1.1.2\n")
	writeFile(t, filepath.Join(root, "fs", "recipe.yaml"), "name: fs\nversion: v1.0.0\n")

	return root
}

func TestMainBuildsEveryRecipe(t *testing.T) {
	_, tmp := mainEnv(t)
	installBuilderStub(t, "stub", builderStub)

	root := writeTree(t)
	artifacts := filepath.Join(t.TempDir(), "artifacts")

	code, out := runMain(t,
		"-builder", "stub",
		"-artifacts", artifacts,
		"-workers", "2",
		root,
	)
	if code != 0 {
		t.Fatalf("exit code %d, output:\n%s", code, out)
	}

	for _, line := range []string{
		"Builder already installed:",
		"Found 2 recipes",
		"Using 2 workers",
		"Generated 2 jobs",
		"All jobs completed",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}

	// No -target given: the default linux/amd64 is built for each recipe,
	// next to the staged recipe and its checksum.
	for _, path := range []string{
		filepath.Join(artifacts, "s3", "recipe.yaml"),
		filepath.Join(artifacts, "s3", "recipe.yaml.sum"),
		filepath.Join(artifacts, "s3", "s3_v1.1.2_linux_amd64.ptar"),
		filepath.Join(artifacts, "s3", "s3_v1.1.2_linux_amd64.ptar.sum"),
		filepath.Join(artifacts, "fs", "recipe.yaml"),
		filepath.Join(artifacts, "fs", "recipe.yaml.sum"),
		filepath.Join(artifacts, "fs", "fs_v1.0.0_linux_amd64.ptar"),
		filepath.Join(artifacts, "fs", "fs_v1.0.0_linux_amd64.ptar.sum"),
	} {
		if !exists(path) {
			t.Errorf("%s missing", path)
		}
	}

	if exists(filepath.Join(tmp, "krossbuild.lock")) {
		t.Error("lock not released")
	}
}

func TestMainErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, home, tmp string) []string
		want  string
	}{
		{
			name: "lock already held",
			setup: func(t *testing.T, home, tmp string) []string {
				writeFile(t, filepath.Join(tmp, "krossbuild.lock"), "1\n")
				return nil
			},
			want: "Error: lock already exists: ",
		},
		{
			name: "named lock already held",
			setup: func(t *testing.T, home, tmp string) []string {
				writeFile(t, filepath.Join(tmp, "krossbuild.nightly.lock"), "1\n")
				return []string{"-name", "nightly"}
			},
			want: string(filepath.Separator) + "krossbuild.nightly.lock\n",
		},
		{
			name: "builder cannot be installed",
			setup: func(t *testing.T, home, tmp string) []string {
				t.Setenv("HOME", "")
				return nil
			},
			want: "Error installing builder: ",
		},
		{
			name: "invalid target",
			setup: func(t *testing.T, home, tmp string) []string {
				return []string{"-builder", "", "-target", "linux"}
			},
			want: "Error: invalid target format: linux",
		},
		{
			name: "missing root directory",
			setup: func(t *testing.T, home, tmp string) []string {
				return []string{"-builder", "", filepath.Join(tmp, "nowhere")}
			},
			want: "Error: ",
		},
		{
			name: "recipe cannot be staged",
			setup: func(t *testing.T, home, tmp string) []string {
				artifacts := filepath.Join(tmp, "artifacts")
				writeFile(t, artifacts, "in the way")
				return []string{"-builder", "", "-artifacts", artifacts, writeTree(t)}
			},
			want: "Error staging recipe ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, tmp := mainEnv(t)
			args := tt.setup(t, home, tmp)

			code, out := runMain(t, args...)
			if code != 1 {
				t.Errorf("exit code %d, want 1", code)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("output lacks %q:\n%s", tt.want, out)
			}
			if strings.Contains(out, "All jobs completed") {
				t.Errorf("main kept going after the error:\n%s", out)
			}
		})
	}
}
