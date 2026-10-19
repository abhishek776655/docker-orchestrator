FROM alpine:3.20
RUN apk add --no-cache curl
COPY check.sh /check.sh
RUN chmod +x /check.sh
ENTRYPOINT ["sh", "-c", "/check.sh; sleep 3600"]
