# Make sure to run the following commands after changes to this file are made:
# `make -f Makefile.docker update-build-image-tag && make -f Makefile.docker push-build-image`

FROM golang:1.16.6-alpine3.14@sha256:a8df40ad1380687038af912378f91cf26aeabb05046875df0bfedd38a79b5499 AS base

# Add kubectl and aws-iam-authenticator to the PATH
ENV PATH="${PATH}:/out/usr/bin:/out/usr/local/bin"

# Build-time dependencies
RUN apk add --no-cache \
    bash \
    curl \
    docker-cli \
    g++ \
    gcc \
    git \
    httpie \
    libsass-dev \
    make \
    musl-dev \
    jq \
    py3-setuptools \
    && true

# Runtime dependencies. Build the root filesystem of the eksctl image at /out
RUN mkdir -p /out/etc/apk && cp -r /etc/apk/* /out/etc/apk/
RUN apk add --no-cache --initdb --root /out \
    alpine-baselayout \
    busybox \
    ca-certificates \
    coreutils \
    git \
    libc6-compat \
    openssh \
    && true

RUN curl --silent --location "https://dl.k8s.io/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" --output /out/usr/local/bin/kubectl \
    && chmod +x /out/usr/local/bin/kubectl

ENV GITHUB_CLI_VERSION 1.5.0
RUN curl --silent --location "https://github.com/cli/cli/releases/download/v${GITHUB_CLI_VERSION}/gh_${GITHUB_CLI_VERSION}_linux_amd64.tar.gz" \
    | tar xvz -C /out/usr/local/bin --strip-components=2 gh_${GITHUB_CLI_VERSION}_linux_amd64/bin/gh && gh --version

# Flux release very regularly so we always get and test against the latest
RUN curl -sL https://api.github.com/repos/fluxcd/flux2/releases/latest 2>/dev/null | \
    jq -r '.assets[] | select(.browser_download_url | contains("linux_amd64")) | .browser_download_url' | \
    xargs curl -sL | tar xvz -C /out/usr/local/bin && flux --version

# Remaining dependencies are controlled by go.mod
WORKDIR /src
ENV CGO_ENABLED=0 GOPROXY=https://proxy.golang.org,direct

# The authenticator is a runtime dependency, so it needs to be in /out
ENV AWS_IAM_AUTH_VERSION 0.5.2
RUN curl --silent --location "https://github.com/kubernetes-sigs/aws-iam-authenticator/releases/download/v${AWS_IAM_AUTH_VERSION}/aws-iam-authenticator_${AWS_IAM_AUTH_VERSION}_linux_amd64" --output /out/usr/local/bin/aws-iam-authenticator \
    && chmod +x /out/usr/local/bin/aws-iam-authenticator
COPY .requirements install-build-deps.sh go.mod go.sum /src/

# Install all build tools dependencies
RUN ./install-build-deps.sh
