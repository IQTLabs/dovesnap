#!/bin/bash

sudo apt-get update && \
  sudo apt-get install -yq --no-install-recommends python3 python3-dev python3-setuptools && \
  python3 -m pip install .
