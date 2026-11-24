FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /photospots ./cmd/photospots

FROM gcr.io/distroless/static-debian12
COPY --from=build /photospots /photospots
COPY --from=build /usr/local/go/lib/time/zoneinfo.zip /zoneinfo.zip
ENV ZONEINFO=/zoneinfo.zip
EXPOSE 8080
ENTRYPOINT ["/photospots"]
CMD ["serve"]
