package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// A handler that answers with one book or author straight through writeJSON
// skips the image proxy, and a stored cover (bindery-cover:) only loads
// through it (#3146). writeBook and writeAuthor are the way to answer with one.
//
// This is a syntactic guard, not a type check: it catches the slip that was
// made, a book or author variable (or an add result's) handed to writeJSON.
// The per endpoint tests in this package prove the behaviour.
func TestHandlersAnswerBooksAndAuthorsThroughTheProxyWriters(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	raw := map[string]bool{
		"book": true, "author": true, "existing": true, "canonical": true,
		"res.Book": true, "res.Author": true,
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "writeJSON" || len(call.Args) != 3 {
				return true
			}
			if name := exprName(call.Args[2]); raw[name] {
				t.Errorf("%s: writeJSON(..., %s) returns the raw cover reference; use writeBook or writeAuthor",
					fset.Position(call.Pos()), name)
			}
			return true
		})
	}
}

func exprName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprName(v.X) + "." + v.Sel.Name
	case *ast.UnaryExpr:
		return exprName(v.X)
	}
	return ""
}
