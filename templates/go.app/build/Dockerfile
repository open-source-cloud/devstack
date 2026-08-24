# syntax=docker/dockerfile:1
ARG GO_VERSION=[[ .params.goVersion ]]
FROM golang:${GO_VERSION}-alpine

# golang:*-alpine ships only ca-certificates. git is needed whenever a dependency
# resolves via VCS rather than the module proxy.
RUN apk add --no-cache git

# air-verse is the current module path; github.com/cosmtrek/air is the pre-move
# location and no longer resolves cleanly.
RUN go install github.com/air-verse/air@latest

WORKDIR /app
EXPOSE [[ .params.port ]]
CMD ["air"]
