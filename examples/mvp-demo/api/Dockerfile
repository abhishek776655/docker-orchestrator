FROM golang:1.22-alpine AS build
WORKDIR /src
COPY server.go .
RUN go build -o /api server.go

FROM alpine:3.20
COPY --from=build /api /api
EXPOSE 9090
ENTRYPOINT ["/api"]
