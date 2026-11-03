FROM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG REVISION=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/herta-network/aws-panel/internal/app/buildinfo.Version=${VERSION} -X github.com/herta-network/aws-panel/internal/app/buildinfo.Revision=${REVISION}" -o /out/aws-panel .

FROM alpine:3.23@sha256:ff7f0c727dd796aa36787bfcd9a64bf35a6ce72a73451b3359fde849b6703d63
RUN apk add --no-cache ca-certificates
RUN mkdir -p /var/lib/aws-panel/keys && chown 65532:65532 /var/lib/aws-panel/keys && chmod 700 /var/lib/aws-panel/keys
COPY --from=build /out/aws-panel /usr/local/bin/aws-panel
ENV TZ=Asia/Shanghai
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/aws-panel"]
CMD ["run"]
