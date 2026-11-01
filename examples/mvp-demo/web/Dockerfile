FROM golang:1.22-alpine AS build
WORKDIR /src
COPY server.go .
RUN go build -o /web server.go

FROM alpine:3.20
COPY --from=build /web /web
EXPOSE 8080
ENTRYPOINT ["/web"]
