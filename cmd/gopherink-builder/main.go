package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const generatedPluginFile = "zz_components_autoload.go"

type discoveredPackage struct {
	Directory  string
	ImportPath string
	ModuleDir  string
}

func main() {
	var (
		output      string
		tags        string
		ldflags     string
		listOnly    bool
		verbose     bool
		trimPath    bool
		raceEnabled bool
	)
	flag.StringVar(&output, "o", "gopherink", "output binary path")
	flag.StringVar(&tags, "tags", "", "comma-separated Go build tags")
	flag.StringVar(&ldflags, "ldflags", "", "arguments to pass to go build -ldflags")
	flag.BoolVar(&listOnly, "list", false, "list discovered plugins and themes without building")
	flag.BoolVar(&verbose, "v", false, "print packages while building")
	flag.BoolVar(&trimPath, "trimpath", false, "remove local file system paths from the binary")
	flag.BoolVar(&raceEnabled, "race", false, "enable the Go race detector")
	flag.Parse()

	if err := run(output, tags, ldflags, listOnly, verbose, trimPath, raceEnabled); err != nil {
		fmt.Fprintln(os.Stderr, "gopherink-builder:", err)
		os.Exit(1)
	}
}

func run(output, tags, ldflags string, listOnly, verbose, trimPath, raceEnabled bool) error {
	root, err := projectRoot()
	if err != nil {
		return err
	}
	rootModule, err := modulePath(root)
	if err != nil {
		return fmt.Errorf("read root module: %w", err)
	}
	plugins, err := discoverPackages(root, rootModule, "plugins", false)
	if err != nil {
		return err
	}
	themes, err := discoverPackages(root, rootModule, "themes", true)
	if err != nil {
		return err
	}
	for _, item := range plugins {
		fmt.Printf("plugin: %s (%s)\n", item.Directory, item.ImportPath)
	}
	for _, item := range themes {
		fmt.Printf("theme: %s (%s)\n", item.Directory, item.ImportPath)
	}
	if listOnly {
		return nil
	}

	buildDir := filepath.Join(root, ".gopherink-build")
	if err := os.RemoveAll(buildDir); err != nil {
		return fmt.Errorf("clean temporary build directory: %w", err)
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return fmt.Errorf("create temporary build directory: %w", err)
	}
	defer os.RemoveAll(buildDir)

	generatedPath := filepath.Join(root, "cmd", "gopherink", generatedPluginFile)
	for _, name := range []string{generatedPluginFile, "zz_plugins_autoload.go"} {
		stalePath := filepath.Join(root, "cmd", "gopherink", name)
		if err := os.Remove(stalePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale generated component imports: %w", err)
		}
	}
	defer os.Remove(generatedPath)
	if err := writeComponentImports(generatedPath, plugins, themes); err != nil {
		return err
	}

	workspacePath := filepath.Join(buildDir, "go.work")
	if err := writeWorkspace(workspacePath, root, plugins, themes); err != nil {
		return err
	}

	if !filepath.IsAbs(output) {
		output = filepath.Join(root, output)
	}
	args := []string{"build", "-o", output}
	if strings.TrimSpace(tags) != "" {
		args = append(args, "-tags", tags)
	}
	if verbose {
		args = append(args, "-v")
	}
	if trimPath {
		args = append(args, "-trimpath")
	}
	if raceEnabled {
		args = append(args, "-race")
	}
	if strings.TrimSpace(ldflags) != "" {
		args = append(args, "-ldflags", ldflags)
	}
	args = append(args, "./cmd/gopherink")

	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = replaceEnv(os.Environ(), "GOWORK", workspacePath)
	cmd.Env = replaceEnv(cmd.Env, "GOFLAGS", "")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build failed: %w", err)
	}
	fmt.Printf("built: %s\n", output)
	return nil
}

func projectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if info, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil && !info.IsDir() {
			if info, pluginErr := os.Stat(filepath.Join(dir, "core", "plugin")); pluginErr == nil && info.IsDir() {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("GopherInk project root not found")
		}
		dir = parent
	}
}

