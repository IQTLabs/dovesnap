# This is a renovate-friendly source of Docker images.
FROM python:3.13.2-slim-bullseye@sha256:81b94d27c19bba9f182fa3e46f13e21e01c48b8f5725972d82bab4cbe1bb96a2 AS python
FROM otel/weaver:v0.15.3@sha256:a84032d6eb95b81972d19de61f6ddc394a26976c1c1697cf9318bef4b4106976 AS weaver
