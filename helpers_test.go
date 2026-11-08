package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written to it. Everything in main.go prints through fmt.Print*,
// so swapping the file is enough; no production seam is needed for output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	old := os.Stdout
	os.Stdout = w

	done := make(chan string)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		_ = r.Close()
		done <- b.String()
	}()

	func() {
		defer func() {
			os.Stdout = old
			_ = w.Close()
		}()
		fn()
	}()

	return <-done
}

// writeScript installs a /bin/sh stub executable at path. The stubs stand in
// for "go install" and for the plakar builder, so no test ever runs a real
// build.
func writeScript(t *testing.T, path, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// builderStub is the body of a fake "plakar pkg build <recipe.yaml>": it logs
// its arguments and the GOOS/GOARCH and CGO_ENABLED it was given to
// KROSSBUILD_STUB_LOG when set, then creates the artifact plakar would
// produce, named after the recipe and the target, in its working directory.
const builderStub = `[ -n "$KROSSBUILD_STUB_LOG" ] && printf '%s\n' "$GOOS/$GOARCH" "CGO_ENABLED=$CGO_ENABLED" "$@" > "$KROSSBUILD_STUB_LOG"
name=$(sed -n 's/^name: //p' "$3")
version=$(sed -n 's/^version: //p' "$3" | sed 's#.*/##')
: > "${name}_${version}_${GOOS}_${GOARCH}.ptar"`

// mainEnv points HOME and TMPDIR at fresh temporary directories so main's
// builder lookup and lock file stay inside the test.
func mainEnv(t *testing.T) (home, tmp string) {
	t.Helper()

	home, tmp = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tmp)

	return home, tmp
}

// installBuilderStub puts a builder stub where ensureBuilderInstalled looks
// for an already installed builder and returns its path. HOME must already
// point at a temporary directory.
func installBuilderStub(t *testing.T, builder, body string) string {
	t.Helper()

	path := filepath.Join(
		os.Getenv("HOME"),
		".krossbuild_plakar_builders",
		builder,
		"plakar",
	)

	writeScript(t, path, body)

	return path
}

// runWorker feeds jobs to a single worker and returns what it printed.
func runWorker(t *testing.T, builderPath, artifactsDirectory string, jobs ...Job) string {
	t.Helper()

	ch := make(chan Job, len(jobs))
	for _, job := range jobs {
		ch <- job
	}
	close(ch)

	var wg sync.WaitGroup
	wg.Add(1)

	return captureStdout(t, func() {
		worker(1, builderPath, artifactsDirectory, ch, &wg)
		wg.Wait()
	})
}

type exitCode int

// runMain calls main with the given command line and reports the exit code
// (0 when main returned) together with everything it printed. Callers set up
// the environment with mainEnv first.
func runMain(t *testing.T, args ...string) (int, string) {
	t.Helper()

	oldArgs, oldFlags, oldExit := os.Args, flag.CommandLine, osExit
	t.Cleanup(func() {
		os.Args, flag.CommandLine, osExit = oldArgs, oldFlags, oldExit
	})

	os.Args = append([]string{"krossbuild"}, args...)
	flag.CommandLine = flag.NewFlagSet("krossbuild", flag.ExitOnError)
	osExit = func(code int) {
		panic(exitCode(code))
	}

	code := 0
	out := captureStdout(t, func() {
		defer func() {
			if r := recover(); r != nil {
				c, ok := r.(exitCode)
				if !ok {
					panic(r)
				}
				code = int(c)
			}
		}()
		main()
	})

	return code, out
}
