# syntax=docker/dockerfile:1
ARG SDK_VERSION=[[ .params.sdkVersion ]]
FROM mcr.microsoft.com/dotnet/sdk:${SDK_VERSION}

WORKDIR /app
EXPOSE [[ .params.port ]]
# dotnet watch ships in the SDK image — no extra tool to install.
CMD ["dotnet", "watch", "--non-interactive", "run", "--no-launch-profile"]
