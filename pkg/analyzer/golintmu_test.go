package analyzer_test

import (
	"testing"

	"github.com/akerouanton/golintmu/pkg/analyzer"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestBasic(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer.Analyzer, "basic")
}

func TestDeferPatterns(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer.Analyzer, "defer_patterns")
}

func TestFalsePositives(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer.Analyzer, "false_positives")
}

func TestBranchPatterns(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer.Analyzer, "branch_patterns")
}

func TestInterprocedural(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer.Analyzer, "interprocedural")
}

func TestDoubleLock(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer.Analyzer, "double_lock")
}

func TestConcurrent(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer.Analyzer, "concurrent")
}

func TestAnnotations(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer.Analyzer, "annotations")
}