func discoverPackages(root, rootModule, folder string, requireAtLeastOne bool) ([]discoveredPackage, error) {
	baseDir := filepath.Join(root, folder)
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) && !requireAtLeastOne {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s directory: %w", folder, err)
	}
	packages := make([]discoveredPackage, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		dir := filepath.Join(baseDir, name)
		hasSource, err := hasRootGoSource(dir)
		if err != nil {
			return nil, fmt.Errorf("inspect %s %s: %w", strings.TrimSuffix(folder, "s"), name, err)
		}
		if !hasSource {
			continue
		}

		item := discoveredPackage{Directory: name}
		if info, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil && !info.IsDir() {
			item.ImportPath, err = modulePath(dir)
			if err != nil {
				return nil, fmt.Errorf("read %s module %s: %w", strings.TrimSuffix(folder, "s"), name, err)
			}
			item.ModuleDir = dir
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("inspect %s module %s: %w", strings.TrimSuffix(folder, "s"), name, statErr)
		} else {
			item.ImportPath = strings.TrimRight(rootModule, "/") + "/" + folder + "/" + name
		}
		packages = append(packages, item)
	}
	sort.Slice(packages, func(i, j int) bool {
		return packages[i].ImportPath < packages[j].ImportPath
	})
	if requireAtLeastOne && len(packages) == 0 {
		return nil, fmt.Errorf("%s directory must contain at least one buildable package", folder)
	}
	return packages, nil
}

func hasRootGoSource(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type().IsRegular() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			return true, nil
		}
	}
	return false, nil
}

func modulePath(dir string) (string, error) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Path}}")
	cmd.Dir = dir
	cmd.Env = replaceEnv(os.Environ(), "GOWORK", "off")
	cmd.Env = replaceEnv(cmd.Env, "GOFLAGS", "")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("%s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	path := strings.TrimSpace(string(out))
	if path == "" || path == "command-line-arguments" {
		return "", fmt.Errorf("module path is empty")
	}
	return path, nil
}

func writeComponentImports(path string, plugins, themes []discoveredPackage) error {
	var source bytes.Buffer
	source.WriteString("// Code generated by gopherink-builder; DO NOT EDIT.\n\n")
	source.WriteString("package main\n\n")
	if len(plugins)+len(themes) > 0 {
		source.WriteString("import (\n")
		for _, item := range plugins {
			source.WriteString("\t_ ")
			source.WriteString(strconv.Quote(item.ImportPath))
			source.WriteByte('\n')
		}
		for _, item := range themes {
			source.WriteString("\t_ ")
			source.WriteString(strconv.Quote(item.ImportPath))
			source.WriteByte('\n')
		}
		source.WriteString(")\n")
	}
	formatted, err := format.Source(source.Bytes())
		if err != nil {
			return fmt.Errorf("format generated component imports: %w", err)
		}
		if err := os.WriteFile(path, formatted, 0o644); err != nil {
			return fmt.Errorf("write generated component imports: %w", err)
		}
		return nil
}

func writeWorkspace(path, root string, packageGroups ...[]discoveredPackage) error {
	version, err := goVersion(root)
	if err != nil {
		return err
	}
	workspaceDir := filepath.Dir(path)
	modules := []string{root}
	seen := map[string]bool{filepath.Clean(root): true}
	for _, packages := range packageGroups {
		for _, item := range packages {
			if item.ModuleDir == "" {
				continue
			}
			clean := filepath.Clean(item.ModuleDir)
			if !seen[clean] {
				seen[clean] = true
				modules = append(modules, clean)
			}
		}
	}
	sort.Strings(modules)

	var work strings.Builder
	work.WriteString("go ")
	work.WriteString(version)
	work.WriteString("\n\nuse (\n")
	for _, module := range modules {
		rel, err := filepath.Rel(workspaceDir, module)
		if err != nil {
			return fmt.Errorf("resolve workspace module %s: %w", module, err)
		}
		work.WriteString("\t")
		work.WriteString(strconv.Quote(filepath.ToSlash(rel)))
		work.WriteByte('\n')
	}
	work.WriteString(")\n")
	if err := os.WriteFile(path, []byte(work.String()), 0o644); err != nil {
		return fmt.Errorf("write temporary workspace: %w", err)
	}
	return nil
}

func goVersion(root string) (string, error) {
	cmd := exec.Command("go", "env", "GOVERSION")
	cmd.Dir = root
	cmd.Env = replaceEnv(os.Environ(), "GOWORK", "off")
	cmd.Env = replaceEnv(cmd.Env, "GOFLAGS", "")
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message != "" {
			return "", fmt.Errorf("read Go version: %w: %s", err, message)
		}
		return "", fmt.Errorf("read Go version: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", fmt.Errorf("Go version is empty")
	}
	version := strings.TrimPrefix(fields[0], "go")
	if version == "" {
		return "", fmt.Errorf("Go version is empty")
	}
	return version, nil
}

func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			if !replaced {
				out = append(out, prefix+value)
				replaced = true
			}
			continue
		}
		out = append(out, item)
	}
	if !replaced {
		out = append(out, prefix+value)
	}
	return out
}
