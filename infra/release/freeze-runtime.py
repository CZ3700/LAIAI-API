#!/usr/bin/env python3
"""Freeze the effective deployed Compose configuration without logging secrets."""
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys


def fail(message):
    raise ValueError(message)


def write_private(path, value):
    path = Path(path)
    tmp = path.with_suffix(path.suffix + '.partial')
    with tmp.open('x', encoding='utf-8') as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write('\n')
    tmp.chmod(0o600)
    tmp.replace(path)


def signature(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def trusted_path(value, directory=False):
    path = Path(value)
    if not path.is_absolute() or path.is_symlink() or any(c in value for c in '\n\r\0'):
        fail('runtime Compose source path is not a regular absolute trusted path')
    info = path.stat()
    if info.st_uid != 0 or info.st_mode & 0o022:
        fail('runtime Compose source must be root-owned and not writable by others')
    if not (stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode)):
        fail('runtime Compose source type mismatch')
    return path


def metadata(items, project):
    if len(items) != 1:
        fail('exactly one current core container is required')
    labels = items[0]['Config']['Labels']
    if labels.get('com.docker.compose.project') != project or labels.get('com.docker.compose.service') != 'core':
        fail('current core identity does not match the Compose project')
    working = labels['com.docker.compose.project.working_dir']
    trusted_path(working, directory=True)
    files = labels['com.docker.compose.project.config_files'].split(',')
    if not files or len(set(files)) != len(files) or any(not name for name in files):
        fail('runtime Compose file chain is missing or ambiguous')
    hashes = {str(trusted_path(name)): hashlib.sha256(Path(name).read_bytes()).hexdigest() for name in files}
    return {'project': project, 'working_dir': working, 'files': files, 'file_hashes': hashes}


def literal(value):
    """Compose interpolates config inputs again: preserve runtime $ literally."""
    if isinstance(value, str):
        return value.replace('$', '$$')
    if isinstance(value, list):
        return [literal(item) for item in value]
    if value is None:
        return None
    fail('runtime command/entrypoint must be a string array or null')


