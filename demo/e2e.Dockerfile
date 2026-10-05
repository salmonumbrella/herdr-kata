# End to end, on a machine that has never seen herdr-kata.
#
# The demo image builds herdr-kata from the working tree, which proves the code
# works but says nothing about whether a stranger can install it. This one
# starts from a bare Ubuntu with only Go, git and herdr on it, and installs
# herdr-kata the way the README says to — from GitHub, through herdr.
#
#   docker build -f demo/e2e.Dockerfile -t herdr-kata-e2e .
#   docker run --rm -e REPO=salmonumbrella/herdr-kata [-e GH_TOKEN=…] herdr-kata-e2e
#
# GH_TOKEN is only needed while the repository is private; a public repo clones
# anonymously, which is the case this is ultimately here to prove.
FROM ubuntu:24.04

ARG GO_VERSION=1.26.5
ARG HERDR_VERSION=v0.7.5

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl git jq \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:/root/go/bin:${PATH}"

RUN curl -fsSL -o /usr/local/bin/herdr \
      "https://github.com/ogulcancelik/herdr/releases/download/${HERDR_VERSION}/herdr-linux-x86_64" \
    && chmod +x /usr/local/bin/herdr

# Only the test script is copied in. Everything else under test arrives the way
# a user would get it: over the network, from the published repository.
COPY demo/e2e.sh /e2e.sh
RUN chmod +x /e2e.sh

ENV HERDR_KATA_HOME=/root/.herdr-kata
ENTRYPOINT ["/e2e.sh"]
