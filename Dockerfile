FROM quay.io/konveyor/builder:ubi9-v1.27

ARG GO_VER=go1.27.1
ARG CONTAINERUSER=testuser

LABEL description="system-tests development image"
LABEL go.version=${GO_VER}
LABEL container.user=${CONTAINERUSER}

RUN dnf install -y tar gcc make && \
    dnf clean all && \
    useradd -U -u 1000 -m -d /home/${CONTAINERUSER} -s /usr/bin/bash ${CONTAINERUSER}

RUN chown -R ${CONTAINERUSER}:${CONTAINERUSER} /opt/app-root/src/go
USER ${CONTAINERUSER}
WORKDIR /home/${CONTAINERUSER}
COPY --chown=${CONTAINERUSER}:${CONTAINERUSER} go.mod go.sum ./
RUN go install github.com/onsi/ginkgo/v2/ginkgo
COPY --chown=${CONTAINERUSER}:${CONTAINERUSER} . .

ENTRYPOINT ["scripts/test-runner.sh"]
