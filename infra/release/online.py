#!/usr/bin/env python3
"""Generate and validate the application-only, no-maintenance release overlay.

Only reviewed core and static-web routes are changed. Unknown Caddy layouts
fail closed before the public configuration is reloaded.
"""
import copy
import json
from pathlib import Path
import re
import sys


def fail(message):
    raise SystemExit(message)


def read(path):
    return json.loads(Path(path).read_text())


def write(path, value):
    target = Path(path)
    target.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')
    target.chmod(0o600)


def names(release_id):
    if not re.fullmatch(r'\d{8}T\d{6}Z-[0-9a-f]{12}', release_id):
        fail('invalid release ID for online services')
    suffix = release_id.lower().replace('t', '-').replace('z-', '-')
    return 'online-core-' + suffix, 'online-web-' + suffix


def overlay():
    plan, images = read(sys.argv[2]), read(sys.argv[3])
    source, target = Path(sys.argv[4]), Path(sys.argv[5])
    core, web = names(target.parent.name)
    if plan['carryover'] != 3:
        fail('online overlay requires exact-schema application-only carryover')
    if not source.is_absolute() or not (source / 'infra/docker-compose.yml').is_file():
        fail('missing pinned source Compose file')
    if not all(re.fullmatch(r'sha256:[0-9a-f]{64}', images[x]) for x in ('core', 'caddy')):
        fail('online overlay requires pinned images')
    caddyfile = target.parent / 'config/online-web.Caddyfile'
    caddyfile.write_text('''
:8081 {
    root * /srv/console
    @assets path /assets/*
    header @assets Cache-Control "public, max-age=31536000, immutable"
    @index path / /index.html
    header @index Cache-Control "no-cache"
    try_files {path} /index.html
    file_server
}
'''.lstrip())
    caddyfile.chmod(0o600)
    lines = [
        'services:',
        f'  {core}:',
        '    extends:',
        '      file: ' + json.dumps(str(source / 'infra/docker-compose.yml')),
        '      service: core',
        '    build: !reset null',
        '    depends_on: !reset {}',
        '    image: ' + json.dumps(images['core']),
        '    pull_policy: never',
        '    restart: unless-stopped',
        f'  {web}:',
        '    image: ' + json.dumps(images['caddy']),
        '    pull_policy: never',
        '    restart: unless-stopped',
        '    read_only: true',
        # The upstream Caddy binary carries cap_net_bind_service. Dropping the
        # entire bounding set makes exec itself fail with EPERM, even on :8081.
        '    security_opt: ["no-new-privileges:true"]',
        '    command: ["caddy", "run", "--config", "/etc/caddy/Caddyfile"]',
        '    volumes:',
        '      - type: bind',
        '        source: ' + json.dumps(str(caddyfile)),
        '        target: /etc/caddy/Caddyfile',
        '        read_only: true',
        '    healthcheck:',
        '      test: ["CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8081/"]',
        '      interval: 5s',
        '      timeout: 3s',
        '      retries: 6',
        '    networks: [laiai]',
    ]
    target.write_text('\n'.join(lines) + '\n')
    target.chmod(0o600)
    print(core, web)


