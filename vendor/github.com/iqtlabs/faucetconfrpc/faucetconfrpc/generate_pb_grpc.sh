#!/bin/sh

protoc protos/faucetconfrpc/faucetconfrpc.proto --go_out=plugins=grpc:.
python -m grpc_tools.protoc -Iprotos protos/faucetconfrpc/faucetconfrpc.proto --python_out=.. --grpc_python_out=..
