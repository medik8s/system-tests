medik8s System Tests
=======
# system-tests

## Overview
The [system-tests](https://github.com/medik8s/system-tests) repository contains E2E tests for [medik8s](https://www.medik8s.io/) operators: FAR, MDR, NHC, NMO, and SNR.
The project is based on golang+[ginkgo](https://onsi.github.io/ginkgo) framework. It was forked from [eco-gotests](https://github.com/rh-ecosystem-edge/eco-gotests).

### Project requirements
* golang v1.26.x
* ginkgo v2.x

## Prerequisites
The test framework is designed to test a pre-installed OCP cluster which meets the following requirements:

### Mandatory setup requirements:
* OCP cluster installed with version >=4.13
* Operator namespace with Pod Security Admission (PSA) labels (manual installs only):

  All medik8s operators deploy into `openshift-workload-availability` and require privileged pod security
  because remediation operators run pods with host access and elevated capabilities. If you install
  operators manually (not via Prow CI), create the namespace and apply PSA labels before creating
  subscriptions:

  ```bash
  kubectl create namespace openshift-workload-availability
  kubectl label --overwrite ns openshift-workload-availability \
    security.openshift.io/scc.podSecurityLabelSync=false \
    pod-security.kubernetes.io/enforce=privileged \
    pod-security.kubernetes.io/audit=privileged \
    pod-security.kubernetes.io/warn=privileged
  ```

  > **Note:** In Prow CI, the `medik8s-operator-subscribe` step applies these labels automatically.
  > The `enforce=privileged` label is required (validated by FAR tests); the remaining labels
  > prevent SCC/PSA conflicts and match Prow CI behavior.

### Supported setups:
* Regular cluster 3 master nodes (VMs or BMs) 2 workers (VMs or BMs)
* Single Node Cluster (VM or BM)

### General environment variables
#### Mandatory:
* `KUBECONFIG` - Path to kubeconfig file. Default: empty
* `WORKLOAD_IMAGE` - Container image for test workload pods (used by destructive tests to verify pod eviction after remediation). In Prow CI this is set automatically by the `medik8s-lib` step. For local runs:
  ```bash
  export WORKLOAD_IMAGE=registry.access.redhat.com/ubi9/ubi-minimal:latest
  ```
#### Optional:
* Logging with glog

We use glog library for logging in the project. In order to enable verbose logging the following needs to be done:

1. Make sure to import inittool in your go script, per this example:

<sup>
    import (
      . "github.com/medik8s/system-tests/tests/internal/inittools"
    )
</sup>

2. Need to export the following SHELL variable:
> export ECO_VERBOSE_LEVEL=100

##### Notes:

  1. The value for the variable has to be >= 100.
  2. The variable can simply be exported in the shell where you run your automation.
  3. The go file you work on has to be in a directory under the `tests/` directory for being able to import inittools.
  4. Importing inittool also initializes the apiclient and it's available via "APIClient" variable.

* Collect logs from cluster with reporter

We use k8reporter library for collecting resource from cluster in case of test failure.
In order to enable k8reporter the following needs to be done:

1. Export DUMP_FAILED_TESTS and set it to true. Use example below
> export ECO_DUMP_FAILED_TESTS=true

2. Specify absolute path for logs directory like it appears below. By default /tmp/reports directory is used.
> export ECO_REPORTS_DUMP_DIR=/tmp/logs_directory

* Generation XML reports

We use reportxml library for generating compatible xml reports. 
The reporter is enabled by default and stores reports under REPORTS_DUMP_DIR directory.
In oder to disable reporterxml the following needs to be done:
> export ECO_ENABLE_REPORT=false


<!-- TODO Update this section with optional env vars for each test suite -->

## How to run

The test-runner [script](scripts/test-runner.sh) is the recommended way for executing tests.

Parameters for the script are controlled by the following environment variables:
- `ECO_TEST_FEATURES`: list of features to be tested ("all" will include all tests). All subdirectories under tests that match a feature will be included (internal directories are excluded) - _required_
- `ECO_TEST_LABELS`: ginkgo query passed to the label-filter option for including/excluding tests - _optional_ 
- `ECO_TEST_FOCUS`: text passed to ginkgo's `--focus` option for running a single test (or any tests whose name matches) - _optional_
- `ECO_VERBOSE_SCRIPT`: prints verbose script information when executing the script - _optional_
- `ECO_TEST_VERBOSE`: executes ginkgo with verbose test output - _optional_
- `ECO_TEST_TRACE`: includes full stack trace from ginkgo tests when a failure occurs - _optional_
- `ECO_TEST_TIMEOUT`: value passed to ginkgo's `-timeout` option, controlling the maximum time the full test run may take. Default: `24h` - _optional_

It is recommended to execute the runner script through the `make run-tests` make target.

Example:
```
$ export KUBECONFIG=/path/to/kubeconfig
$ export WORKLOAD_IMAGE=registry.access.redhat.com/ubi9/ubi-minimal:latest
$ export ECO_TEST_FEATURES="far-operator"
$ export ECO_TEST_LABELS='far'
$ make run-tests                    
Executing test-runner script
scripts/test-runner.sh
ginkgo -timeout=24h --keep-going --require-suite -r --label-filter="far" ./tests/far-operator
```

To run a single test, set `ECO_TEST_FOCUS` to (a substring of) the test's name, e.g. the `"should remediate a worker node after kubelet stop"` test from the SNR (Self Node Remediation) suite:

```
$ export KUBECONFIG=/path/to/kubeconfig
$ export WORKLOAD_IMAGE=registry.access.redhat.com/ubi9/ubi-minimal:latest
$ export ECO_TEST_FEATURES="snr-operator"
$ export ECO_TEST_FOCUS="should remediate a worker node after kubelet stop"
$ make run-tests
Executing test-runner script
scripts/test-runner.sh
ginkgo -timeout=24h --keep-going --require-suite -r --focus="should remediate a worker node after kubelet stop" ./tests/snr-operator
```

When focusing on a single test, it's also useful to shorten `ECO_TEST_TIMEOUT` from the 24h default:

```
$ export ECO_TEST_TIMEOUT=30m
$ make run-tests
Executing test-runner script
scripts/test-runner.sh
ginkgo -timeout=30m --keep-going --require-suite -r --focus="should remediate a worker node after kubelet stop" ./tests/snr-operator
```
# How to contribute

The project uses a development method - forking workflow
### The following is a step-by-step example of forking workflow:
1) A developer [forks](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/working-with-forks/fork-a-repo)
   the [system-tests](https://github.com/medik8s/system-tests) project
2) A new local feature branch is created
3) A developer makes changes on the new branch.
4) New commits are created for the changes.
5) The branch gets pushed to developer's own server-side copy.
6) Changes are tested.
7) A developer opens a pull request(`PR`) from the new branch to
   the [system-tests](https://github.com/medik8s/system-tests).
8) The pull request gets approved from at least 2 reviewers for merge and is merged into
   the [system-tests](https://github.com/medik8s/system-tests).

# Project structure
    .
    ├── images                             # container images artifacts
    ├── scripts                            # makefile scripts
    ├── tests                              # test cases directory
    │   ├── internal                       # common packages used across framework
    │   │   ├── config                     # common config struct
    │   │   ├── medik8sconfig              # medik8s-specific configuration
    │   │   ├── medik8sinittools           # medik8s test initialization
    │   │   └── medik8sparams              # medik8s shared constants
    │   ├── far-operator                   # Fence Agents Remediation tests
    │   ├── mdr-operator                   # Machine Deletion Remediation tests
    │   ├── nhc-operator                   # Node Health Check tests
    │   ├── nmo-operator                   # Node Maintenance Operator tests
    │   └── snr-operator                   # Self Node Remediation tests
### Code conventions
#### Lint
Push requested are tested in a pipeline with golangci-lint. It is advised to add [Golangci-lint integration](https://golangci-lint.run/usage/integrations/) to your development editor. It's recommended to run `make lint` before uploading a PR.

#### Commit Message Guidelines
There are two main components of a Git commit message: the title or summary, and the description. The commit message title is limited to 72 characters, and the description has no character limit.

Commit title format has two parts: 
1. Team name: Example - "cnf network" or "hw-accel" 
2. Short summary of code changes: Example - "added deployment test".

If a PR changes multiple team's directories or common infrastructure code, then instead of the team name, simply add "infra". Follow similar naming rules when adding changes to README (readme:) or github ci (ci:) files.

Commit message examples:
```text
infra: func defineNetwork moved to global net package"
readme: added commit message convention"
ci: added new deployment job
cnf network: added set func to cluster pkg
```

Please notice: Don't use internal test IDs and capital letters in commit message.

Commit description explains a changes in details if it's needed.

#### Functions format
If the function's arguments fit in a single line - use the following format:
```go
func Function(argInt1, argInt2 int, argString1, argString2 string) {
    ...
}
```

If the function's arguments don't fit in a single line - use the following format:
```go
func Function(
    argInt1 int,
    argInt2 int,
    argInt3 int,
    argInt4 int,
    argString1 string,
    argString2 string,
    argString3 string,
    argString4 string) {
    ...
}
```
One more acceptable format example:
```go
func Function(
    argInt1, argInt2 int, argString1, argString2 string, argSlice1, argSlice2 []string) {
	
}
```

### Common issues:
* If the automated commit check fails - make sure to pull/rebase the latest change and have a successful execution of 'make lint' locally first.


### Update eco-goinfra modules - How to:
1. In the left pane locate the "Eco-GoInfra Module Bump" action here: https://github.com/medik8s/system-tests/actions
2. Click on "Run workflow" in the right pane and run the workflow against the main branch (should take a few minutes to complete)
3. Click on the last executed workflow, then click on the "Label created PR" job and expand the "Label PR based on CI result" step
4. Copy the link to the created pull request and paste it in your browser.
5. Merge the pull request after passing the automated checks on it

#### Note: To start using the new package for the first time:
1. Add it to the import section of your test
2. Run `go mod tidy`
