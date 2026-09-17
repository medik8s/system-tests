package far

import (
	"context"
	"runtime"
	"testing"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	_ "github.com/medik8s/system-tests/tests/far-operator/tests"
	fartests "github.com/medik8s/system-tests/tests/far-operator/tests"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/reporter"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"
)

var _, currentFile, _, _ = runtime.Caller(0)

func TestFAR(t *testing.T) {
	_, reporterConfig := GinkgoConfiguration()
	reporterConfig.JUnitReport = Medik8sConfig.GetJunitReportPath(currentFile)

	RegisterFailHandler(Fail)
	RunSpecs(t, "FAR", Label(farparams.Labels...), reporterConfig)
}

var _ = JustAfterEach(func() {
	reporter.ReportIfFailed(
		CurrentSpecReport(), currentFile, farparams.ReporterNamespacesToDump, farparams.ReporterCRDsToDump)
})

var _ = AfterSuite(func() {
	Expect(fartests.CleanupCredentialsSecret(context.Background(), APIClient)).To(Succeed())
})

var _ = ReportAfterSuite("", func(report Report) {
	reportxml.Create(
		report, Medik8sConfig.GetReportPath(), Medik8sConfig.TCPrefix)
})
