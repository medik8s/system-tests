package medik8sconfig

import (
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/kelseyhightower/envconfig"
	"github.com/medik8s/system-tests/tests/internal/config"
	"gopkg.in/yaml.v2"
)

const (
	// PathToDefaultParamsFile path to config file with default medik8s parameters.
	PathToDefaultParamsFile = "./default.yaml"
)

// Medik8sConfig type keeps medik8s configuration.
type Medik8sConfig struct {
	*config.GeneralConfig
}

// NewMedik8sConfig returns instance of Medik8s config type.
func NewMedik8sConfig() *Medik8sConfig {
	log.Print("Creating new Medik8sConfig struct")

	var medik8sConf Medik8sConfig

	medik8sConf.GeneralConfig = config.NewConfig()

	_, filename, _, _ := runtime.Caller(0)
	baseDir := filepath.Dir(filename)
	confFile := filepath.Join(baseDir, PathToDefaultParamsFile)

	err := readFile(&medik8sConf, confFile)
	if err != nil {
		log.Printf("Error to read config file %s", confFile)

		return nil
	}

	err = readEnv(&medik8sConf)
	if err != nil {
		log.Print("Error to read environment variables")

		return nil
	}

	return &medik8sConf
}

func readFile(medik8sConfig *Medik8sConfig, cfgFile string) error {
	openedCfgFile, err := os.Open(cfgFile)
	if err != nil {
		return err
	}

	defer func() {
		_ = openedCfgFile.Close()
	}()

	decoder := yaml.NewDecoder(openedCfgFile)

	err = decoder.Decode(&medik8sConfig)
	if err != nil {
		return err
	}

	return nil
}

func readEnv(medik8sConfig *Medik8sConfig) error {
	err := envconfig.Process("", medik8sConfig)
	if err != nil {
		return err
	}

	return nil
}
