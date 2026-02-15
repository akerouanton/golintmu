package analyzer

import (
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// computeConcurrentContext detects concurrent entrypoints and computes the set
// of functions reachable from them. If no entrypoints are detected, all
// functions are treated as concurrent (conservative fallback).
func (ctx *passContext) computeConcurrentContext() {
	entrypoints := ctx.detectConcurrentEntrypoints()
	if len(entrypoints) == 0 {
		ctx.concurrentFuncs = nil // nil = all concurrent
		return
	}

	// BFS reachability from entrypoints through the call graph.
	forward := ctx.buildForwardCallGraph()
	reachable := make(map[*ssa.Function]bool)
	queue := make([]*ssa.Function, 0, len(entrypoints))
	for fn := range entrypoints {
		reachable[fn] = true
		queue = append(queue, fn)
	}
	for head := 0; head < len(queue); head++ {
		fn := queue[head]
		for _, callee := range forward[fn] {
			if !reachable[callee] {
				reachable[callee] = true
				queue = append(queue, callee)
			}
		}
	}

	ctx.concurrentFuncs = reachable
}

// isConcurrent returns true if fn runs in a concurrent context.
// When concurrentFuncs is nil (no entrypoints detected), all functions are concurrent.
func (ctx *passContext) isConcurrent(fn *ssa.Function) bool {
	if ctx.concurrentFuncs == nil {
		return true
	}
	return ctx.concurrentFuncs[fn]
}

// detectConcurrentEntrypoints scans source functions for concurrent patterns:
// - Functions launched via `go` statements
// - ServeHTTP methods with the correct signature
// - Functions passed to http.HandleFunc / (*http.ServeMux).HandleFunc
func (ctx *passContext) detectConcurrentEntrypoints() map[*ssa.Function]bool {
	entrypoints := make(map[*ssa.Function]bool)

	for _, fn := range ctx.srcFuncs {
		// Check if this function is a ServeHTTP method.
		if isServeHTTPMethod(fn) {
			entrypoints[fn] = true
		}

		if len(fn.Blocks) == 0 {
			continue
		}

		for _, block := range fn.Blocks {
			for _, instr := range block.Instrs {
				switch inst := instr.(type) {
				case *ssa.Go:
					if target := extractGoTarget(inst); target != nil {
						entrypoints[target] = true
					}
				case *ssa.Call:
					if target := extractHandlerFuncTarget(inst); target != nil {
						entrypoints[target] = true
					}
				}
			}
		}
	}

	// Merge in functions annotated with //mu:concurrent.
	if ctx.annotations != nil {
		for fn := range ctx.annotations.concurrent {
			entrypoints[fn] = true
		}
	}

	return entrypoints
}

// buildForwardCallGraph builds caller → []callee from recorded call sites.
func (ctx *passContext) buildForwardCallGraph() map[*ssa.Function][]*ssa.Function {
	forward := make(map[*ssa.Function][]*ssa.Function)
	for _, cs := range ctx.callSites {
		forward[cs.Caller] = append(forward[cs.Caller], cs.Callee)
	}
	return forward
}

// isServeHTTPMethod returns true if fn is a method named ServeHTTP with
// signature (http.ResponseWriter, *http.Request).
func isServeHTTPMethod(fn *ssa.Function) bool {
	if fn.Name() != "ServeHTTP" {
		return false
	}
	sig := fn.Signature
	params := sig.Params()
	// Signature.Params() does not include the receiver.
	// ServeHTTP(w http.ResponseWriter, r *http.Request) has 2 params.
	if params.Len() != 2 {
		return false
	}
	return isHTTPResponseWriter(params.At(0).Type()) && isHTTPRequestPtr(params.At(1).Type())
}

// extractGoTarget extracts the function launched by a `go` statement.
func extractGoTarget(goInstr *ssa.Go) *ssa.Function {
	common := goInstr.Common()
	// Static callee: go foo() or go obj.Method()
	if callee := common.StaticCallee(); callee != nil {
		return callee
	}
	// go func() { ... }() — the value is a *ssa.MakeClosure
	if mc, ok := common.Value.(*ssa.MakeClosure); ok {
		if fn, ok := mc.Fn.(*ssa.Function); ok {
			return fn
		}
	}
	// go boundMethod() — the value is a *ssa.Function directly
	if fn, ok := common.Value.(*ssa.Function); ok {
		return fn
	}
	return nil
}

// extractHandlerFuncTarget extracts function arguments passed to
// http.HandleFunc or (*http.ServeMux).HandleFunc.
func extractHandlerFuncTarget(call *ssa.Call) *ssa.Function {
	common := call.Common()
	callee := common.StaticCallee()
	if callee == nil {
		return nil
	}

	// Match net/http.HandleFunc (package-level) or (*ServeMux).HandleFunc
	if !isHTTPHandleFunc(callee) {
		return nil
	}

	// The handler argument is the last argument.
	args := common.Args
	if len(args) == 0 {
		return nil
	}
	handlerArg := args[len(args)-1]

	// Direct function reference.
	if fn, ok := handlerArg.(*ssa.Function); ok {
		return fn
	}
	// MakeClosure wrapping a function literal.
	if mc, ok := handlerArg.(*ssa.MakeClosure); ok {
		if fn, ok := mc.Fn.(*ssa.Function); ok {
			return fn
		}
	}
	return nil
}

// isHTTPHandleFunc returns true if fn is net/http.HandleFunc or
// (*net/http.ServeMux).HandleFunc.
func isHTTPHandleFunc(fn *ssa.Function) bool {
	if fn.Name() != "HandleFunc" {
		return false
	}
	pkg := fn.Package()
	if pkg == nil || pkg.Pkg == nil {
		return false
	}
	return pkg.Pkg.Path() == "net/http"
}

// isHTTPResponseWriter returns true if t is net/http.ResponseWriter.
func isHTTPResponseWriter(t types.Type) bool {
	// http.ResponseWriter is a named interface type.
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "net/http" && obj.Name() == "ResponseWriter"
}

// isHTTPRequestPtr returns true if t is *net/http.Request.
func isHTTPRequestPtr(t types.Type) bool {
	ptr, ok := t.(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "net/http" && obj.Name() == "Request"
}
