#!/usr/bin/python3

import re
import os
import subprocess
import docker
from graphviz import Digraph


class GraphDovesnapException(Exception):
    pass


class GraphDovesnap:

    DOVESNAP_NAME = 'dovesnap_plugin'
    OVS_NAME = 'dovesnap_ovs'
    DRIVER_NAME = 'ovs'
    OFP_LOCAL = 4294967294
    DOCKER_URL = 'unix://var/run/docker.sock'
    OUTPUT_FILE = 'dovesnapviz'

    def __init__(self):
        return

    def _get_named_container(self, client, name):
        container = client.containers(filters={'name': name})
        if container:
            return container[0]
        return None

    def _get_named_container_hi(self, client_hi, name):
        container = client_hi.containers.list(filters={'name': name})
        if container:
            return container[0]
        return None

    def _get_dovesnap_networks(self, client):
        return client.networks(filters={'driver': self.DRIVER_NAME})

    def _dovesnap_bridgename(self, net_id):
        return 'ovsbr-%s' % net_id[:5]

    def _scrape_container_cmd(self, name, cmd):
        client_hi = docker.DockerClient(base_url=self.DOCKER_URL)
        container = client_hi.containers.list(filters={'name': name})
        (dump_exit, output) = container[0].exec_run(cmd)
        if dump_exit != 0:
            raise GraphDovesnapException('%s: %s', cmd, output)
        return [line.decode('UTF-8') for line in output.splitlines()]

    def _scrape_container_iface(self, name):
        lines = self._scrape_container_cmd(name, ['ip', '-o', 'link', 'show'])
        if_re = re.compile(r'^(\d+):\s+([^\@]+)\@if(\d+):.+link\/ether\s+(\S+).+$')
        results = []
        for line in lines:
            match = if_re.match(line)
            if match:
                iflink = int(match[1])
                ifname = match[2]
                peeriflink = int(match[3])
                mac = match[4]
                results.append((ifname, mac, iflink, peeriflink))
        return results

    def _scrape_container_ip(self, name, iflink):
        lines = self._scrape_container_cmd(name, ['ip', '-o', 'addr'])
        if_re = re.compile(r'^%u:.+inet\s+(\S+).+$' % iflink)
        for line in lines:
            match = if_re.match(line)
            if match:
                return match[1]
        return None

    def _scrape_bridge_interfaces(self, network):
        network_id = network['Id']
        bridge_name = self._dovesnap_bridgename(network_id)
        lines = self._scrape_container_cmd(
            self.OVS_NAME, ['ovs-ofctl', 'dump-ports-desc', bridge_name])
        port_desc = {}
        port_desc_re = re.compile(r'^\s*(\d+|LOCAL)\((\S+)\).+$')
        for line in lines:
            match = port_desc_re.match(line)
            if match:
                port = match[1]
                desc = match[2]
                if port == 'LOCAL':
                    port = self.OFP_LOCAL
                port = int(port)
                port_desc[desc] = port
        return port_desc

    def _scrape_host_veths(self):
        host_veths = {}
        process = subprocess.Popen(
            ['ip', '-o', 'link', 'show', 'type', 'veth'], stdout=subprocess.PIPE)
        if_re = re.compile(
            r'^(\d+):\s+([^\@]+)\@.+link\/ether\s+(\S+).+link-netnsid\s+(\d+).*$')
        for line in process.stdout.readlines():
            match = if_re.match(line.decode('UTF-8'))
            if match:
                iflink = int(match[1])
                ifname = match[2]
                mac = match[3]
                host_veths[iflink] = (ifname, mac)
        return host_veths

    def build_graph(self):
        dot = Digraph()
        client = docker.APIClient(base_url=self.DOCKER_URL)
        if not client.ping():
            raise GraphDovesnapException('cannot connect to docker')
        dovesnap = self._get_named_container(client, self.DOVESNAP_NAME)
        if not dovesnap:
            raise GraphDovesnapException('cannot find dovesnap container')
        networks = self._get_dovesnap_networks(client)
        host_veths = self._scrape_host_veths()
        all_port_desc = {}
        for network in networks:
            network_id = network['Id']
            network_name = network['Name']
            network = client.inspect_network(network_id)
            bridge_name = self._dovesnap_bridgename(network_id)
            all_port_desc[network_id] = self._scrape_bridge_interfaces(network)
            options = ['%s: %s' % (option.split('.')[-1], optionval)
                for option, optionval in network['Options'].items()]
            network_label = '\n'.join([network_name, bridge_name] + options)
            dot.node(network_id, network_label)
            container_ports = set()
            for container_id, container in network['Containers'].items():
                container_name = container['Name']
                container_inspect = client.inspect_container(container_id)
                for ifname, mac, iflink, peeriflink in self._scrape_container_iface(container_name):
                    if peeriflink in host_veths:
                        br_ifname, _ = host_veths[peeriflink]
                        labels = ['%s: %s' % (label.split('.')[-1], labelval)
                            for label, labelval in container_inspect['Config']['Labels'].items()]
                        host_label = [container_name, ifname, mac]
                        ip = self._scrape_container_ip(container_name, iflink)
                        if ip:
                            host_label.append(ip)
                        host_label.extend(labels)
                        ofport = all_port_desc[network_id][br_ifname]
                        container_ports.add(ofport)
                        edge_label = '%u' % ofport
                        dot.node(container_id, '\n'.join(host_label))
                        dot.edge(network_id, container_id, edge_label)
                        break
            for br_desc, ofport in all_port_desc[network_id].items():
                if ofport in container_ports:
                    continue
                if ofport == self.OFP_LOCAL:
                    dot.edge(network_id, 'NAT')
                else:
                    dot.edge(network_id, br_desc, str(ofport))
        dot.format = 'png'
        dot.render(self.OUTPUT_FILE)
        # leave only PNG
        os.remove(self.OUTPUT_FILE)


g = GraphDovesnap()
g.build_graph()
