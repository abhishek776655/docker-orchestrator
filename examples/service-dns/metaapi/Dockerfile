FROM golang:1.22-alpine AS build
WORKDIR /src
COPY server.go .
RUN go build -o /metaapi server.go

FROM alpine:3.20
COPY --from=build /metaapi /metaapi
EXPOSE 5000
ENTRYPOINT ["/metaapi"]
