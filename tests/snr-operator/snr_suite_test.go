package snr

import (
	"runtime"
	"testing"

	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/reporter"
	"github.com/medik8s/system-tests/tests/snr-operator/internal/snrparams"
	_ "github.com/medik8s/system-tests/tests/snr-operator/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"
)

var _, currentFile, _, _ = runtime.Caller(0)

func TestSNR(t *testing.T) {
	_, reporterConfig := GinkgoConfiguration()
	reporterConfig.JUnitReport = Medik8sConfig.GetJunitReportPath(currentFile)

	RegisterFailHandler(Fail)
	RunSpecs(t, "SNR", Label(snrparams.Labels...), reporterConfig)
}

var _ = JustAfterEach(func() {
	reporter.ReportIfFailed(
		CurrentSpecReport(), currentFile, snrparams.ReporterNamespacesToDump, snrparams.ReporterCRDsToDump)
})

var _ = ReportAfterSuite("", func(report Report) {
	reportxml.Create(
		report, Medik8sConfig.GetReportPath(), Medik8sConfig.TCPrefix)
})
