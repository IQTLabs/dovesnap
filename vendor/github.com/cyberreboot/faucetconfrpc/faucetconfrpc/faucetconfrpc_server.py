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
            _, _, dps, _ = dp_parser(root_config, logname)
            dps_conf = None
            valve_cls = None
            if dps is not None:
                dps_conf = {dp.name: dp for dp in dps}
                valve_cls = [valve.valve_factory(dp) for dp in dps]
            if not dps_conf or not valve_cls:
                raise InvalidConfigError('no DPs defined')
            return dps_conf
        except InvalidConfigError as err:
            raise _ServerError(err)

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

    def _get_config_file(self, config_filename):
        try:
            with open(self._validate_filename(config_filename)) as config_file:
                return yaml.safe_load(config_file.read())
        except (FileNotFoundError, PermissionError) as err:
            raise _ServerError(err)

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

    def _set_config_file(self, config_filename, config_yaml, merge):
        try:
            config_filename = self._validate_filename(config_filename)
            new_config_yaml = yaml.safe_load(config_yaml)
            if merge:
                curr_config_yaml = self._get_config_file(config_filename)
                new_config_yaml = self._yaml_merge(curr_config_yaml, new_config_yaml)
            self._validate_config_tree(config_filename, new_config_yaml)
            self._replace_config_file(config_filename, new_config_yaml)
        except (FileNotFoundError, PermissionError, _ServerError) as err:
            raise _ServerError(err)

    def _del_config_from_file(self, config_filename, config_yaml_keys):
        try:
            config_yaml_keys = yaml.safe_load(config_yaml_keys)
            if not isinstance(config_yaml_keys, list):
                raise _ServerError('config_yaml_keys %s not a list' % config_yaml_keys)
            new_config_yaml = self._get_config_file(config_filename)
            penultimate_key = new_config_yaml
            last_key = config_yaml_keys[-1]
            for key in config_yaml_keys[:-1]:
                penultimate_key = penultimate_key[key]
            if isinstance(penultimate_key, dict):
                del penultimate_key[last_key]
            else:
                penultimate_key.remove(last_key)
            self._validate_config_tree(config_filename, new_config_yaml)
            self._replace_config_file(config_filename, new_config_yaml)
        except (KeyError, ValueError, _ServerError) as err:
            raise _ServerError(err)

    @staticmethod
    def _log_error(context, request, err):
        log = '%s: error %s' % (str(request), str(err))
        logging.error(log)
        context.set_code(grpc.StatusCode.UNKNOWN)
        context.set_details(log)

    def GetConfigFile(self, request, context):  # pylint: disable=invalid-name
        """Return existing file contents as YAML string."""
        with self.lock:
            try:
                config_filename = request.config_filename
                if not config_filename:
                    config_filename = self.default_config
                return faucetconfrpc_pb2.GetConfigFileReply(
                    config_yaml=yaml.dump(self._get_config_file(config_filename)))
            except _ServerError as err:
                self._log_error(context, request, err)
        return faucetconfrpc_pb2.GetConfigFileReply()

    def SetConfigFile(self, request, context):  # pylint: disable=invalid-name
        """Overwrite/update config file contents with provided YAML."""
        with self.lock:
            try:
                config_filename = request.config_filename
                if not config_filename:
                    config_filename = self._filename_for_yaml(request.config_yaml)
                self._set_config_file(
                    config_filename, request.config_yaml, request.merge)
            except _ServerError as err:
                self._log_error(context, request, err)
        return faucetconfrpc_pb2.SetConfigFileReply()

    def _get_mirror(self, request):
        dps = self._validate_faucet_config(self.config_dir)
        dp = dps[request.dp_name]  # pylint: disable=invalid-name
        port = dp.ports[request.port_no]
        mirror_port = dp.ports[request.mirror_port_no]
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
        with self.lock:
            try:
                config_filename = self.default_config
                port, mirrors = self._get_mirror(request)
                if port.number not in mirrors:
                    mirrors.append(port.number)
                self._set_mirror(config_filename, request, mirrors)
            except _ServerError as err:
                self._log_error(context, request, err)
        return faucetconfrpc_pb2.AddPortMirrorReply()

    def RemovePortMirror(self, request, context):  # pylint: disable=invalid-name
        with self.lock:
            try:
                config_filename = self.default_config
                port, mirrors = self._get_mirror(request)
                if port.number in mirrors:
                    mirrors.remove(port.number)
                self._set_mirror(config_filename, request, mirrors)
            except _ServerError as err:
                self._log_error(context, request, err)
        return faucetconfrpc_pb2.AddPortMirrorReply()

    def _get_port_acls(self, request):
        dps = self._validate_faucet_config(self.config_dir)
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

    def AddPortAcl(self, request, context):  # pylint: disable=invalid-name
        with self.lock:
            try:
                config_filename = self.default_config
                acls_in = self._get_port_acls(request)
                if request.acl not in acls_in:
                    acls_in.append(request.acl)
                self._set_port_acls(config_filename, request, acls_in)
            except _ServerError as err:
                self._log_error(context, request, err)
        return faucetconfrpc_pb2.AddPortAclReply()

    def RemovePortAcl(self, request, context):  # pylint: disable=invalid-name
        with self.lock:
            try:
                config_filename = self.default_config
                acls_in = self._get_port_acls(request)
                if request.acl in acls_in:
                    acls_in.remove(request.acl)
                self._set_port_acls(config_filename, request, acls_in)
            except _ServerError as err:
                self._log_error(context, request, err)
        return faucetconfrpc_pb2.AddPortAclReply()

    def DelConfigFromFile(self, request, context):  # pylint: disable=invalid-name
        """Delete config file contents based on provided key ."""
        with self.lock:
            try:
                config_filename = request.config_filename
                if not config_filename:
                    config_filename = self._filename_for_yaml(request.config_yaml)
                self._del_config_from_file(
                    config_filename, request.config_yaml_keys)
            except _ServerError as err:
                self._log_error(context, request, err)
        return faucetconfrpc_pb2.DelConfigFromFileReply()


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
