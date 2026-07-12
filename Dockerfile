FROM golang:1.24-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bridge .

FROM scratch
COPY --from=build /bridge /bridge
ENV BRIDGE_ADDR=0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["/bridge"]
