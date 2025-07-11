# This is a renovate-friendly source of Docker images.
FROM python:3.13.5-slim-bullseye@sha256:631af3fee9d0b0a046855a62af745c1f94b75c5309be8802a0928cce3ac0f98d AS python
FROM otel/weaver:v0.16.1@sha256:5ca4901b460217604ddb83feaca05238e2b016a226ecfb9b87a95555918a03af AS weaver
