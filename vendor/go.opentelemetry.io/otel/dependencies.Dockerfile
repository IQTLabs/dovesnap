# This is a renovate-friendly source of Docker images.
FROM python:3.13.2-slim-bullseye@sha256:81b94d27c19bba9f182fa3e46f13e21e01c48b8f5725972d82bab4cbe1bb96a2 AS python
FROM otel/weaver:v0.16.0@sha256:ee6eefd8cd8f4d2cfb7763b8a0fd613cfdf7dfbfda97e0e9b49d1a00dd01f7d6 AS weaver
