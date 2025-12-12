FROM --platform=$BUILDPLATFORM golang:1.25 AS builder
LABEL authors="yuyi2"

ARG TARGETOS
ARG TARGETARCH
ARG version

WORKDIR /src/sub2sing-box
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w -X github.com/yuyi124/sub2sing-box/constant.Version=${version}" -o /out/sub2sing-box .

WORKDIR /src/sub2sing-box-v1.11
COPY sub2sing-box-v1.11/go.mod sub2sing-box-v1.11/go.sum ./
RUN go mod download
COPY sub2sing-box-v1.11/ ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /out/sub2sing-box-v1.11 .

FROM alpine:latest
WORKDIR /app

RUN mkdir -p /app/sub2sing-box-v1.11
COPY --from=builder /out/sub2sing-box /app/sub2sing-box
COPY --from=builder /out/sub2sing-box-v1.11 /app/sub2sing-box-v1.11/sub2sing-box-v1.11
COPY templates /app/templates
EXPOSE 8080
CMD ["/app/sub2sing-box", "server"]