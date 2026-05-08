package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Recipe struct {
	Name       string `yaml:"name"`
	Version    string `yaml:"version"`
	Repository string `yaml:"repository"`

	Path string `yaml:"-"`
}

type Target struct {
	GOOS   string
	GOARCH string
}

type Job struct {
	Recipe Recipe
	Target Target
}

type targetList []string

func (t *targetList) String() string {
	return strings.Join(*t, ",")
}

func (t *targetList) Set(value string) error {
	*t = append(*t, value)
	return nil
}

func parseTarget(value string) (Target, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return Target{}, fmt.Errorf("invalid target format: %s", value)
	}

	return Target{
		GOOS:   parts[0],
		GOARCH: parts[1],
	}, nil
}

func findRecipes(rootDirectory string) ([]Recipe, error) {
	var recipes []Recipe

	err := filepath.WalkDir(rootDirectory, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() || d.Name() != "recipe.yaml" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		var recipe Recipe

		if err := yaml.Unmarshal(data, &recipe); err != nil {
			return fmt.Errorf("failed to parse %s: %w", path, err)
		}

		recipe.Path = path

		recipes = append(recipes, recipe)

		return nil
	})

	return recipes, err
}

func ensureBuilderInstalled(builder string) (string, error) {
	if builder == "" {
		return "", nil
	}

	userHomedir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	builderDirectory := filepath.Join(
		userHomedir,
		".krossbuild_plakar_builders",
		builder,
	)

	if err := os.MkdirAll(builderDirectory, 0755); err != nil {
		return "", err
	}

	plakarPath := filepath.Join(builderDirectory, "plakar")

	if _, err := os.Stat(plakarPath); err == nil {
		fmt.Println("Builder already installed:", plakarPath)
		return plakarPath, nil
	}

	fmt.Println("Installing builder:", builder)

	cmd := exec.Command(
		"go",
		"install",
		fmt.Sprintf("github.com/PlakarKorp/plakar@%s", builder),
	)

	cmd.Env = append(os.Environ(),
		"GOBIN="+builderDirectory,
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", err
	}

	return plakarPath, nil
}

func artifactFilename(job Job) string {
	return fmt.Sprintf(
		"%s_%s_%s_%s.ptar",
		job.Recipe.Name,
		job.Recipe.Version,
		job.Target.GOOS,
		job.Target.GOARCH,
	)
}

func artifactDirectory(baseDirectory string, job Job) string {
	return filepath.Join(
		baseDirectory,
		job.Recipe.Name,
	)
}

func artifactPath(baseDirectory string, job Job) string {
	return filepath.Join(
		artifactDirectory(baseDirectory, job),
		artifactFilename(job),
	)
}

func acquireLock(lockPath string) error {
	file, err := os.OpenFile(
		lockPath,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0644,
	)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("lock already exists: %s", lockPath)
		}
		return err
	}

	defer file.Close()

	_, err = file.WriteString(fmt.Sprintf("%d\n", os.Getpid()))
	return err
}

func worker(
	id int,
	builderPath string,
	artifactsDirectory string,
	jobs <-chan Job,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	for job := range jobs {
		outputDirectory := artifactDirectory(
			artifactsDirectory,
			job,
		)

		outputPath := artifactPath(
			artifactsDirectory,
			job,
		)

		// Skip existing artifacts
		if _, err := os.Stat(outputPath); err == nil {
			fmt.Printf(
				"[worker %d] SKIP existing artifact %s\n",
				id,
				outputPath,
			)
			continue
		}

		if err := os.MkdirAll(outputDirectory, 0755); err != nil {
			fmt.Printf(
				"[worker %d] ERROR creating artifact directory: %v\n",
				id,
				err,
			)
			continue
		}

		fmt.Printf(
			"[worker %d] building %s@%s for %s/%s\n",
			id,
			job.Recipe.Name,
			job.Recipe.Version,
			job.Target.GOOS,
			job.Target.GOARCH,
		)

		fmt.Printf(
			"[worker %d] output=%s\n",
			id,
			outputPath,
		)

		cmd := exec.Command(
			builderPath,
			"pkg",
			"build",
			job.Recipe.Path,
		)

		// Build inside artifact directory
		cmd.Dir = outputDirectory

		cmd.Env = append(os.Environ(),
			"GOOS="+job.Target.GOOS,
			"GOARCH="+job.Target.GOARCH,
		)

		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			fmt.Printf(
				"[worker %d] ERROR building %s for %s/%s: %v\n",
				id,
				job.Recipe.Name,
				job.Target.GOOS,
				job.Target.GOARCH,
				err,
			)
			continue
		}

		// Verify artifact exists
		if _, err := os.Stat(outputPath); err != nil {
			fmt.Printf(
				"[worker %d] ERROR artifact not produced: %s\n",
				id,
				outputPath,
			)
			continue
		}

		fmt.Printf(
			"[worker %d] DONE %s\n",
			id,
			outputPath,
		)
	}
}

func main() {
	var edition string
	var version string
	var builder string
	var workers int
	var artifactsDirectory string

	var targets targetList

	flag.StringVar(
		&edition,
		"edition",
		"community",
		"Specify the edition to build",
	)

	flag.StringVar(
		&version,
		"version",
		"v1.1.0",
		"Specify the version to build",
	)

	flag.StringVar(
		&builder,
		"builder",
		"v1.1.0-beta.6",
		"Specify the builder version",
	)

	flag.StringVar(
		&artifactsDirectory,
		"artifacts",
		"./artifacts",
		"Artifact output directory",
	)

	flag.IntVar(
		&workers,
		"workers",
		4,
		"Number of concurrent workers",
	)

	flag.Var(
		&targets,
		"target",
		"Build target in GOOS/GOARCH format (repeatable)",
	)

	flag.Parse()

	lockPath := filepath.Join(
		os.TempDir(),
		"krossbuild.lock",
	)

	if err := acquireLock(lockPath); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}

	defer os.Remove(lockPath)

	rootDirectory := "./"
	if flag.NArg() > 0 {
		rootDirectory = flag.Arg(0)
	}

	if len(targets) == 0 {
		targets = append(targets, "linux/amd64")
	}

	builderPath, err := ensureBuilderInstalled(builder)
	if err != nil {
		fmt.Println("Error installing builder:", err)
		os.Exit(1)
	}

	var parsedTargets []Target

	for _, value := range targets {
		target, err := parseTarget(value)
		if err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}

		parsedTargets = append(parsedTargets, target)
	}

	recipes, err := findRecipes(rootDirectory)
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}

	fmt.Printf("Found %d recipes\n", len(recipes))
	fmt.Printf("Using %d workers\n", workers)

	totalJobs := len(recipes) * len(parsedTargets)

	fmt.Printf("Generated %d jobs\n", totalJobs)

	jobs := make(chan Job)

	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)

		go worker(
			i+1,
			builderPath,
			artifactsDirectory,
			jobs,
			&wg,
		)
	}

	for _, recipe := range recipes {
		for _, target := range parsedTargets {
			jobs <- Job{
				Recipe: recipe,
				Target: target,
			}
		}
	}

	close(jobs)

	wg.Wait()

	fmt.Println("All jobs completed")
}
