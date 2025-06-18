# This is a renovate-friendly source of Docker images.
FROM python:3.13.2-slim-bullseye@sha256:81b94d27c19bba9f182fa3e46f13e21e01c48b8f5725972d82bab4cbe1bb96a2 AS python
FROM otel/weaver:v0.15.2@sha256:b13acea09f721774daba36344861f689ac4bb8d6ecd94c4600b4d590c8fb34b9 AS weaver
