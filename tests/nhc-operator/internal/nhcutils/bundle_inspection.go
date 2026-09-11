package nhcutils

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcparams"
	"k8s.io/apimachinery/pkg/util/yaml"
)

type bundleCSV struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Version string `json:"version"`
		Install struct {
			Spec struct {
				Deployments []struct {
					Spec struct {
						Template struct {
							Spec struct {
								Containers []struct {
									Name  string `json:"name"`
									Image string `json:"image"`
								} `json:"containers"`
							} `json:"spec"`
						} `json:"template"`
					} `json:"spec"`
				} `json:"deployments"`
			} `json:"spec"`
		} `json:"install"`
	} `json:"spec"`
}

type bundleAnnotations struct {
	Annotations map[string]string `json:"annotations"`
}

type imageInfo struct {
	Digest string `json:"digest"`
}

type inspectedBundle struct {
	Pullspec, Package, Version, ManagerImage string
}

// ResolveAndVerifyCandidateInputs pins and validates the bundle supplied by a PR build.
func ResolveAndVerifyCandidateInputs(
	ctx context.Context, inputs nhcparams.CandidateInputs,
) (nhcparams.CandidateInputs, error) {
	candidate, err := inspectBundle(ctx, inputs.Bundle)
	if err != nil {
		return inputs, fmt.Errorf("inspect candidate bundle: %w", err)
	}

	if err := verifyBundle(candidate, inputs.Package, inputs.Version); err != nil {
		return inputs, fmt.Errorf("candidate bundle: %w", err)
	}

	if err := requireSameImage(ctx, candidate.ManagerImage, inputs.Image); err != nil {
		return inputs, fmt.Errorf("candidate bundle manager: %w", err)
	}

	inputs.Bundle, inputs.Image = candidate.Pullspec, candidate.ManagerImage

	return inputs, nil
}

// ResolveAndVerifyUpgradeInputs performs bundle identity checks inside the Go
// test so local and Prow runs use the same preparation path.
func ResolveAndVerifyUpgradeInputs(
	ctx context.Context, inputs nhcparams.UpgradeInputs,
) (nhcparams.UpgradeInputs, error) {
	candidate, err := inspectBundle(ctx, inputs.CandidateBundle)
	if err != nil {
		return inputs, fmt.Errorf("inspect candidate bundle: %w", err)
	}

	if err := verifyBundle(candidate, inputs.Package, inputs.CandidateVersion); err != nil {
		return inputs, fmt.Errorf("candidate bundle: %w", err)
	}

	if err := requireSameImage(ctx, candidate.ManagerImage, inputs.CandidateImage); err != nil {
		return inputs, fmt.Errorf("candidate bundle manager: %w", err)
	}

	old, err := inspectBundle(ctx, inputs.OldBundle)
	if err != nil {
		return inputs, fmt.Errorf("inspect old NHC bundle: %w", err)
	}

	if err := verifyBundle(old, inputs.Package, inputs.OldVersion); err != nil {
		return inputs, fmt.Errorf("old NHC bundle: %w", err)
	}

	if err := requireSameImage(ctx, old.ManagerImage, inputs.OldImage); err != nil {
		return inputs, fmt.Errorf("old NHC bundle manager: %w", err)
	}

	snr, err := inspectBundle(ctx, inputs.SNRBundle)
	if err != nil {
		return inputs, fmt.Errorf("inspect SNR bundle: %w", err)
	}

	if err := verifyBundle(snr, inputs.SNRPackage, inputs.SNRVersion); err != nil {
		return inputs, fmt.Errorf("SNR bundle: %w", err)
	}

	inputs.CandidateBundle, inputs.CandidateImage = candidate.Pullspec, candidate.ManagerImage
	inputs.OldBundle, inputs.OldImage = old.Pullspec, old.ManagerImage
	inputs.SNRBundle = snr.Pullspec

	return inputs, nil
}

