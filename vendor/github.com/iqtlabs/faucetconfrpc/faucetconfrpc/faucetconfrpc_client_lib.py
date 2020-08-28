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
        self._channel = grpc.secure_channel(server_addr, client_creds)
        self._stub = faucetconfrpc_pb2_grpc.FaucetConfServerStub(self._channel)

    @staticmethod
    def _call(rpc, params):
        try:
            return rpc(params)
        except grpc.RpcError as err:
            logging.error(err)
            return None

    def get_config_file(self, config_filename=None):
        """Get a YAML config file."""
        response = self._call(self._stub.GetConfigFile, faucetconfrpc_pb2.GetConfigFileRequest(
            config_filename=config_filename))
        if response is not None:
            return yaml.safe_load(response.config_yaml)
        return None

    def set_config_file(self, config_yaml, config_filename=None, merge=True,
                        del_config_yaml_keys=''):
        """Set a YAML config file."""
        if isinstance(config_yaml, dict):
            config_yaml = yaml.dump(config_yaml)
        return self._call(self._stub.SetConfigFile, faucetconfrpc_pb2.SetConfigFileRequest(
            config_filename=config_filename,
            config_yaml=config_yaml,
            merge=merge,
            del_config_yaml_keys=del_config_yaml_keys))

    def del_config_from_file(self, config_yaml_keys, config_filename=None):
        """Delete a key from YAML config file."""
        if isinstance(config_yaml_keys, list):
            config_yaml_keys = yaml.dump(config_yaml_keys)
        return self._call(self._stub.DelConfigFromFile, faucetconfrpc_pb2.DelConfigFromFileRequest(
            config_filename=config_filename,
            config_yaml_keys=config_yaml_keys))

    def add_port_mirror(self, dp_name, port_no, mirror_port_no):
        """Add port mirroring."""
        return self._call(self._stub.AddPortMirror, faucetconfrpc_pb2.AddPortMirrorRequest(
            dp_name=dp_name, port_no=port_no, mirror_port_no=mirror_port_no))

    def remove_port_mirror(self, dp_name, port_no, mirror_port_no):
        """Remove port mirroring."""
        return self._call(self._stub.RemovePortMirror, faucetconfrpc_pb2.RemovePortMirrorRequest(
            dp_name=dp_name, port_no=port_no, mirror_port_no=mirror_port_no))

    def clear_port_mirror(self, dp_name, mirror_port_no):
        """Clear all mirroring on port."""
        return self._call(self._stub.ClearPortMirror, faucetconfrpc_pb2.ClearPortMirrorRequest(
            dp_name=dp_name, mirror_port_no=mirror_port_no))

    def add_port_acl(self, dp_name, port_no, acl):
        """Add port ACL."""
        return self._call(self._stub.AddPortAcl, faucetconfrpc_pb2.AddPortAclRequest(
            dp_name=dp_name, port_no=port_no, acl=acl))

    def set_port_acls(self, dp_name, port_no, acls):
        """Set port ACL."""
        return self._call(self._stub.SetPortAcl, faucetconfrpc_pb2.SetPortAclRequest(
            dp_name=dp_name, port_no=port_no, acls=acls))

    def remove_port_acl(self, dp_name, port_no, acl=None):
        """Remove port ACL."""
        if acl:
            return self._call(self._stub.RemovePortAcl, faucetconfrpc_pb2.RemovePortAclRequest(
                dp_name=dp_name, port_no=port_no, acl=acl))
        return self._call(self._stub.RemovePortAcl, faucetconfrpc_pb2.RemovePortAclRequest(
            dp_name=dp_name, port_no=port_no, acl=acl))

    def get_dp_info(self, dp_name=''):
        """Get DP info."""
        return self._call(self._stub.GetDpInfo, faucetconfrpc_pb2.GetDpInfoRequest(
            dp_name=dp_name))

    def set_dp_interfaces(self, dp_interfaces_requests=None):
        """Set DP interfaces."""
        if not dp_interfaces_requests:
            dp_interfaces_requests = []
        request = faucetconfrpc_pb2.SetDpInterfacesRequest()
        for dp_name, dp_interfaces in dp_interfaces_requests:
            dp_request = request.interfaces_config.add()  # pylint: disable=no-member
            dp_request.dp_name = dp_name
            for port_no, config_yaml in dp_interfaces.items():
                interfaces_request = dp_request.interface_config.add()
                interfaces_request.port_no = port_no
                interfaces_request.config_yaml = config_yaml
        return self._call(self._stub.SetDpInterfaces, request)

    def del_dp_interfaces(self, dp_interfaces_requests=None, delete_empty_dp=False):
        """Delete DP interfaces."""
        if not dp_interfaces_requests:
            dp_interfaces_requests = []
        request = faucetconfrpc_pb2.DelDpInterfacesRequest()
        request.delete_empty_dp = delete_empty_dp
        for dp_name, dp_interfaces in dp_interfaces_requests:
            dp_request = request.interfaces_config.add()  # pylint: disable=no-member
            dp_request.name = dp_name
            for port_no in dp_interfaces:
                interfaces_request = dp_request.interfaces.add()
                interfaces_request.port_no = port_no
        return self._call(self._stub.DelDpInterfaces, request)

    def del_dps(self, dps):
        """Delete DPs."""
        request = faucetconfrpc_pb2.DelDpsRequest()
        for dp_name in dps:
            dp_request = request.interfaces_config.add()  # pylint: disable=no-member
            dp_request.name = dp_name
        return self._call(self._stub.DelDps, request)

    def set_remote_mirror_port(self, dp_name='', port_no=0, tunnel_vid=0,  # pylint: disable=too-many-arguments
                               remote_dp_name='', remote_port_no=''):
        """Set a port to be a remote mirror via tunnel."""
        request = faucetconfrpc_pb2.SetRemoteMirrorPortRequest()
        request.dp_name = dp_name
        request.port_no = port_no
        request.tunnel_vid = tunnel_vid
        request.remote_dp_name = remote_dp_name
        request.remote_port_no = remote_port_no
        return self._call(self._stub.SetRemoteMirrorPort, request)

    def get_dp_names(self):
        """Get a list of DP names."""
        request = faucetconfrpc_pb2.GetDpNamesRequest()
        return self._call(self._stub.GetDpNames, request)

    def get_dp_ids(self):
        """Get a list of DP IDs."""
        request = faucetconfrpc_pb2.GetDpIDsRequest()
        return self._call(self._stub.GetDpIDs, request)

    def get_acl_names(self):
        """Get a list of ACL names."""
        request = faucetconfrpc_pb2.GetAclNamesRequest()
        return self._call(self._stub.GetAclNames, request)
