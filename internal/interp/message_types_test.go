package interp_test

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// TestSentTypesAreRegistered shows every Director message type this package sends is a constant of
// store.MessageTypes, the registry the wake line and DirectorUnread count by: each notify and directorMessage
// call names its type as store.<constant>, never a literal or a local name.
func TestSentTypesAreRegistered(t *testing.T) {
	consts := storeConstants(t)
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sites := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if fd, ok := n.(*ast.FuncDecl); ok && fd.Name.Name == "notify" {
				return false // it forwards its caller's type, checked at each call
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			arg := -1
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if fn.Sel.Name == "notify" {
					arg = 1
				}
			case *ast.Ident:
				if fn.Name == "directorMessage" {
					arg = 3
				}
			}
			if arg < 0 || len(call.Args) <= arg {
				return true
			}
			sites++
			at := fset.Position(call.Pos())
			sel, ok := call.Args[arg].(*ast.SelectorExpr)
			if x, isIdent := selectorX(sel); !ok || !isIdent || x.Name != "store" {
				t.Errorf("%s: the message type %s is not a store constant", at, source(fset, call.Args[arg]))
				return true
			}
			v, ok := consts[sel.Sel.Name]
			if _, registered := store.MessageTypes[v]; !ok || !registered {
				t.Errorf("%s: store.%s (%q) is not in store.MessageTypes", at, sel.Sel.Name, v)
			}
			return true
		})
	}
	if sites == 0 {
		t.Fatal("found no notify or directorMessage call")
	}
}

// storeConstants reads the string constants the store package declares, by name.
func storeConstants(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "store", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, err := strconv.Unquote(lit.Value); err == nil {
							out[id.Name] = v
						}
					}
				}
			}
		}
	}
	return out
}

func selectorX(sel *ast.SelectorExpr) (*ast.Ident, bool) {
	if sel == nil {
		return nil, false
	}
	x, ok := sel.X.(*ast.Ident)
	return x, ok
}

// source prints an expression's source for a failure message.
func source(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, e); err != nil {
		return "?"
	}
	return b.String()
}
