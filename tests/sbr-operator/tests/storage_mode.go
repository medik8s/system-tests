package tests

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// StorageConfig carries storage-mode-specific settings for parameterized test suites.
type StorageConfig struct {
	// Name is a short label ("filesystem" or "block") included in Describe descriptions.
	Name string
	// VolumeMode is "" for filesystem (SBRC default) or "Block" for raw block mode.
	VolumeMode string
	// NameSuffix is appended to every SBRC and NHC CR name to prevent collision
	// when both modes run in the same Ginkgo session ("-fs" or "-blk").
	NameSuffix string
}

var (
	// FilesystemMode configures tests to use a CephFS RWX StorageClass (sharedStorageVolumeMode omitted).
	FilesystemMode = StorageConfig{Name: "filesystem", VolumeMode: "", NameSuffix: "-fs"}
	// BlockMode configures tests to use a Ceph RBD StorageClass with sharedStorageVolumeMode=Block.
	BlockMode = StorageConfig{Name: "block", VolumeMode: "Block", NameSuffix: "-blk"}
)

// discoverStorageClass resolves the appropriate StorageClass for the given mode.
// Must be called from within a Ginkgo node (BeforeAll/It); calls Skip when none is found.
func discoverStorageClass(cfg StorageConfig) string {
	if cfg.VolumeMode == "Block" {
		return discoverRBDStorageClass()
	}

	return discoverRWXStorageClass()
}

// buildSBRCWithMode builds an SBRC unstructured object with the given storage class and mode.
// extra fields are merged into spec (e.g. sbrTimeoutSeconds, detectOnlyMode).
func buildSBRCWithMode(name, sc string, cfg StorageConfig, extra map[string]interface{}) *unstructured.Unstructured {
	spec := map[string]interface{}{"sharedStorageClass": sc}
	if cfg.VolumeMode != "" {
		spec["sharedStorageVolumeMode"] = cfg.VolumeMode
	}

	for k, v := range extra {
		spec[k] = v
	}

	return buildSBRC(name, spec)
}

func init() {
	for _, cfg := range []StorageConfig{FilesystemMode, BlockMode} {
		registerDetectOnlyTests(cfg)
		registerSplitBrainTests(cfg)
		registerTransientTests(cfg)
		registerNHCIntegrationTests(cfg)
		registerWriteLossTests(cfg)
		registerWatchdogPathTests(cfg)
		registerLifecycleTests(cfg)
	}
}
