# Self-hosted image: static binary on distroless, running as a non-root user.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/controle-gastos ./cmd/app

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/controle-gastos /controle-gastos
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/controle-gastos"]
CMD ["serve"]
