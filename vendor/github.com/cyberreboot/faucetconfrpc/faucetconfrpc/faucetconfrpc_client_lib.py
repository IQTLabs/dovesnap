"""faucetconfrpc client library."""

import logging
import grpc
import yaml

from faucetconfrpc import faucetconfrpc_pb2
from faucetconfrpc import faucetconfrpc_pb2_grpc


class FaucetConfRpcClient:
    """faucetconfrpc client library."""

    def __init__(self, client_key, client_cert, ca_cert, server_addr):
        client_cred_args = {
            arg: open(keyfile).read().encode('utf8') for arg, keyfile in (
                ('root_certificates', ca_cert),
                ('certificate_chain', client_cert),
                ('private_key', client_key))}
        client_creds = grpc.ssl_channel_credentials(**client_cred_args)
        self.channel = grpc.secure_channel(server_addr, client_creds)
        self.stub = faucetconfrpc_pb2_grpc.FaucetConfServerStub(self.channel)

    @staticmethod
    def _call(rpc, params):
        try:
            return rpc(params)
        except grpc.RpcError as err:
            logging.error(err)
            return None

    def get_config_file(self, config_filename=None):
        """Get a YAML config file."""
        response = self._call(self.stub.GetConfigFile, faucetconfrpc_pb2.GetConfigFileRequest(
            config_filename=config_filename))
        if response is not None:
            return yaml.safe_load(response.config_yaml)
        return None

    def set_config_file(self, config_yaml, config_filename=None, merge=True):
        """Set a YAML config file."""
        if isinstance(config_yaml, dict):
            config_yaml = yaml.dump(config_yaml)
        return self._call(self.stub.SetConfigFile, faucetconfrpc_pb2.SetConfigFileRequest(
            config_filename=config_filename,
            config_yaml=config_yaml,
            merge=merge))

    def del_config_from_file(self, config_yaml_keys, config_filename=None):
        """Delete a key from YAML config file."""
        if isinstance(config_yaml_keys, list):
            config_yaml_keys = yaml.dump(config_yaml_keys)
        return self._call(self.stub.DelConfigFromFile, faucetconfrpc_pb2.DelConfigFromFileRequest(
            config_filename=config_filename,
            config_yaml_keys=config_yaml_keys))

    def add_port_mirror(self, dp_name, port_no, mirror_port_no):
        """Add port mirroring."""
        return self._call(self.stub.AddPortMirror, faucetconfrpc_pb2.AddPortMirrorRequest(
            dp_name=dp_name, port_no=port_no, mirror_port_no=mirror_port_no))

    def remove_port_mirror(self, dp_name, port_no, mirror_port_no):
        """Remove port mirroring."""
        return self._call(self.stub.RemovePortMirror, faucetconfrpc_pb2.RemovePortMirrorRequest(
            dp_name=dp_name, port_no=port_no, mirror_port_no=mirror_port_no))

    def add_port_acl(self, dp_name, port_no, acl):
        """Add port ACL."""
        return self._call(self.stub.AddPortAcl, faucetconfrpc_pb2.AddPortAclRequest(
            dp_name=dp_name, port_no=port_no, acl=acl))

    def remove_port_acl(self, dp_name, port_no, acl):
        """Remove port ACL."""
        return self._call(self.stub.RemovePortAcl, faucetconfrpc_pb2.RemovePortAclRequest(
            dp_name=dp_name, port_no=port_no, acl=acl))
