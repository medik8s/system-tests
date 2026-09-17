#!/usr/bin/env bash

GINKGO="${GINKGO:-ginkgo}"
GOPATH="${GOPATH:-${HOME}/go}"
PATH=$PATH:$GOPATH/bin
TEST_DIR="./tests"
ECO_TEST_TIMEOUT="${ECO_TEST_TIMEOUT:-24h}"

# In CI (ARTIFACT_DIR set by ci-operator), default to verbose test output
# so passing tests also show their step-by-step timeline in the logs.
ECO_TEST_VERBOSE="${ECO_TEST_VERBOSE:-${ARTIFACT_DIR:+true}}"

# In CI, ARTIFACT_DIR is set by ci-operator and is collected/uploaded automatically.
# Fall back to ECO_REPORTS_DUMP_DIR if already set, then /tmp/reports for local runs.
export ECO_REPORTS_DUMP_DIR="${ARTIFACT_DIR:-${ECO_REPORTS_DUMP_DIR:-/tmp/reports}}"

# Check that ECO_TEST_FEATURES environment variable has been set
if [[ -z "${ECO_TEST_FEATURES}" ]]; then
    echo "ECO_TEST_FEATURES environment variable is undefined"
    exit 1
fi

# Set feature_dirs to top-level test directory when "all" feature provided
if [[ "${ECO_TEST_FEATURES}" == "all" ]]; then
    feature_dirs=${TEST_DIR}
else
    # Find all test directories matching provided features
    for feature in ${ECO_TEST_FEATURES}; do
        discovered_features=$(find $TEST_DIR -depth -name "${feature}" -not -path '*/internal/*' 2> /dev/null)
        if [[ ! -z $discovered_features ]]; then
            feature_dirs+=" "$discovered_features
        else
            if [[ "${ECO_VERBOSE_SCRIPT}" == "true" ]]; then
                echo "Could not find any feature directories matching ${feature}"
            fi
        fi
    done

    if [[ -z "${feature_dirs}" ]]; then
        echo "Could not find any feature directories for provided features: ${ECO_TEST_FEATURES}"
        exit 1
    fi

    if [[ "${ECO_VERBOSE_SCRIPT}" == "true" ]]; then
        echo "Found feature directories:"
        for directory in $feature_dirs; do printf "$directory\n"; done
    fi
fi


# Build ginkgo command
cmd="${GINKGO} -timeout=${ECO_TEST_TIMEOUT} --keep-going --require-suite --randomize-all -r"

if [[ "${ECO_TEST_VERBOSE}" == "true" ]]; then
    cmd+=" -vv"
fi

if [[ "${ECO_TEST_TRACE}" == "true" ]]; then
    cmd+=" --trace"
fi

if [[ ! -z "${ECO_TEST_LABELS}" ]]; then
    cmd+=" --label-filter=\"${ECO_TEST_LABELS}\""
fi

# ECO_TEST_FOCUS: run a single test by name, e.g. "should remediate a worker node after kubelet stop"
if [[ ! -z "${ECO_TEST_FOCUS}" ]]; then
    cmd+=" --focus=\"${ECO_TEST_FOCUS}\""
fi
cmd+=" $@ $feature_dirs"   # add user args before feature dirs

# Execute ginkgo command
echo $cmd
eval $cmd
GINKGO_EXIT=$?

# Copy reportxml testrun XML to SHARED_DIR for the Polarion reporter post step.
COPY_EXIT=0
if [[ -n "${SHARED_DIR}" ]]; then
    mapfile -t testrun_files < <(find "${ECO_REPORTS_DUMP_DIR}" -type f -name '*_testrun.xml' 2>/dev/null)
    if [[ ${#testrun_files[@]} -eq 0 ]]; then
        echo "Warning: no *_testrun.xml files found in ${ECO_REPORTS_DUMP_DIR}" >&2
        COPY_EXIT=1
    elif ! cp -t "${SHARED_DIR}/" "${testrun_files[@]}"; then
        echo "Failed to copy *_testrun.xml from ${ECO_REPORTS_DUMP_DIR} to ${SHARED_DIR}" >&2
        COPY_EXIT=1
    fi
fi

if [[ $GINKGO_EXIT -eq 0 && $COPY_EXIT -ne 0 ]]; then
    exit $COPY_EXIT
fi
exit $GINKGO_EXIT
