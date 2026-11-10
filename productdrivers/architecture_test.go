package productdrivers_test

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/quant-store/quantstore-cli"

var allowedPureStdlib = map[string]struct{}{
	"context": {}, "encoding/json": {}, "net/url": {}, "sort": {}, "strconv": {}, "strings": {},
}

func TestProductDriverSourceCapabilities(t *testing.T) {
	root := moduleRoot(t)
	for _, product := range []string{"coinglass", "twittermonitor", "smartmoneypro"} {
		productDir := filepath.Join(root, "productdrivers", product)
		err := filepath.WalkDir(productDir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imported := range file.Imports {
				importPath, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					return err
				}
				if _, allowed := allowedPureStdlib[importPath]; allowed {
					continue
				}
				if importPath == modulePath+"/driverapi" {
					continue
				}
				return fmt.Errorf("%s imports denied capability %q", path, importPath)
			}
			if importsC(file) {
				return fmt.Errorf("%s enables cgo", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestProductDriverModuleDependencyClosure(t *testing.T) {
	root := moduleRoot(t)
	for _, product := range []string{"coinglass", "twittermonitor", "smartmoneypro"} {
		command := exec.Command("go", "list", "-deps", "-json", "./productdrivers/"+product)
		command.Dir = root
		output, err := command.Output()
		if err != nil {
			t.Fatalf("go list %s: %v", product, err)
		}
		decoder := json.NewDecoder(strings.NewReader(string(output)))
		for {
			var item struct {
				ImportPath string
				Standard   bool
			}
			if err := decoder.Decode(&item); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if item.Standard || item.ImportPath == modulePath+"/driverapi" || item.ImportPath == modulePath+"/productdrivers/"+product {
				continue
			}
			t.Fatalf("%s reaches denied dependency %q", product, item.ImportPath)
		}
	}
}

func TestProductDriverCannotImportCoreInternal(t *testing.T) {
	root := moduleRoot(t)
	fixture := t.TempDir()
	goMod := "module " + modulePath + "/productdriverscompileguard\n\ngo 1.23.0\n\nrequire " + modulePath + " v0.0.0\nreplace " + modulePath + " => " + root + "\n"
	source := "package compileguard\n\nimport _ \"" + modulePath + "/core/internal/version\"\n"
	if err := os.WriteFile(filepath.Join(fixture, "go.mod"), []byte(goMod), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "guard.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./...")
	command.Dir = fixture
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("product driver fixture unexpectedly imported core/internal")
	}
	if !strings.Contains(string(output), "use of internal package") || !strings.Contains(string(output), "not allowed") {
		t.Fatalf("unexpected compiler failure:\n%s", output)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(filename))
}

func importsC(file *ast.File) bool {
	for _, imported := range file.Imports {
		if imported.Path.Value == `"C"` {
			return true
		}
	}
	return false
}