def check_compose():
    plan, images = read(sys.argv[2]), read(sys.argv[3])
    core_name, web_name = names(sys.argv[4])
    config = json.load(sys.stdin)
    if config.get('name') != sys.argv[5]:
        fail('online Compose project changed')
    services = config['services']
    stable, candidate, web = (services[n] for n in ('core', core_name, web_name))
    if candidate.get('image') != images['core'] or web.get('image') != images['caddy']:
        fail('online candidate image differs from pinned build')
    if candidate.get('build') or web.get('build') or candidate.get('ports') or web.get('ports'):
        fail('online candidates may not build or publish host ports')
    for field in ('environment', 'volumes', 'networks', 'read_only', 'tmpfs',
                  'healthcheck', 'security_opt', 'cap_drop', 'stop_grace_period'):
        if candidate.get(field) != stable.get(field):
            fail('online core differs from verified stable service: ' + field)
    if candidate.get('pull_policy') != 'never' or web.get('pull_policy') != 'never':
        fail('online candidate may not pull an image')
    if candidate.get('depends_on'):
        fail('online candidate must not start data services')
    if candidate.get('restart') != 'unless-stopped' or web.get('restart') != 'unless-stopped':
        fail('online candidates must restart after a host restart')
    if (web.get('read_only') is not True or web.get('cap_drop')
            or web.get('security_opt') != ['no-new-privileges:true']
            or web.get('command') != ['caddy', 'run', '--config', '/etc/caddy/Caddyfile']
            or web.get('healthcheck', {}).get('test') !=
            ['CMD', 'wget', '-q', '-O', '/dev/null', 'http://127.0.0.1:8081/']):
        fail('online frontend security or health configuration changed')
    web_mounts = [{k: v for k, v in mount.items() if k != 'bind' or v}
                  for mount in web.get('volumes', [])]
    if web_mounts != [{'type': 'bind', 'source': str(Path(sys.argv[6]) / 'config/online-web.Caddyfile'),
                       'target': '/etc/caddy/Caddyfile', 'read_only': True}]:
        fail('online web has an unexpected mount')
    if web.get('networks') != {'laiai': None}:
        fail('online web must join only the application network')
    if any(key in web for key in ('environment', 'depends_on')):
        fail('online web has unexpected dependencies or secrets')
    if plan['carryover'] != 3:
        fail('online deployment requires exact-schema application-only carryover')


def walk(value):
    if isinstance(value, dict):
        yield value
        for child in value.values():
            yield from walk(child)
    elif isinstance(value, list):
        for child in value:
            yield from walk(child)


def upstream_kind(handler):
    dynamic = handler.get('dynamic_upstreams', {})
    if dynamic.get('name') == 'core' and dynamic.get('port') == '8080':
        return 'core'
    upstreams = handler.get('upstreams', [])
    if len(upstreams) == 1:
        dial = upstreams[0].get('dial', '')
        if re.fullmatch(r'online-core-[a-z0-9-]+:8080', dial):
            return 'core'
        if re.fullmatch(r'online-web-[a-z0-9-]+:8081', dial):
            return 'web'
    return None


def drop_edge_try_files(candidate):
    # The edge Caddy keeps a baked console root that goes stale once the switch
    # routes the frontend to the online candidate. Its local try_files fallback
    # would rewrite every new asset path to /index.html before the proxy
    # (observed 2026-11-17: the whole console served only index.html). The
    # online frontend container owns asset fallback through its own try_files
    # and file_server. Two legacy shapes are handled: a pure fallback sibling
    # route is removed, and a route that fuses the fallback with the candidate
    # proxy keeps the proxy and sheds the matcher and rewrite. Fail closed on
    # unexpected counts.
    def has_tf(route):
        return any(isinstance(mm, dict) and 'try_files' in mm.get('file', {})
                   for mm in route.get('match') or [])

    def has_rw(route):
        return any(isinstance(h, dict) and h.get('handler') == 'rewrite'
                   for h in route.get('handle', []))

    def has_proxy(route):
        return any(isinstance(h, dict) and h.get('handler') == 'reverse_proxy'
                   for h in route.get('handle', []))

    seen = {'n': 0}

    def clean_in_place(node):
        if isinstance(node, dict):
            if has_tf(node) and has_rw(node) and has_proxy(node):
                node.pop('match', None)
                node['handle'] = [h for h in node['handle']
                                  if not (isinstance(h, dict) and h.get('handler') == 'rewrite')]
                seen['n'] += 1
            for value in node.values():
                clean_in_place(value)
        elif isinstance(node, list):
            for item in node:
                clean_in_place(item)

    def prune(node):
        if isinstance(node, list):
            for item in list(node):
                if isinstance(item, dict) and has_tf(item) and has_rw(item) and not has_proxy(item):
                    seen['n'] += 1
                    node.remove(item)
                else:
                    prune(item)
        elif isinstance(node, dict):
            for value in node.values():
                prune(value)

    clean_in_place(candidate['apps'])
    prune(candidate['apps'])
    if seen['n'] > 1:
        fail('unexpected number of edge try_files fallback routes')


