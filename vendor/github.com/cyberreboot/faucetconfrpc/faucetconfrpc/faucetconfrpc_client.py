#!/usr/bin/python3

"""Manage FAUCET config files via RPC (client)."""

import argparse
from faucetconfrpc.faucetconfrpc_client_lib import FaucetConfRpcClient


class ClientError(Exception):
    """Exceptions for client."""


def main():
    """Instantiate client and call it."""
    parser = argparse.ArgumentParser()
    parser.add_argument(
        '--key', help='client private key', action='store',
        default='client.key')
    parser.add_argument(
        '--cert', help='client public cert', action='store',
        default='client.crt')
    parser.add_argument(
        '--cacert', help='CA public cert', action='store',
        default='ca.crt')
    parser.add_argument(
        '--port', help='port to serve rpc requests', action='store',
        default=59999, type=int)
    parser.add_argument(
        '--host', help='host address to serve rpc requests',
        default='localhost')
    parser.add_argument(
        'commands', type=str, nargs='+',
        help='rpc commands')
    args = parser.parse_args()
    server_addr = '%s:%u' % (args.host, args.port)
    client = FaucetConfRpcClient(args.key, args.cert, args.cacert, server_addr)
    command = getattr(client, args.commands[0], None)
    if not command:
        raise ClientError('no such rpc: %s' % args.commands[0])
    command_args = {}
    for args in args.commands[1:]:
        arg, val = args.split('=')
        if val.lower() == 'true':
            val = True
        elif val.lower() == 'false':
            val = False
        command_args[arg] = val
    print(command(**command_args))


if __name__ == '__main__':
    main()
