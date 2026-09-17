package reporter

import (
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"k8s.io/apimachinery/pkg/runtime"
)

var reporterSchemes = []clients.SchemeAttacher{
	clients.SetScheme,
}

func setReporterSchemes(scheme *runtime.Scheme) error {
	for _, schemeAttacher := range reporterSchemes {
		if err := schemeAttacher(scheme); err != nil {
			return err
		}
	}

	return nil
}
