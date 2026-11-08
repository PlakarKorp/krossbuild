package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindRecipes(t *testing.T) {
	t.Run("walks the tree and skips everything else", func(t *testing.T) {
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, "a", "recipe.yaml"), "name: a\nversion: v1.0.0\nrepository: r\n")
		writeFile(t, filepath.Join(dir, "b", "nested", "recipe.yaml"), "name: b\nversion: b/v2.0.0\n")
		writeFile(t, filepath.Join(dir, "b", "README.md"), "not a recipe")
		mkdir(t, filepath.Join(dir, "c", "recipe.yaml"))

		recipes, err := findRecipes(dir)
		if err != nil {
			t.Fatal(err)
		}

		want := []Recipe{
			{Name: "a", Version: "v1.0.0", Repository: "r", Path: filepath.Join(dir, "a", "recipe.yaml")},
			{Name: "b", Version: "b/v2.0.0", Path: filepath.Join(dir, "b", "nested", "recipe.yaml")},
		}

		if len(recipes) != len(want) {
			t.Fatalf("got %d recipes, want %d: %+v", len(recipes), len(want), recipes)
		}
		for i := range want {
			if recipes[i] != want[i] {
				t.Errorf("recipe %d = %+v, want %+v", i, recipes[i], want[i])
			}
		}
	})

	t.Run("missing root", func(t *testing.T) {
		if _, err := findRecipes(filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("unreadable recipe", func(t *testing.T) {
		dir := t.TempDir()

		// A dangling symlink is listed by the walk but cannot be read.
		if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "recipe.yaml")); err != nil {
			t.Fatal(err)
		}

		if _, err := findRecipes(dir); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("invalid yaml", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "recipe.yaml"), "name: [\n")

		_, err := findRecipes(dir)
		if err == nil || !strings.Contains(err.Error(), "failed to parse") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestEnsureBuilderInstalled(t *testing.T) {
	t.Run("empty builder", func(t *testing.T) {
		path, err := ensureBuilderInstalled("")
		if err != nil || path != "" {
			t.Errorf("got %q, %v", path, err)
		}
	})

	t.Run("no home", func(t *testing.T) {
		t.Setenv("HOME", "")

		if _, err := ensureBuilderInstalled("v1.0.0"); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("home is a file", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "home")
		writeFile(t, home, "")
		t.Setenv("HOME", home)

		if _, err := ensureBuilderInstalled("v1.0.0"); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("already installed", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		want := installBuilderStub(t, "v1.0.0", "exit 0")

		var path string
		var err error
		out := captureStdout(t, func() {
			path, err = ensureBuilderInstalled("v1.0.0")
		})
		if err != nil {
			t.Fatal(err)
		}
		if path != want {
			t.Errorf("path = %q, want %q", path, want)
		}
		if !strings.Contains(out, "Builder already installed: "+want) {
			t.Errorf("output = %q", out)
		}
	})

	t.Run("installs with go install", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		bin := t.TempDir()
		log := filepath.Join(bin, "go.log")
		writeScript(t, filepath.Join(bin, "go"),
			`printf '%s\n' "$GOBIN" "$@" > "`+log+`"
mkdir -p "$GOBIN" && : > "$GOBIN/plakar"`)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		var path string
		var err error
		out := captureStdout(t, func() {
			path, err = ensureBuilderInstalled("v1.2.3")
		})
		if err != nil {
			t.Fatal(err)
		}

		builderDirectory := filepath.Join(home, ".krossbuild_plakar_builders", "v1.2.3")
		if path != filepath.Join(builderDirectory, "plakar") {
			t.Errorf("path = %q", path)
		}
		if !exists(path) {
			t.Errorf("%s was not created by the stub", path)
		}
		if !strings.Contains(out, "Installing builder: v1.2.3") {
			t.Errorf("output = %q", out)
		}

		want := builderDirectory + "\ninstall\ngithub.com/PlakarKorp/plakar@v1.2.3\n"
		if got := readFile(t, log); got != want {
			t.Errorf("go invoked as %q, want %q", got, want)
		}
	})

	t.Run("go install fails", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())

		bin := t.TempDir()
		writeScript(t, filepath.Join(bin, "go"), "exit 1")
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		var err error
		captureStdout(t, func() {
			_, err = ensureBuilderInstalled("v1.2.3")
		})
		if err == nil {
			t.Error("expected error")
		}
	})
}

