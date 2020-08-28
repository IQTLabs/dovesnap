#!/bin/bash
set -e

protoc protos/faucetconfrpc/faucetconfrpc.proto --go_out=plugins=grpc:.
python3 -m grpc_tools.protoc -Iprotos protos/faucetconfrpc/faucetconfrpc.proto --python_out=.. --grpc_python_out=..
