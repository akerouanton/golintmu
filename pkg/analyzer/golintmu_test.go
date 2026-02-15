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
