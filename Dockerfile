FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o pt-backend ./

FROM alpine:3.21
RUN apk add --no-cache ca-certificates openssh-client && \
    mkdir -p /root/.ssh && \
    chmod 700 /root/.ssh && \
    touch /root/.ssh/known_hosts

WORKDIR /app
COPY --from=builder /app/pt-backend .

RUN mkdir -p temp

EXPOSE 8080
CMD ssh-keyscan -H "${host}" >> /root/.ssh/known_hosts 2>/dev/null; ./pt-backend
