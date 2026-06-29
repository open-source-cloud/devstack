# syntax=docker/dockerfile:1
ARG NODE_VERSION=[[ .params.nodeVersion ]]
FROM node:${NODE_VERSION}-alpine

WORKDIR /app
EXPOSE 5173
CMD ["npm", "run", "dev"]
