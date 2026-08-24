# syntax=docker/dockerfile:1
ARG NODE_VERSION=[[ .params.nodeVersion ]]
FROM node:${NODE_VERSION}-alpine

# probot ships smee-client to npm only — there is no official image, and the
# third-party ones are stale and amd64-only. Building here keeps it current and
# multi-arch.
RUN npm install -g smee-client

WORKDIR /app
CMD ["smee"]
