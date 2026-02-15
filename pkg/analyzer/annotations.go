package analyzer

import (
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// annotations holds parsed comment directives for the current package.
type annotations struct {
	concurrent map[*ssa.Function]bool // functions marked //mu:concurrent
	ignored    map[*ssa.Function]bool // functions marked //mu:ignore
	nolint     map[string]map[int]bool // filename → set of suppressed line numbers
}

// parseAnnotations scans all comment groups in the package's AST files and
// populates ctx.annotations with directive information.
func (ctx *passContext) parseAnnotations() {
	ann := &annotations{
		concurrent: make(map[*ssa.Function]bool),
		ignored:    make(map[*ssa.Function]bool),
		nolint:     make(map[string]map[int]bool),
	}

	fset := ctx.pass.Fset

	for _, file := range ctx.pass.Files {
		// Build a list of func decls in declaration order for this file.
		var funcDecls []*ast.FuncDecl
		for _, decl := range file.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				funcDecls = append(funcDecls, fd)
			}
		}

		for _, cg := range file.Comments {
			for _, comment := range cg.List {
				text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))

				switch {
				case text == "mu:concurrent" || strings.HasPrefix(text, "mu:concurrent "):
					if fn := ctx.findFuncForComment(fset, funcDecls, comment.Pos()); fn != nil {
						ann.concurrent[fn] = true
					}

				case text == "mu:ignore" || strings.HasPrefix(text, "mu:ignore "):
					if fn := ctx.findFuncForComment(fset, funcDecls, comment.Pos()); fn != nil {
						ann.ignored[fn] = true
					}

				case text == "mu:nolint" || strings.HasPrefix(text, "mu:nolint "):
					pos := fset.Position(comment.Pos())
					filename := pos.Filename
					suppressedLine := pos.Line + 1
					if ann.nolint[filename] == nil {
						ann.nolint[filename] = make(map[int]bool)
					}
					ann.nolint[filename][suppressedLine] = true
				}
			}
		}
	}

	ctx.annotations = ann
}

// findFuncForComment finds the SSA function corresponding to the function
// declaration that contains or immediately follows the comment at commentPos.
func (ctx *passContext) findFuncForComment(fset *token.FileSet, funcDecls []*ast.FuncDecl, commentPos token.Pos) *ssa.Function {
	commentLine := fset.Position(commentPos).Line

	// Find the function decl whose start line is on or just after the comment line.
	// The comment should be either inside the func or immediately above it.
	var best *ast.FuncDecl
	for _, fd := range funcDecls {
		fdLine := fset.Position(fd.Pos()).Line
		// Comment is on the line immediately before or on the same line as the func decl.
		if fdLine >= commentLine && fdLine <= commentLine+1 {
			best = fd
			break
		}
		// Comment is inside the function body.
		if fd.Body != nil && commentPos >= fd.Pos() && commentPos <= fd.Body.End() {
			best = fd
			break
		}
	}

	if best == nil {
		return nil
	}

	return ctx.astFuncToSSA(best)
}

// astFuncToSSA maps an AST FuncDecl to its SSA function by position matching.
func (ctx *passContext) astFuncToSSA(fd *ast.FuncDecl) *ssa.Function {
	for _, fn := range ctx.srcFuncs {
		if fn.Pos() == fd.Name.Pos() {
			return fn
		}
	}
	return nil
}

// isSuppressed returns true if reporting should be suppressed for the given
// function and position, either because the function has //mu:ignore or the
// line has //mu:nolint on the preceding line.
func (ctx *passContext) isSuppressed(fn *ssa.Function, pos token.Pos) bool {
	if ctx.annotations == nil {
		return false
	}
	if ctx.annotations.ignored[fn] {
		return true
	}
	if pos.IsValid() {
		p := ctx.pass.Fset.Position(pos)
		if lines, ok := ctx.annotations.nolint[p.Filename]; ok {
			if lines[p.Line] {
				return true
			}
		}
	}
	return false
}
