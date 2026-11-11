# Wera container image per PLAN.md section 13: multi-stage build producing
# a static binary. The runtime image is Alpine rather than distroless so it
# can ship poppler's pdftotext (reading uploaded resumes) and typst
# (rendering resumes). Secrets (AI_API_KEY) come from the environment
# at runtime.
FROM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X wera/internal/buildinfo.Version=${VERSION} -X wera/internal/buildinfo.Commit=${COMMIT}" \
      -o /out/wera ./cmd/wera

FROM alpine:3.24
RUN apk add --no-cache poppler-utils typst ca-certificates tzdata \
 && adduser -D -H -u 65532 nonroot
WORKDIR /app
COPY --from=build /out/wera /app/wera
COPY --from=build /src/config /app/config
ENV HTTP_ADDR=:8080
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/app/wera"]
CMD ["serve"]
