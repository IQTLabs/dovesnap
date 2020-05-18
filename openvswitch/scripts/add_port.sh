#!/bin/bash

# TODO handle when more than one?
BR_NAME=$(ovs-vsctl --db=tcp:127.0.0.1:6640 list-br)

ovs-vsctl --db=tcp:127.0.0.1:6640 add-port $BR_NAME $1
