#!/usr/bin/python3

"""Manage FAUCET config files via RPC (server)."""

from concurrent import futures  # pytype: disable=pyi-error

import argparse
import logging
import os
import shutil
import sys
import tempfile
import threading
import yaml

import grpc

from faucet import valve
from faucet.config_parser import dp_parser
from faucet.conf import InvalidConfigError

from faucetconfrpc import faucetconfrpc_pb2
from faucetconfrpc import faucetconfrpc_pb2_grpc


class _ServerError(Exception):

    pass


class Server(faucetconfrpc_pb2_grpc.FaucetConfServerServicer):  # pylint: disable=too-few-public-methods
    """Serve config management requests."""

    def __init__(self, config_dir, default_config):
        self.config_dir = config_dir
        self.default_config = default_config
        self.lock = threading.Lock()
        os.chdir(self.config_dir)

    def request_wrapper(self, request_handler, context, request, default_reply):
        """Wrap an RPC call in a lock and log."""
        with self.lock:
            reply = default_reply
            try:
                reply = request_handler()
                logging.info('request %s: reply %s', request, reply)
                return reply
            except (_ServerError, InvalidConfigError) as err:
                log = 'request %s: error %s' % (str(request), str(err))
                logging.error(log)
                context.set_code(grpc.StatusCode.UNKNOWN)
                context.set_details(log)
            return default_reply

    def _yaml_merge(self, yaml_doc_a, yaml_doc_b):
        if yaml_doc_a is None or isinstance(yaml_doc_a, (str, int, float)):
            yaml_doc_a = yaml_doc_b
        if yaml_doc_a != yaml_doc_b:
            if isinstance(yaml_doc_a, list) and isinstance(yaml_doc_b, list):
                yaml_doc_a = yaml_doc_b
            elif isinstance(yaml_doc_a, dict) and isinstance(yaml_doc_b, dict):
                for k in yaml_doc_b:
                    if k in yaml_doc_a:
                        yaml_doc_a[k] = self._yaml_merge(yaml_doc_a[k], yaml_doc_b[k])
                    else:
                        yaml_doc_a[k] = yaml_doc_b[k]
            else:
                raise _ServerError('cannot merge %s and %s' % (yaml_doc_a, yaml_doc_b))
        return yaml_doc_a

    def _filename_for_yaml(self, _config_yaml):
        # implement DFS for canonical config location based on YAML.
        return self.default_config

    def _validate_faucet_config(self, config_dir):
        logname = os.devnull
        try:
            root_config = os.path.join(config_dir, self.default_config)
            _, _, dps, top_confs = dp_parser(root_config, logname)
            dps_conf = None
            valve_cls = None
            acls_conf = None
            if dps is not None:
                dps_conf = {dp.name: dp for dp in dps}
                valve_cls = [valve.valve_factory(dp) for dp in dps]
                acls_conf = top_confs.get('acls', {})
            if not dps_conf or not valve_cls:
                raise InvalidConfigError('no DPs defined')
            return (dps_conf, acls_conf)
        except InvalidConfigError as err:
            raise _ServerError('Invalid config: %s' % err)  # pylint: disable=raise-missing-from

    def _validate_config_tree(self, config_filename, config_yaml):
        with tempfile.TemporaryDirectory() as tmpdir:
            config_dir = os.path.join(tmpdir, 'test_config')
            shutil.copytree(self.config_dir, config_dir)
            self._replace_config_file(config_filename, config_yaml, config_dir=config_dir)
            self._validate_faucet_config(config_dir)

    @staticmethod
    def _validate_filename(filename):
        safe_filename = os.path.basename(filename)
        safe_filename = "".join(i for i in safe_filename if i.isalnum() or i in '._')
        if safe_filename != filename:
            raise _ServerError('unexpected chars in filename')
        if not safe_filename.endswith('.yaml'):
            raise _ServerError('filename %s must end with .yaml' % safe_filename)
        if os.path.exists(safe_filename) and not os.path.isfile(safe_filename):
            raise _ServerError('cannot overwrite %s' % safe_filename)
        return safe_filename

    @staticmethod
    def _yaml_parse(config_yaml_str):
        try:
            return yaml.safe_load(config_yaml_str)
        except (yaml.constructor.ConstructorError, yaml.parser.ParserError) as err:
            raise _ServerError(f'YAML error: {err}')  # pylint: disable=raise-missing-from

    def _get_config_file(self, config_filename):
        try:
            with open(self._validate_filename(config_filename)) as config_file:
                return self._yaml_parse(config_file.read())
        except (FileNotFoundError, PermissionError) as err:
            raise _ServerError(f'Error: {err}')  # pylint: disable=raise-missing-from

    def _replace_config_file(self, config_filename, config_yaml, config_dir=None):
        if config_dir is None:
            config_dir = self.config_dir
        config_filename = self._validate_filename(config_filename)
        new_file = tempfile.NamedTemporaryFile(
            mode='wt', dir=config_dir, delete=False)
        new_file_name = new_file.name
        new_file.write(yaml.dump(config_yaml))
        new_file.close()
        os.rename(new_file_name, os.path.join(config_dir, config_filename))

    def _set_config_file(self, config_filename, config_yaml, merge, del_yaml_keys=None):
        try:
            config_filename = self._validate_filename(config_filename)
            new_config_yaml = self._yaml_parse(config_yaml)
            if merge:
                curr_config_yaml = self._get_config_file(config_filename)
                if del_yaml_keys:
                    curr_config_yaml = self._del_keys_from_yaml(
                        del_yaml_keys, curr_config_yaml)
                new_config_yaml = self._yaml_merge(curr_config_yaml, new_config_yaml)
            self._validate_config_tree(config_filename, new_config_yaml)
            self._replace_config_file(config_filename, new_config_yaml)
        except (FileNotFoundError, PermissionError, _ServerError) as err:
            raise _ServerError('Cannot set FAUCET config: %s' % err)   # pylint: disable=raise-missing-from

    def _del_keys_from_yaml(self, config_yaml_keys, new_config_yaml):
        config_yaml_keys = self._yaml_parse(config_yaml_keys)
        if not isinstance(config_yaml_keys, list):
            raise _ServerError('config_yaml_keys %s not a list' % config_yaml_keys)
        penultimate_key = new_config_yaml
        last_key = config_yaml_keys[-1]
        for key in config_yaml_keys[:-1]:
            penultimate_key = penultimate_key[key]
        if isinstance(penultimate_key, dict):
            del penultimate_key[last_key]
        else:
            penultimate_key.remove(last_key)
        return new_config_yaml

    def _del_config_from_file(self, config_filename, config_yaml_keys):
        try:
            new_config_yaml = self._del_keys_from_yaml(
                config_yaml_keys, self._get_config_file(config_filename))
            self._validate_config_tree(config_filename, new_config_yaml)
            self._replace_config_file(config_filename, new_config_yaml)
        except (KeyError, ValueError, _ServerError) as err:
            raise _ServerError(f'Unable to find key in the config: {err}')  # pylint: disable=raise-missing-from

    def GetConfigFile(self, request, context):  # pylint: disable=invalid-name
        """Return existing file contents as YAML string."""

        default_reply = faucetconfrpc_pb2.GetConfigFileReply()

        def get_config_file():
            config_filename = request.config_filename
            if not config_filename:
                config_filename = self.default_config
            return faucetconfrpc_pb2.GetConfigFileReply(
                config_yaml=yaml.dump(self._get_config_file(config_filename)))

        return self.request_wrapper(
            get_config_file, context, request, default_reply)

    def GetDpInfo(self, request, context):  # pylint: disable=invalid-name
        """Return parsed DP info."""

        default_reply = faucetconfrpc_pb2.GetDpInfoReply()

        def get_dp_info():
            config_filename = request.config_filename
            if not config_filename:
                config_filename = self.default_config
            config_yaml = self._get_config_file(config_filename)
            dps = config_yaml['dps']
            if request.dp_name:
                if request.dp_name in dps:
                    dps = {request.dp_name: dps[request.dp_name]}
                else:
                    dps = {}
            for dp_name, dp in dps.items():  # pylint: disable=invalid-name
                dp_info = default_reply.dps.add()  # pylint: disable=no-member
                dp_info.name = dp_name
                dp_info.dp_id = dp.get('dp_id', 0)
                dp_info.description = dp.get('description', '')
                for port_no, port in dp.get('interfaces', {}).items():
                    interface_info = dp_info.interfaces.add()
                    interface_info.port_no = port_no
                    interface_info.name = port.get('name', '')
                    interface_info.description = port.get('description', '')
            return default_reply

        return self.request_wrapper(
            get_dp_info, context, request, default_reply)

    def SetConfigFile(self, request, context):  # pylint: disable=invalid-name
        """Overwrite/update config file contents with provided YAML."""

        default_reply = faucetconfrpc_pb2.SetConfigFileReply()

        def set_config_file():
            config_filename = request.config_filename
            if not config_filename:
                config_filename = self._filename_for_yaml(request.config_yaml)
            self._set_config_file(
                config_filename, request.config_yaml, request.merge,
                request.del_config_yaml_keys)
            return default_reply

        return self.request_wrapper(
            set_config_file, context, request, default_reply)

    def _get_mirror(self, request):
        dps, _ = self._validate_faucet_config(self.config_dir)
        dp = dps[request.dp_name]  # pylint: disable=invalid-name
        port = dp.ports[request.port_no]
        mirror_port = dp.ports[request.mirror_port_no]
        mirrors = []
        if mirror_port.mirror:
            mirrors = list(mirror_port.mirror)
        return (port, mirrors)

    def _set_mirror(self, config_filename, request, mirrors):
        config_yaml = '{dps: {%s: {interfaces: {%u: {mirror: %s}}}}}' % (
            request.dp_name,
            request.mirror_port_no,
            mirrors)
        self._set_config_file(
            config_filename, config_yaml, merge=True)

    def AddPortMirror(self, request, context):  # pylint: disable=invalid-name
        """Add mirroring for port."""

        default_reply = faucetconfrpc_pb2.AddPortMirrorReply()

        def add_port_mirror():
            config_filename = self.default_config
            port, mirrors = self._get_mirror(request)
            if port.number not in mirrors:
                mirrors.append(port.number)
            self._set_mirror(config_filename, request, mirrors)
            return default_reply

        return self.request_wrapper(
            add_port_mirror, context, request, default_reply)

    def RemovePortMirror(self, request, context):  # pylint: disable=invalid-name
        """Remove mirroring for port."""

        default_reply = faucetconfrpc_pb2.AddPortMirrorReply()

        def remove_port_mirror():
            config_filename = self.default_config
            port, mirrors = self._get_mirror(request)
            if port.number in mirrors:
                mirrors.remove(port.number)
            self._set_mirror(config_filename, request, mirrors)
            return default_reply

        return self.request_wrapper(
            remove_port_mirror, context, request, default_reply)

    def ClearPortMirror(self, request, context):  # pylint: disable=invalid-name
        """Remove all mirroring on port."""

        default_reply = faucetconfrpc_pb2.ClearPortMirrorReply()

        def clear_port_mirror():
            config_filename = self.default_config
            self._set_mirror(config_filename, request, [])
            return default_reply

        return self.request_wrapper(
            clear_port_mirror, context, request, default_reply)

    def _get_port_acls(self, request):
        dps, _ = self._validate_faucet_config(self.config_dir)
        dp = dps[request.dp_name]  # pylint: disable=invalid-name
        port = dp.ports[request.port_no]
        acls_in = []
        if port.acls_in:
            acls_in = list([acl_in._id for acl_in in port.acls_in])  # pylint: disable=protected-access
        return acls_in

    def _set_port_acls(self, config_filename, request, acls_in):
        config_yaml = '{dps: {%s: {interfaces: {%u: {acls_in: [%s]}}}}}' % (
            request.dp_name,
            request.port_no,
            ','.join(acls_in))
        self._set_config_file(
            config_filename, config_yaml, merge=True)

    def SetPortAcl(self, request, context):  # pylint: disable=invalid-name
        """Set ACL list on port."""

        default_reply = faucetconfrpc_pb2.SetPortAclReply()

        def set_port_acl():
            config_filename = self.default_config
            self._set_port_acls(config_filename, request, request.acls.split(','))
            return default_reply

        return self.request_wrapper(
            set_port_acl, context, request, default_reply)

    def AddPortAcl(self, request, context):  # pylint: disable=invalid-name
        """Add ACL to port."""

        default_reply = faucetconfrpc_pb2.AddPortAclReply()

        def add_port_acl():
            config_filename = self.default_config
            acls_in = self._get_port_acls(request)
            if request.acl not in acls_in:
                acls_in.append(request.acl)
            self._set_port_acls(config_filename, request, acls_in)
            return default_reply

        return self.request_wrapper(
            add_port_acl, context, request, default_reply)

    def RemovePortAcl(self, request, context):  # pylint: disable=invalid-name
        """Remove ACL from port."""

        default_reply = faucetconfrpc_pb2.RemovePortAclReply()

        def remove_port_acl():
            config_filename = self.default_config
            acls_in = []
            # If no ACLs specified, remove all ACLs.
            if request.acl:
                acls_in = self._get_port_acls(request)
                if request.acl in acls_in:
                    acls_in.remove(request.acl)
            self._set_port_acls(config_filename, request, acls_in)
            return default_reply

        return self.request_wrapper(
            remove_port_acl, request, context, default_reply)

    def DelConfigFromFile(self, request, context):  # pylint: disable=invalid-name
        """Delete config file contents based on provided key."""

        default_reply = faucetconfrpc_pb2.DelConfigFromFileReply()

        def del_config_from_file():
            config_filename = request.config_filename
            if not config_filename:
                config_filename = self._filename_for_yaml(request.config_yaml)
            self._del_config_from_file(
                config_filename, request.config_yaml_keys)
            return default_reply

        return self.request_wrapper(
            del_config_from_file, request, context, default_reply)

    def SetDpInterfaces(self, request, context):  # pylint: disable=invalid-name
        """Replace interfaces config."""

        default_reply = faucetconfrpc_pb2.SetDpInterfacesReply()

        def set_dp_interfaces():
            config_filename = self.default_config
            config_yaml = self._get_config_file(config_filename)
            for dp_request in request.interfaces_config:
                interfaces = config_yaml['dps'][dp_request.dp_name]['interfaces']
                for interface_request in dp_request.interface_config:
                    interfaces[interface_request.port_no] = self._yaml_parse(
                        interface_request.config_yaml)
            self._set_config_file(
                config_filename, yaml.dump(config_yaml), False, [])
            return default_reply

        return self.request_wrapper(
            set_dp_interfaces, request, context, default_reply)

    @staticmethod
    def _del_dp(dp_name, config_yaml):
        if dp_name in config_yaml['dps']:
            for interface in config_yaml['dps'][dp_name]['interfaces'].values():
                port_stack = interface.get('stack', None)
                if port_stack:
                    del config_yaml['dps'][port_stack['dp']]['interfaces'][port_stack['port']]
            del config_yaml['dps'][dp_name]

    def DelDps(self, request, context):  # pylint: disable=invalid-name
        """Delete DPs altogether."""

        default_reply = faucetconfrpc_pb2.DelDpsReply()

        def del_dps():
            config_filename = self.default_config
            config_yaml = self._get_config_file(config_filename)
            for dp_request in request.interfaces_config:
                self._del_dp(dp_request.name, config_yaml)
            self._set_config_file(
                config_filename, yaml.dump(config_yaml), False, [])
            return default_reply

        return self.request_wrapper(
            del_dps, request, context, default_reply)

    def DelDpInterfaces(self, request, context):  # pylint: disable=invalid-name

        default_reply = faucetconfrpc_pb2.DelDpInterfacesReply()

        def del_dp_interfaces():
            config_filename = self.default_config
            config_yaml = self._get_config_file(config_filename)
            for dp_info in request.interfaces_config:
                dp_port_nos = set()
                dp_interfaces = config_yaml['dps'][dp_info.name]['interfaces']
                for interface_info in dp_info.interfaces:
                    try:
                        del dp_interfaces[interface_info.port_no]
                        dp_port_nos.add(interface_info.port_no)
                    except KeyError:
                        continue
                # second pass to clean up any mirroring
                for interface in dp_interfaces:
                    port_mirror = set(dp_interfaces[interface].get('mirror', []))
                    if port_mirror:
                        dp_interfaces[interface]['mirror'] = list(port_mirror - dp_port_nos)
            if request.delete_empty_dp:
                for dp_info in request.interfaces_config:
                    try:
                        if not dp_interfaces:
                            self._del_dp(dp_info.name, config_yaml)
                    except KeyError:
                        continue
            self._set_config_file(
                config_filename, yaml.dump(config_yaml), False, [])
            return default_reply

        return self.request_wrapper(
            del_dp_interfaces, request, context, default_reply)

    def SetRemoteMirrorPort(self, request, context):  # pylint: disable=invalid-name

        default_reply = faucetconfrpc_pb2.SetRemoteMirrorPortReply()

        def make_acl(rules):
            return [{'rule': rule} for rule in rules]

        def set_remote_mirror_port():
            config_filename = self.default_config
            config_yaml = self._get_config_file(config_filename)
            config_yaml.setdefault('acls', {})
            acl_name = 'remote-mirror-%u-%s-%u' % (
                request.tunnel_vid,
                request.remote_dp_name,
                request.remote_port_no)
            config_yaml['acls'][acl_name] = make_acl([
                {
                    'vlan_vid': request.tunnel_vid,
                    'actions': {
                        'allow': 0}},
                {
                    'actions': {
                        'allow': 0,
                        'output': {
                            'tunnel': {
                                'dp': request.remote_dp_name,
                                'port': request.remote_port_no,
                                'tunnel_id': request.tunnel_vid,
                                'type': 'vlan'}}}},
            ])
            dp = config_yaml['dps'][request.dp_name]  # pylint: disable=invalid-name
            dp['interfaces'][request.port_no] = {
                'acls_in': [acl_name],
                'coprocessor': {
                    'strategy': 'vlan_vid',
                },
                'description': 'loopback'
            }
            self._set_config_file(
                config_filename, yaml.dump(config_yaml), False, [])
            return default_reply

        return self.request_wrapper(
            set_remote_mirror_port, request, context, default_reply)

    def GetDpNames(self, request, context):  # pylint: disable=invalid-name

        default_reply = faucetconfrpc_pb2.GetDpNamesReply()

        def get_dp_names():
            config_filename = self.default_config
            config_yaml = self._get_config_file(config_filename)
            dp_names = config_yaml['dps'].keys()
            default_reply.dp_name[:] = dp_names  # pylint: disable=no-member
            return default_reply

        return self.request_wrapper(
            get_dp_names, request, context, default_reply)

    def GetDpIDs(self, request, context):  # pylint: disable=invalid-name

        default_reply = faucetconfrpc_pb2.GetDpIDsReply()

        def get_dp_ids():
            config_filename = self.default_config
            config_yaml = self._get_config_file(config_filename)
            dp_names = [dp['dp_id'] for dp in config_yaml['dps'].values()]
            default_reply.dp_id[:] = dp_names  # pylint: disable=no-member
            return default_reply

        return self.request_wrapper(
            get_dp_ids, request, context, default_reply)

    def GetAclNames(self, request, context):  # pylint: disable=invalid-name

        default_reply = faucetconfrpc_pb2.GetAclNamesReply()

        def get_acl_names():
            _, acls_conf = self._validate_faucet_config(self.config_dir)
            acl_names = acls_conf.keys()
            default_reply.acl_name[:] = acl_names  # pylint: disable=no-member
            return default_reply

        return self.request_wrapper(
            get_acl_names, request, context, default_reply)


