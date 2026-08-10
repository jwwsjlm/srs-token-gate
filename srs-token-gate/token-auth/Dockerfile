FROM golang:1.26 AS build
WORKDIR /src
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download || echo "GOPROXY unavailable, using vendored dependencies"
COPY vendor ./vendor
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags='-s -w' -o /out/token-auth .
FROM scratch
COPY --from=build /out/token-auth /app/token-auth
COPY ui/index.html /app/ui/index.html
EXPOSE 8085
ENTRYPOINT ["/app/token-auth"]
CMD ["serve"]
