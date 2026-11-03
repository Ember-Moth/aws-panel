package arch

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDependencyBoundaries(t *testing.T) {
	root := filepath.Join("..", "..")
	prefix := "github.com/Ember-Moth/aws-panel/"
	err := filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() {
			if item.Name() == ".git" || item.Name() == ".local" || item.Name() == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		dir := filepath.ToSlash(filepath.Dir(relative))
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			value, _ := strconv.Unquote(spec.Path.Value)
			imported := strings.TrimPrefix(value, prefix)
			if imported == "internal/app" && dir != "cmd" {
				t.Errorf("%s imports the composition root", relative)
			}
			if strings.HasPrefix(dir, "pkg/") && strings.HasPrefix(imported, "internal/") {
				t.Errorf("%s: shared package depends on internal code", relative)
			}
			if strings.HasPrefix(dir, "internal/infra/") &&
				(strings.HasPrefix(imported, "internal/module/") || strings.HasPrefix(imported, "internal/app/")) {
				t.Errorf("%s: infrastructure depends on application code", relative)
			}
			if strings.HasPrefix(dir, "internal/module/") {
				parts := strings.Split(dir, "/")
				own := "internal/module/" + parts[2]
				if strings.HasPrefix(imported, "internal/app") {
					t.Errorf("%s imports app", relative)
				}
				if strings.Contains(dir, "/contract") && strings.HasPrefix(imported, "internal/module/") && !strings.HasPrefix(imported, own+"/") {
					t.Errorf("%s: cross-module DTO dependency", relative)
				}
				if !strings.Contains(dir, "/transport/") && strings.Contains(imported, "/transport/") {
					t.Errorf("%s: module core depends on transport", relative)
				}
			}
			if strings.HasPrefix(dir, "internal/transport/task") &&
				(strings.HasPrefix(imported, "internal/repository") || value == "gorm.io/gorm") {
				t.Errorf("%s: task handler accesses persistence", relative)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
