// Package archtest enforces the layer dependency rules at test time.
package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/IlyasFounas/route_after_route/internal/"

// allowed lists, for each layer package, the internal packages it may import.
// Concrete market adapters (market/<x>) and app are never allowed: only the
// composition root (internal/app, cmd/) may know them.
var allowed = map[string][]string{
	"profile":  {},
	"market":   {"profile"},
	"strategy": {"market", "profile"},
	"backtest": {"market", "strategy"},
	"eval":     {"market", "strategy", "backtest"},
	"arbiter":  {},
	"output":   {"profile", "market", "arbiter"},
	"loop":     {"profile", "output"},
	"live":     {"app", "market", "strategy", "eval", "arbiter", "output", "loop", "profile"},
	"gui":      {"live", "output", "loop", "market", "profile", "arbiter", "wallet"},
	"wallet":   {},
}

func TestLayerImports(t *testing.T) {
	for pkg, ok := range allowed {
		dir := filepath.Join("..", pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				dep, internal := strings.CutPrefix(p, module)
				if !internal || dep == pkg {
					continue
				}
				permitted := false
				for _, a := range ok {
					permitted = permitted || dep == a
				}
				if !permitted {
					t.Errorf("%s imports %q: layer %q may only import %v", path, p, pkg, ok)
				}
			}
		}
	}
}
