FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /incidentpilot-agent ./cmd/agent

FROM scratch
COPY --from=build /incidentpilot-agent /incidentpilot-agent
USER 65532:65532
ENTRYPOINT ["/incidentpilot-agent"]
