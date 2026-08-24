# syntax=docker/dockerfile:1
ARG NODE_VERSION=[[ .params.nodeVersion ]]
FROM node:${NODE_VERSION}-alpine
WORKDIR /app
EXPOSE 4321
CMD ["npm", "run", "dev"]
