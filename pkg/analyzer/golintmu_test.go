package analyzer_test

import (
	"testing"

	"github.com/akerouanton/golintmu/pkg/analyzer"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"
)

// singlePkgAnalyzer wraps the real analyzer without FactTypes. This prevents
// fact export in single-package tests, avoiding the need for fact expectations
// in every test file. Cross-package tests use the real Analyzer which has FactTypes.
var singlePkgAnalyzer = &analysis.Analyzer{
	Name:     analyzer.Analyzer.Name,
	Doc:      analyzer.Analyzer.Doc,
	Run:      analyzer.Analyzer.Run,
	Requires: analyzer.Analyzer.Requires,
}

func TestBasic(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "basic")
}

func TestDeferPatterns(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "defer_patterns")
}

func TestFalsePositives(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "false_positives")
}

func TestBranchPatterns(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "branch_patterns")
}

func TestInterprocedural(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "interprocedural")
}

func TestDoubleLock(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "double_lock")
}

func TestConcurrent(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "concurrent")
}

func TestAnnotations(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "annotations")
}

func TestRWMutex(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "rwmutex")
}

func TestEmbedded(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "embedded")
}

func TestLockOrdering(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "lock_ordering")
}

func TestUnlockOfUnlocked(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "unlock_of_unlocked")
}

func TestLockLeak(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "lock_leak")
}

func TestReturnWhileLocked(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "return_while_locked")
}

func TestDeferredLock(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "deferred_lock")
}

func TestMultiReturn(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "multi_return")
}

func TestTandemLocks(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "tandem_locks")
}

func TestConstructorCalls(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, singlePkgAnalyzer, "constructor_calls")
}

func TestCrossPackage(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer.Analyzer, "crosspackage/pkga", "crosspackage/pkgb")
}
