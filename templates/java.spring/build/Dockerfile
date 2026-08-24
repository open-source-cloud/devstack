# syntax=docker/dockerfile:1
ARG BASE_IMAGE=[[ .params.image ]]
FROM ${BASE_IMAGE}

WORKDIR /app
EXPOSE [[ .params.port ]]
CMD ["./mvnw", "spring-boot:run"]
