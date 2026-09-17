package medik8sinittools

import (
	"github.com/medik8s/system-tests/tests/internal/inittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sconfig"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
)

var (
	// APIClient provides API access to cluster.
	APIClient *clients.Settings
	// Medik8sConfig provides access to general configuration parameters.
	Medik8sConfig *medik8sconfig.Medik8sConfig
)

// init loads all variables automatically when this package is imported. Once package is imported a user has full
// access to all vars within init function. It is recommended to import this package using dot import.
func init() {
	Medik8sConfig = medik8sconfig.NewMedik8sConfig()
	APIClient = inittools.APIClient
}
