FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY policies/ ./policies/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /incidentpilot-api ./cmd/api

FROM scratch
COPY --from=build /incidentpilot-api /incidentpilot-api
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/incidentpilot-api"]
