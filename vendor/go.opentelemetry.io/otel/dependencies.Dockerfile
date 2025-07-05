# This is a renovate-friendly source of Docker images.
FROM python:3.13.2-slim-bullseye@sha256:81b94d27c19bba9f182fa3e46f13e21e01c48b8f5725972d82bab4cbe1bb96a2 AS python
FROM otel/weaver:v0.16.1@sha256:5ca4901b460217604ddb83feaca05238e2b016a226ecfb9b87a95555918a03af AS weaver