def serve():
    """Start server and serve requests."""
    logging.basicConfig(stream=sys.stdout, level=logging.DEBUG)
    parser = argparse.ArgumentParser()
    parser.add_argument(
        '--config_dir',
        help='directory to serve config',
        action='store',
        default='/tmp/')
    parser.add_argument(
        '--default_config',
        help='name of default location of FAUCET config',
        action='store',
        default='faucet.yaml')
    parser.add_argument(
        '--key',
        help='server private key',
        action='store',
        default='localhost.key')
    parser.add_argument(
        '--cert',
        help='server public cert',
        action='store',
        default='localhost.crt')
    parser.add_argument(
        '--cacert',
        help='CA public cert',
        action='store',
        default='ca.crt')
    parser.add_argument(
        '--host',
        help='host address to serve rpc requests',
        default='localhost')
    parser.add_argument(
        '--port',
        help='port to serve rpc requests',
        action='store',
        default=59999,
        type=int)
    args = parser.parse_args()
    with open(args.key) as keyfile:
        private_key = keyfile.read().encode('utf8')
    with open(args.cert) as keyfile:
        certificate_chain = keyfile.read().encode('utf8')
    with open(args.cacert) as keyfile:
        root_certificate = keyfile.read().encode('utf8')
    server_credentials = grpc.ssl_server_credentials(
        ((private_key, certificate_chain),),
        root_certificate,
        require_client_auth=True)
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=1))
    faucetconfrpc_pb2_grpc.add_FaucetConfServerServicer_to_server(
        Server(args.config_dir, args.default_config), server)
    server.add_secure_port('%s:%u' % (args.host, args.port), server_credentials)
    server.start()
    server.wait_for_termination()


if __name__ == '__main__':
    serve()
