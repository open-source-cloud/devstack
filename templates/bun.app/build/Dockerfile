# syntax=docker/dockerfile:1
ARG BUN_VERSION=[[ .params.bunVersion ]]
FROM oven/bun:${BUN_VERSION}-alpine
WORKDIR /app
EXPOSE 3000
CMD ["bun", "run", "dev"]