def freeze(config, source, items):
    roles = ('core', 'caddy', 'mysql', 'redis')
    if set(config.get('services', {})) != set(roles) or config.get('name') != source['project']:
        fail('runtime Compose services or project differ from the verified deployment')
    actual = {}
    for item in items:
        labels = item['Config']['Labels']
        role = labels.get('com.docker.compose.service')
        if labels.get('com.docker.compose.project') != source['project'] or role not in roles or role in actual:
            fail('runtime container identity is ambiguous')
        if not re.fullmatch(r'sha256:[a-f0-9]{64}', item['Image']):
            fail('runtime image is not a full image ID')
        actual[role] = item
    if set(actual) != set(roles):
        fail('one runtime container is required for each service')

    # Config output is already Compose-escaped. Only values taken directly from
    # docker inspect need $ -> $$; escaping the full rendered config would double
    # existing escapes in health checks and other shell expressions.
    config.pop('volumes', None)
    config.pop('networks', None)
    config['volumes'], config['networks'] = {}, {}
    volume_keys, network_keys, summaries = {}, {}, {}
    for role in roles:
        item, service = actual[role], config['services'][role]
        service.pop('build', None)
        service.pop('env_file', None)
        if service.get('network_mode'):
            fail('non-Compose network modes require a separately reviewed deployment')
        service['image'], service['pull_policy'] = item['Image'], 'never'
        environment = {}
        for entry in item['Config'].get('Env') or []:
            key, separator, value = entry.partition('=')
            if not separator or not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*', key) or key in environment:
                fail('runtime environment is invalid or ambiguous')
            environment[key] = value
        service['environment'] = {key: literal(value) for key, value in environment.items()}
        service['command'] = literal(item['Config'].get('Cmd'))
        service['entrypoint'] = literal(item['Config'].get('Entrypoint'))
        mounts = []
        for mount in item.get('Mounts', []):
            kind = mount['Type']
            if kind == 'tmpfs':
                continue  # Retained below from the effective HostConfig tmpfs.
            row = {'type': kind, 'target': literal(mount['Destination']), 'read_only': not mount['RW']}
            if kind == 'volume':
                name = mount['Name']
                key = volume_keys.setdefault(name, 'runtime_volume_' + str(len(volume_keys)))
                config['volumes'][key] = {'name': literal(name), 'external': True}
                row['source'] = key
            elif kind == 'bind':
                row['source'] = literal(mount['Source'])
                propagation = mount.get('Propagation')
                if propagation:
                    row['bind'] = {'propagation': propagation, 'create_host_path': False}
                else:
                    row['bind'] = {'create_host_path': False}
                selinux = set(mount.get('Mode', '').split(',')) & {'z', 'Z'}
                if len(selinux) == 1:
                    row['bind']['selinux'] = selinux.pop()
            else:
                fail('unsupported runtime mount type')
            mounts.append(row)
        service['volumes'] = mounts
        tmpfs = item.get('HostConfig', {}).get('Tmpfs') or {}
        if tmpfs:
            service['tmpfs'] = [literal(path + (':' + options if options else '')) for path, options in sorted(tmpfs.items())]
        else:
            service.pop('tmpfs', None)
        service_networks = {}
        for name, attachment in item.get('NetworkSettings', {}).get('Networks', {}).items():
            if name in ('host', 'none', 'bridge'):
                fail('only existing named Compose networks may be reused')
            key = network_keys.setdefault(name, 'runtime_network_' + str(len(network_keys)))
            config['networks'][key] = {'name': literal(name), 'external': True}
            settings = {'aliases': literal(attachment.get('Aliases') or [])}
            # Preserve explicitly requested addresses only, not Docker's current
            # dynamically allocated address, which is free to change on recreate.
            requested = attachment.get('IPAMConfig') or {}
            for source_key, destination in (('IPv4Address', 'ipv4_address'), ('IPv6Address', 'ipv6_address')):
                if requested.get(source_key):
                    settings[destination] = requested[source_key]
            service_networks[key] = settings
        if not service_networks:
            fail('runtime service is not attached to a named network')
        service['networks'] = service_networks
        summaries[role] = {'image': item['Image'], 'environment_sha256': signature(environment),
                           'command_sha256': signature(item['Config'].get('Cmd')),
                           'entrypoint_sha256': signature(item['Config'].get('Entrypoint')),
                           'mounts_sha256': signature(item.get('Mounts', [])),
                           'network_names_sha256': signature(sorted(item['NetworkSettings']['Networks']))}
    return config, {'version': 1, 'source': source, 'services': summaries}


def main():
    command = sys.argv[1]
    if command == 'metadata':
        json.dump(metadata(json.load(sys.stdin), sys.argv[2]), sys.stdout)
    elif command == 'paths':
        data = json.loads(Path(sys.argv[2]).read_text())
        for value in [data['working_dir'], *data['files']]:
            sys.stdout.buffer.write(value.encode() + b'\0')
    elif command == 'freeze':
        rendered = json.loads(Path(sys.argv[2]).read_text())
        source = json.loads(Path(sys.argv[3]).read_text())
        frozen, manifest = freeze(rendered, source, json.load(sys.stdin))
        write_private(sys.argv[4], frozen)
        manifest['compose_sha256'] = hashlib.sha256(Path(sys.argv[4]).read_bytes()).hexdigest()
        write_private(sys.argv[5], manifest)
        digest_file = Path(sys.argv[4]).with_name('rollback-compose.sha256')
        temporary = digest_file.with_suffix('.sha256.partial')
        with temporary.open('x', encoding='utf-8') as stream:
            stream.write(manifest['compose_sha256'] + '  ' + Path(sys.argv[4]).name + '\n')
        temporary.chmod(0o600)
        temporary.replace(digest_file)
    else:
        fail('unknown runtime snapshot operation')


if __name__ == '__main__':
    try:
        main()
    except (OSError, KeyError, ValueError, TypeError, IndexError):
        print('runtime configuration snapshot failed; no raw configuration or secret is printed', file=sys.stderr)
        sys.exit(1)
