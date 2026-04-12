FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o pt-backend ./

FROM alpine:3.21
RUN apk add --no-cache ca-certificates openssh-client

WORKDIR /app
COPY --from=builder /app/pt-backend .

RUN mkdir -p temp

EXPOSE 8080
CMD ["./pt-backend"]
