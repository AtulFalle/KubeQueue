FROM golang:1.26.5-alpine AS build
ARG TARGET=api
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY apps/control-plane/go.mod apps/control-plane/go.sum* ./
RUN go mod download
COPY apps/control-plane ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kubequeue "./cmd/${TARGET}"

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kubequeue /kubequeue
USER nonroot:nonroot
ENTRYPOINT ["/kubequeue"]
