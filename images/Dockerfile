# This Dockerfile is used to build the image available on DockerHub
FROM --platform=$BUILDPLATFORM golang:1.23 as build

# Add everything
ADD . /usr/src/multus-cni

ARG TARGETPLATFORM
RUN  cd /usr/src/multus-cni && \
     ./hack/build-go.sh

FROM gcr.io/distroless/base-debian11:latest
LABEL org.opencontainers.image.source https://github.com/k8snetworkplumbingwg/multus-cni
COPY --from=build /usr/src/multus-cni/bin /usr/src/multus-cni/bin
COPY --from=build /usr/src/multus-cni/LICENSE /usr/src/multus-cni/LICENSE
WORKDIR /

COPY --from=build /usr/src/multus-cni/bin/install_multus /
COPY --from=build /usr/src/multus-cni/bin/thin_entrypoint /
COPY --from=build /usr/src/multus-cni/bin/kubeconfig_generator /
COPY --from=build /usr/src/multus-cni/bin/cert-approver /
CMD ["/thin_entrypoint"]