//nolint:funlen,wsl_v5 // Extraction, parsing, and identity validation form one bounded operation.
func inspectBundle(ctx context.Context, pullspec string) (inspectedBundle, error) {
	info, err := inspectImage(ctx, pullspec)
	if err != nil {
		return inspectedBundle{}, err
	}

	repository := strings.Split(pullspec, "@")[0]
	lastSlash := strings.LastIndex(repository, "/")
	if colon := strings.LastIndex(repository, ":"); colon > lastSlash {
		repository = repository[:colon]
	}
	resolved := repository + "@" + info.Digest

	dir, err := os.MkdirTemp("", "nhc-bundle-inspection-")
	if err != nil {
		return inspectedBundle{}, err
	}
	defer os.RemoveAll(dir)

	manifests, metadata := filepath.Join(dir, "manifests"), filepath.Join(dir, "metadata")
	if err := os.MkdirAll(manifests, 0o700); err != nil {
		return inspectedBundle{}, err
	}
	if err := os.MkdirAll(metadata, 0o700); err != nil {
		return inspectedBundle{}, err
	}

	if output, err := RunCommand(ctx, "oc", "image", "extract", resolved,
		"--path", "/manifests/:"+manifests, "--path", "/metadata/:"+metadata, "--confirm"); err != nil {
		return inspectedBundle{}, fmt.Errorf("extract %s: %w\n%s", resolved, err, output)
	}

	csvPaths, err := filepath.Glob(filepath.Join(manifests, "*clusterserviceversion.yaml"))
	if err != nil || len(csvPaths) != 1 {
		return inspectedBundle{}, fmt.Errorf("expected exactly one CSV, found %d", len(csvPaths))
	}

	csvBytes, err := os.ReadFile(csvPaths[0])
	if err != nil {
		return inspectedBundle{}, err
	}
	var csv bundleCSV
	if err := unmarshalYAML(csvBytes, &csv); err != nil {
		return inspectedBundle{}, fmt.Errorf("parse CSV: %w", err)
	}

	annotationBytes, err := os.ReadFile(filepath.Join(metadata, "annotations.yaml"))
	if err != nil {
		return inspectedBundle{}, err
	}
	var annotations bundleAnnotations
	if err := unmarshalYAML(annotationBytes, &annotations); err != nil {
		return inspectedBundle{}, fmt.Errorf("parse bundle annotations: %w", err)
	}

	manager := ""
	for _, deployment := range csv.Spec.Install.Spec.Deployments {
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name == "manager" {
				if manager != "" {
					return inspectedBundle{}, fmt.Errorf("more than one manager container")
				}
				manager = container.Image
			}
		}
	}
	if manager == "" || csv.Metadata.Annotations["containerImage"] != manager {
		return inspectedBundle{}, fmt.Errorf("CSV manager image and containerImage annotation do not match")
	}

	return inspectedBundle{
		Pullspec:     resolved,
		Package:      annotations.Annotations["operators.operatorframework.io.bundle.package.v1"],
		Version:      csv.Spec.Version,
		ManagerImage: manager,
	}, nil
}

//nolint:wsl_v5 // Parsing checks intentionally follow their inputs.
func inspectImage(ctx context.Context, pullspec string) (imageInfo, error) {
	output, err := RunCommand(ctx, "oc", "image", "info", pullspec, "-o", "json")
	if err != nil {
		return imageInfo{}, fmt.Errorf("oc image info %s: %w\n%s", pullspec, err, output)
	}
	var info imageInfo
	if err := json.Unmarshal([]byte(output), &info); err != nil {
		return imageInfo{}, fmt.Errorf("parse image info: %w", err)
	}
	if !strings.HasPrefix(info.Digest, "sha256:") {
		return imageInfo{}, fmt.Errorf("image has invalid digest %q", info.Digest)
	}

	return info, nil
}

//nolint:wsl_v5 // Package and version checks intentionally remain adjacent.
func verifyBundle(bundle inspectedBundle, expectedPackage, expectedVersion string) error {
	if bundle.Package != expectedPackage {
		return fmt.Errorf("package %q, expected %q", bundle.Package, expectedPackage)
	}
	if bundle.Version != expectedVersion {
		return fmt.Errorf("version %q, expected %q", bundle.Version, expectedVersion)
	}

	return nil
}

//nolint:wsl_v5 // Digest reads and comparison form one linear check.
func requireSameImage(ctx context.Context, actual, expected string) error {
	actualInfo, err := inspectImage(ctx, actual)
	if err != nil {
		return err
	}
	expectedInfo, err := inspectImage(ctx, expected)
	if err != nil {
		return err
	}
	if actualInfo.Digest != expectedInfo.Digest {
		return fmt.Errorf("digest %s does not match expected digest %s", actualInfo.Digest, expectedInfo.Digest)
	}

	return nil
}

func unmarshalYAML(data []byte, value any) error {
	jsonData, err := yaml.ToJSON(data)
	if err != nil {
		return err
	}

	return json.Unmarshal(jsonData, value)
}
