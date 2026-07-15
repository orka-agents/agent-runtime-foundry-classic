# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /out && CGO_ENABLED=0 GOOS=linux go build -o /out/orka-foundry-classic-harness-adapter .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/orka-foundry-classic-harness-adapter /orka-foundry-classic-harness-adapter
USER 65532:65532
EXPOSE 8090
ENTRYPOINT ["/orka-foundry-classic-harness-adapter"]
