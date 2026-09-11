#!/usr/bin/env bash
# Explicit local build stages. No pushes, cluster calls, or implicit registry choice.
set -euo pipefail
[[ $# == 1 && ( $1 == operator || $1 == bundle ) ]] || {
    echo "usage: $0 operator|bundle (see tests/nhc-operator/README.md)" >&2; exit 2;
}
: "${NHC_SOURCE_REPOSITORY:?Set the local NHC repository path}"
: "${NHC_BUILD_DIR:?Set a new persistent standalone clone path}"
: "${NHC_BUILD_REPORT_DIR:?Set a persistent build evidence directory}"
: "${NHC_EXPECTED_SOURCE_COMMIT:?Set the full expected source commit}"
: "${VERSION:?Set the expected CSV version}"
: "${OPERATOR_BUILD_IMAGE:?Set your approved writable operator image tag}"
: "${BUNDLE_BUILD_IMAGE:?Set your approved writable bundle image tag}"
: "${CONSOLE_PLUGIN_IMAGE:?Set the verified console image digest pullspec}"
: "${MUST_GATHER_IMAGE:?Set the verified must-gather digest pullspec}"
[[ $NHC_BUILD_DIR == /* && $NHC_BUILD_REPORT_DIR == /* && $NHC_BUILD_DIR != /tmp/* && $NHC_BUILD_REPORT_DIR != /tmp/* ]]
[[ $NHC_EXPECTED_SOURCE_COMMIT =~ ^[a-f0-9]{40}$ ]]
[[ ${OPERATOR_BUILD_IMAGE##*/} == *:* && ${BUNDLE_BUILD_IMAGE##*/} == *:* ]]
[[ $CONSOLE_PLUGIN_IMAGE =~ @sha256:[a-f0-9]{64}$ && $MUST_GATHER_IMAGE =~ @sha256:[a-f0-9]{64}$ ]]
[[ $(uname -m) == x86_64 ]] || { echo "This Dockerfile downloads the amd64 Go toolchain" >&2; exit 1; }
if [[ $1 == operator ]]; then
    [[ ! -e $NHC_BUILD_DIR ]] || { echo "Build directory already exists; preserve it and choose a new directory" >&2; exit 1; }
    git clone --no-hardlinks "$NHC_SOURCE_REPOSITORY" "$NHC_BUILD_DIR"
    git -C "$NHC_BUILD_DIR" checkout --detach "$NHC_EXPECTED_SOURCE_COMMIT"
fi
[[ -d $NHC_BUILD_DIR/.git && $(git -C "$NHC_BUILD_DIR" rev-parse HEAD) == "$NHC_EXPECTED_SOURCE_COMMIT" ]]
[[ -z $(git -C "$NHC_BUILD_DIR" status --porcelain --untracked-files=no) ]] || {
    echo "Source has tracked edits; preserve them and use a new build directory" >&2; exit 1;
}
mkdir -p "$NHC_BUILD_REPORT_DIR"
git -C "$NHC_BUILD_DIR" show --no-patch --format=fuller HEAD > "$NHC_BUILD_REPORT_DIR/source.txt"
if [[ $1 == operator ]]; then
    # The existing Dockerfile runs hack/build.sh; VERSION below controls bundle
    # metadata, while the binary's version comes from git describe/rev-list.
    podman build --label "org.opencontainers.image.revision=$NHC_EXPECTED_SOURCE_COMMIT" \
        -f "$NHC_BUILD_DIR/Dockerfile" -t "$OPERATOR_BUILD_IMAGE" "$NHC_BUILD_DIR" 2>&1 | tee "$NHC_BUILD_REPORT_DIR/operator-build.log"
    podman image inspect "$OPERATOR_BUILD_IMAGE" > "$NHC_BUILD_REPORT_DIR/local-operator-image.json"
else
    : "${NHC_OPERATOR_IMAGE:?Set the published operator digest pullspec after read-back}"
    [[ $NHC_OPERATOR_IMAGE =~ @sha256:[a-f0-9]{64}$ ]]
    make -C "$NHC_BUILD_DIR" bundle-build-ocp \
        VERSION="$VERSION" IMG="$NHC_OPERATOR_IMAGE" BUNDLE_IMG="$BUNDLE_BUILD_IMAGE" \
        CONSOLE_PLUGIN_IMAGE="$CONSOLE_PLUGIN_IMAGE" MUST_GATHER_IMAGE="$MUST_GATHER_IMAGE" \
        PREVIOUS_VERSION=0.12.0 SKIP_RANGE_LOWER=0.1.0 2>&1 | tee "$NHC_BUILD_REPORT_DIR/bundle-build.log"
    cp -a "$NHC_BUILD_DIR/bundle" "$NHC_BUILD_REPORT_DIR/generated-bundle"
    git -C "$NHC_BUILD_DIR" diff > "$NHC_BUILD_REPORT_DIR/generated.diff"
fi
