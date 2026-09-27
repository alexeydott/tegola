// Command check-dependencies checks the local replacement inventory and a
// downstream module using the documented checkout-based dependency setup.
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type moduleVersion struct {
	Path    string
	Version string
}

type moduleFile struct {
	Module  moduleVersion
	Go      string
	Replace []struct {
		Old moduleVersion
		New moduleVersion
	}
}

var forks = map[string]string{
	"github.com/go-spatial/geom": "./third_party/go-spatial/geom",
	"github.com/go-spatial/proj": "./third_party/go-spatial/proj",
}

func main() {
	if err := check(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func goCommand(dir string, offline bool, args ...string) ([]byte, error) {
	goName := "go"
	if runtime.GOOS == "windows" {
		goName += ".exe"
	}
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", goName), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	if offline {
		cmd.Env = append(cmd.Env, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("go %v in %s: %w\n%s", args, dir, err, output)
	}
	return output, nil
}

func readModule(dir string) (moduleFile, error) {
	var mod moduleFile
	output, err := goCommand(dir, false, "mod", "edit", "-json")
	if err != nil {
		return mod, err
	}
	if err := json.Unmarshal(output, &mod); err != nil {
		return mod, fmt.Errorf("read module in %s: %w", dir, err)
	}
	return mod, nil
}

func check() error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	mod, err := readModule(root)
	if err != nil {
		return err
	}
	if mod.Module.Path != "github.com/go-spatial/tegola" {
		return fmt.Errorf("run this check from the Tegola repository root")
	}
	if len(mod.Replace) != len(forks) {
		return fmt.Errorf("root replacement inventory changed: review third_party/README.md and this check")
	}
	for _, replacement := range mod.Replace {
		want, ok := forks[replacement.Old.Path]
		if !ok || replacement.New.Path != want || replacement.Old.Version != "" || replacement.New.Version != "" {
			return fmt.Errorf("unexpected root replacement: %+v", replacement)
		}
	}
	found := 0
	err = filepath.WalkDir(filepath.Join(root, "third_party"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "go.mod" {
			return nil
		}
		nested, err := readModule(filepath.Dir(path))
		if err != nil {
			return err
		}
		want, ok := forks[nested.Module.Path]
		if !ok || filepath.Join(root, filepath.FromSlash(want), "go.mod") != path || len(nested.Replace) != 0 {
			return fmt.Errorf("unexpected nested module or replacement in %s", path)
		}
		found++
		return nil
	})
	if err != nil {
		return err
	}
	if found != len(forks) {
		return fmt.Errorf("found %d nested modules, want %d", found, len(forks))
	}
	fmt.Println("Replacement inventory: two root local forks, no nested replacements")

	consumer, err := os.MkdirTemp("", "tegola-consumer-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(consumer); err != nil {
			fmt.Fprintf(os.Stderr, "remove temporary consumer %s: %v\n", consumer, err)
		}
	}()
	goMod := fmt.Sprintf("module example.com/tegola-consumer\n\ngo %s\n\nrequire github.com/go-spatial/tegola v0.0.0\n", mod.Go)
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), []byte(goMod), 0600); err != nil {
		return err
	}
	replacements := []string{"mod", "edit", "-replace=github.com/go-spatial/tegola=" + filepath.ToSlash(root)}
	for module, relative := range forks {
		replacements = append(replacements, "-replace="+module+"="+filepath.ToSlash(filepath.Join(root, filepath.FromSlash(relative))))
	}
	if _, err := goCommand(consumer, false, replacements...); err != nil {
		return err
	}
	for _, name := range []string{"consumer.go", "consumer_test.go"} {
		source, err := os.ReadFile(filepath.Join(root, "ci", "check-dependencies", "testdata", name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(consumer, name), source, 0600); err != nil {
			return err
		}
	}
	for _, args := range [][]string{
		{"mod", "tidy"},
		{"test", "-mod=readonly", "-count=1", "./..."},
		{"list", "-mod=mod", "-m", "all"},
		{"mod", "verify"},
		{"mod", "vendor"},
	} {
		output, err := goCommand(consumer, false, args...)
		if err != nil {
			return err
		}
		fmt.Printf("Consumer go %v: passed\n%s", args, output)
	}
	for _, args := range [][]string{
		{"build", "-mod=vendor", "./..."},
		{"test", "-mod=vendor", "-count=1", "./..."},
	} {
		output, err := goCommand(consumer, true, args...)
		if err != nil {
			return err
		}
		fmt.Printf("Offline consumer go %v: passed\n%s", args, output)
	}
	return nil
}