func TestWorker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	builder := installBuilderStub(t, "stub", builderStub)

	dir := t.TempDir()
	recipePath := filepath.Join(dir, "src", "s3", "recipe.yaml")
	writeFile(t, recipePath, "name: s3\nversion: s3/v1.1.2\n")

	job := Job{
		Recipe: Recipe{Name: "s3", Version: "s3/v1.1.2", Path: recipePath},
		Target: Target{GOOS: "openbsd", GOARCH: "arm64"},
	}
	filename := "s3_v1.1.2_openbsd_arm64.ptar"

	t.Run("builds and checksums", func(t *testing.T) {
		artifacts := filepath.Join(dir, "build")
		log := filepath.Join(dir, "build.log")
		t.Setenv("KROSSBUILD_STUB_LOG", log)

		out := runWorker(t, builder, artifacts, job)

		outputPath := filepath.Join(artifacts, "s3", filename)
		if !exists(outputPath) {
			t.Fatalf("artifact %s not produced in the builder's directory", outputPath)
		}
		if !exists(outputPath + ".sum") {
			t.Errorf("checksum not written")
		}

		info, err := os.Stat(outputPath)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o644 {
			t.Errorf("artifact mode = %o, want 644", perm)
		}

		want := "openbsd/arm64\nCGO_ENABLED=0\npkg\nbuild\n" + recipePath + "\n"
		if got := readFile(t, log); got != want {
			t.Errorf("builder invoked as %q, want %q", got, want)
		}

		for _, line := range []string{
			"[worker 1] building s3@s3/v1.1.2 for openbsd/arm64",
			"[worker 1] output=" + outputPath,
			"[worker 1] DONE " + outputPath,
		} {
			if !strings.Contains(out, line) {
				t.Errorf("output %q lacks %q", out, line)
			}
		}
	})

	t.Run("skips existing artifact", func(t *testing.T) {
		artifacts := filepath.Join(dir, "skip")
		outputPath := filepath.Join(artifacts, "s3", filename)
		writeFile(t, outputPath, "old")
		writeFile(t, outputPath+".sum", "old sum")

		out := runWorker(t, builder, artifacts, job)

		if !strings.Contains(out, "[worker 1] SKIP existing artifact "+outputPath) {
			t.Errorf("output = %q", out)
		}
		if got := readFile(t, outputPath+".sum"); got != "old sum" {
			t.Errorf("checksum rewritten: %q", got)
		}
	})

	t.Run("checksums existing artifact that lacks one", func(t *testing.T) {
		artifacts := filepath.Join(dir, "skipsum")
		outputPath := filepath.Join(artifacts, "s3", filename)
		writeFile(t, outputPath, "old")

		out := runWorker(t, builder, artifacts, job)

		if !strings.Contains(out, "[worker 1] SKIP existing artifact "+outputPath) {
			t.Errorf("output = %q", out)
		}
		if got := readFile(t, outputPath+".sum"); !strings.HasPrefix(got, "SHA256 ("+filename+") = ") {
			t.Errorf("checksum = %q", got)
		}
	})

	t.Run("existing artifact checksum fails", func(t *testing.T) {
		// The artifact is present but is a directory: it has no checksum
		// and cannot be hashed.
		artifacts := filepath.Join(dir, "skipfail")
		outputPath := filepath.Join(artifacts, "s3", filename)
		mkdir(t, outputPath)

		out := runWorker(t, builder, artifacts, job)

		if !strings.Contains(out, "[worker 1] ERROR checksumming "+outputPath) {
			t.Errorf("output = %q", out)
		}
		if strings.Contains(out, "SKIP") {
			t.Errorf("output = %q", out)
		}
	})

	t.Run("artifact directory cannot be created", func(t *testing.T) {
		artifacts := filepath.Join(dir, "notdir")
		writeFile(t, filepath.Join(artifacts, "s3"), "in the way")

		out := runWorker(t, builder, artifacts, job)

		if !strings.Contains(out, "[worker 1] ERROR creating artifact directory") {
			t.Errorf("output = %q", out)
		}
	})

	t.Run("build fails", func(t *testing.T) {
		artifacts := filepath.Join(dir, "fail")

		out := runWorker(t, filepath.Join(dir, "no-such-builder"), artifacts, job)

		if !strings.Contains(out, "[worker 1] ERROR building s3 for openbsd/arm64") {
			t.Errorf("output = %q", out)
		}
		if exists(filepath.Join(artifacts, "s3", filename+".sum")) {
			t.Error("checksum written for a failed build")
		}
	})

	t.Run("build produces no artifact", func(t *testing.T) {
		noop := installBuilderStub(t, "noop", "exit 0")
		artifacts := filepath.Join(dir, "noartifact")

		out := runWorker(t, noop, artifacts, job)

		outputPath := filepath.Join(artifacts, "s3", filename)
		if !strings.Contains(out, "[worker 1] ERROR artifact not produced: "+outputPath) {
			t.Errorf("output = %q", out)
		}
	})

	t.Run("built artifact checksum fails", func(t *testing.T) {
		artifacts := filepath.Join(dir, "sumfail")
		outputPath := filepath.Join(artifacts, "s3", filename)
		mkdir(t, outputPath+".sum")
		t.Setenv("KROSSBUILD_STUB_LOG", filepath.Join(dir, "sumfail.log"))

		out := runWorker(t, builder, artifacts, job)

		if !strings.Contains(out, "[worker 1] ERROR checksumming "+outputPath) {
			t.Errorf("output = %q", out)
		}
		if strings.Contains(out, "DONE") {
			t.Errorf("output = %q", out)
		}
	})
}