def route():
    original = read(sys.argv[2])
    core, web = names(sys.argv[4])
    candidate = copy.deepcopy(original)
    core_proxies = [item for item in walk(candidate)
                    if item.get('handler') == 'reverse_proxy' and upstream_kind(item) == 'core']
    if len(core_proxies) != 3:
        fail('expected exactly three existing core routes')
    for proxy in core_proxies:
        proxy.pop('dynamic_upstreams', None)
        proxy['upstreams'] = [{'dial': core + ':8080'}]
    web_proxies = [item for item in walk(candidate)
                   if item.get('handler') == 'reverse_proxy' and upstream_kind(item) == 'web']
    file_servers = [item for item in walk(candidate) if item.get('handler') == 'file_server']
    if len(web_proxies) == 1 and not file_servers:
        web_proxies[0]['upstreams'] = [{'dial': web + ':8081'}]
    elif len(file_servers) == 1 and not web_proxies:
        routes = [item for item in walk(candidate) if isinstance(item.get('handle'), list)
                  and any(n.get('handler') == 'file_server' for n in walk(item['handle']))]
        if not routes:
            fail('cannot locate static frontend route')
        route_holder = routes[-1]
        if route_holder.get('match') or route_holder.get('terminal'):
            fail('unexpected static frontend route shape')
        route_holder['handle'] = [{'handler': 'reverse_proxy', 'upstreams': [{'dial': web + ':8081'}]}]
    else:
        fail('expected one known static frontend route')
    if sum(1 for item in walk(candidate) if item.get('handler') == 'file_server'):
        fail('static frontend was not fully switched')
    drop_edge_try_files(candidate)
    write(sys.argv[3], candidate)


def referenced():
    # Container service names that any supplied Caddy configuration still dials.
    # Retire keeps exactly these candidates and removes the rest; unknown
    # configurations fail closed upstream, never here.
    names = set()
    for path in sys.argv[2:]:
        config = json.load(sys.stdin) if path == '-' else read(path)
        for item in walk(config):
            if item.get('handler') != 'reverse_proxy':
                continue
            for up in item.get('upstreams', []):
                dial = up.get('dial', '')
                host = dial.rsplit(':', 1)[0] if ':' in dial else ''
                if re.fullmatch(r'online-(core|web)-[a-z0-9-]+', host):
                    names.add(host)
    for name in sorted(names):
        print(name)


def previous_web():
    config = read(sys.argv[2])
    proxies = [item for item in walk(config)
               if item.get('handler') == 'reverse_proxy' and upstream_kind(item) == 'web']
    if len(proxies) > 1:
        fail('multiple previous online web services')
    if proxies:
        print(proxies[0]['upstreams'][0]['dial'].split(':')[0])


def active_core():
    config = json.load(sys.stdin)
    proxies = [item for item in walk(config)
               if item.get('handler') == 'reverse_proxy' and upstream_kind(item) == 'core']
    if len(proxies) != 3:
        fail('cannot identify the three active core routes')
    targets = set()
    for proxy in proxies:
        if proxy.get('dynamic_upstreams', {}).get('name') == 'core':
            targets.add('core')
        else:
            targets.add(proxy['upstreams'][0]['dial'].split(':')[0])
    if len(targets) != 1:
        fail('active core routes disagree')
    print(targets.pop())


def same():
    if json.load(sys.stdin) != read(sys.argv[2]):
        fail('public Caddy configuration does not match the expected online route')


def main():
    {'overlay': overlay, 'check-compose': check_compose, 'referenced': referenced,
     'route': route, 'previous-web': previous_web,
     'active-core': active_core, 'same': same,
     'names': lambda: print(*names(sys.argv[2]))}[sys.argv[1]]()


if __name__ == '__main__':
    try:
        main()
    except (KeyError, IndexError, ValueError, OSError, TypeError):
        fail('invalid or incomplete online release state')
