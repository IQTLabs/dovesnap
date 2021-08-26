"""package setup."""

from setuptools import setup

setup(
    name='dovesnap',
    version=[ f.split('"')[1] for f in open('main.go', 'r').readlines() if 'version' in f ][0],
    install_requires=open('requirements.txt', 'r').read().splitlines(),
    license='Apache License 2.0',
    description='graphviz generator of dovesnap networks',
    url='https://github.com/IQTLabs/dovesnap',
    scripts=['bin/graph_dovesnap', 'bin/cleanup_dovesnap'],
)
