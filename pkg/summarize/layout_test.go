package summarize

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestSummarizerImplementationsUseDedicatedFiles(t *testing.T) {
	fileset := token.NewFileSet()
	packages, err := parser.ParseDir(fileset, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	implementations := make(map[string]string)
	for filename, file := range packages["summarize"].Files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || function.Name.Name != "Summarize" {
				continue
			}
			receiver := receiverName(function.Recv.List[0].Type)
			implementations[receiver] = filepath.Base(filename)
		}
	}

	want := map[string]string{
		"LLM":       "llm.go",
		"Heuristic": "heuristic.go",
	}
	for receiver, filename := range want {
		if implementations[receiver] != filename {
			t.Errorf("%s.Summarize file = %q, want %q", receiver, implementations[receiver], filename)
		}
	}
}

func receiverName(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.StarExpr:
		return receiverName(expression.X)
	default:
		return ""
	}
}
