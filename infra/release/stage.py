#!/usr/bin/env python3
"""Build, isolate, exercise and publish the exact release images. Linux/root only.

No production configuration is imported. Registry HTTP is allowed only over a
loopback endpoint (remote operators must provide an authenticated SSH tunnel).
All subprocess errors/logs are kept in the private release directory; API bodies,
passwords and keys are never printed. Tests do not configure an upstream channel.
"""
import argparse
import base64
import copy
try:
    import fcntl
except ImportError:  # Allow portable, no-Docker unit tests on Windows.
    fcntl = None
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

import artifact


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError('unexpected HTTP redirect refused')


def require(value, message):
    if not value:
        raise ValueError(message)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


def write(path, value, exclusive=False):
    flags = os.O_WRONLY | os.O_CREAT | (os.O_EXCL if exclusive else os.O_TRUNC)
    with os.fdopen(os.open(path, flags, 0o600), 'w') as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write('\n')


def call(args, *, data=None, log=None, env=None, timeout=600):
    if args[0] == 'docker':
        args = ['docker', '--host', 'unix:///var/run/docker.sock', *args[1:]]
    result = subprocess.run(args, input=data, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, env=env, timeout=timeout)
    if log:
        with open(log, 'ab') as stream:
            stream.write(result.stdout)
    require(result.returncode == 0, 'Command failed: ' + args[0] + '; see private stage logs')
    return result.stdout


def registry_url(value):
    parsed = urllib.parse.urlsplit(value)
    require(parsed.scheme in ('http', 'https') and parsed.hostname and not parsed.username
            and not parsed.password and parsed.path in ('', '/') and not parsed.query
            and not parsed.fragment, 'registry must be a bare HTTP(S) origin')
    require(parsed.scheme != 'http' or parsed.hostname in ('127.0.0.1', 'localhost', '::1'),
            'registry HTTP is allowed only on loopback; use TLS or an SSH tunnel')
    return parsed.scheme + '://' + parsed.netloc


class Stage:
    def __init__(self, config):
        self.cfg = config
        self.commit = config['commit']
        self.tag = artifact.tag(config['branch'], self.commit)
        self.source = Path(config['source']).resolve()
        self.root = Path(config['root']).resolve()
        require(self.source.is_absolute() and str(self.source).startswith('/opt/laiai-build/sources/'),
                'source must be a dedicated /opt/laiai-build/sources checkout')
        require(str(self.root).startswith('/var/lib/laiai-artifacts/') and self.root.name == self.tag,
                'root must be /var/lib/laiai-artifacts/<release tag>')
        self.private = self.root / 'stage'
        self.private.mkdir(parents=True, exist_ok=True, mode=0o700)
        os.chmod(self.private, 0o700)
        self.project = 'laiai-stage-' + self.commit[:12]
        self.port = config.get('port', 18080)
        require(type(self.port) is int and 1024 <= self.port <= 65535, 'invalid loopback port')
        self.registry = registry_url(config['registry'])
        self.authority = urllib.parse.urlsplit(self.registry).netloc
        self.http = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        self.log = self.private / 'commands.log'
        self.compose_path = self.private / 'compose.json'
        self.deploy_path = self.private / 'deployment.json'
        self.result_path = self.private / 'acceptance.local.json'
        self.fingerprint = sha(canonical(config))

    def source_check(self):
        head = call(['git', '-C', str(self.source), 'rev-parse', 'HEAD']).decode().strip()
        dirty = call(['git', '-C', str(self.source), 'status', '--porcelain']).decode().strip()
        require(head == self.commit and not dirty, 'source must be clean at the full pinned commit')
        for file in ('infra/Caddyfile', 'infra/mysql/my.cnf', 'infra/redis/redis.conf'):
            require((self.source / file).is_file(), 'missing source deployment configuration')

    def inspect(self, *args):
        # Container inspection includes environment secrets; parse only in memory.
        return json.loads(call(['docker', 'inspect', *args]))

    def image(self, role):
        reference = 'laiai/' + role + ':' + self.tag
        image = self.inspect(reference)[0]
        require(image.get('Os') == 'linux' and image.get('Architecture') == 'amd64', 'image platform mismatch')
        require(image.get('Config', {}).get('Labels', {}).get('org.opencontainers.image.revision') == self.commit,
                'image revision mismatch: ' + role)
        require(re.fullmatch('sha256:[a-f0-9]{64}', image.get('Id', '')), 'invalid image identity')
        return image['Id']

    def compose(self, *args, timeout=600, log_output=True):
        return call(['docker', 'compose', '-p', self.project, '-f', str(self.compose_path), *args],
                    log=self.log if log_output else None, timeout=timeout)

    def build(self):
        self.source_check()
        require(not self.deploy_path.exists(), 'use a new release for a deployed candidate; cannot rebuild tested tags')
        bases = self.cfg['base_images']
        for role, keys in (('core', ('GO_IMAGE', 'ALPINE_IMAGE')), ('caddy', ('NODE_IMAGE', 'CADDY_IMAGE'))):
            command = ['docker', 'build', '--pull=false', '--cpuset-cpus', '0-1', '--memory',
                       '6g' if role == 'core' else '4g', '--label',
                       'org.opencontainers.image.revision=' + self.commit,
                       '--label', 'org.opencontainers.image.source=' + artifact.REPOSITORY,
                       '-t', 'laiai/' + role + ':' + self.tag,
                       '-f', str(self.source / ('infra/' + role + '.Dockerfile'))]
            for key in keys:
                require(re.fullmatch(r'[A-Za-z0-9./:_-]+@sha256:[a-f0-9]{64}', bases[key]), 'pinned base digest required')
                command += ['--build-arg', key + '=' + bases[key]]
            command += [str(self.source)]
            env = dict(os.environ, DOCKER_BUILDKIT='0')
            call(command, log=self.private / ('build-' + role + '.log'), env=env, timeout=7200)
            self.image(role)
        write(self.private / 'build.json', {'commit': self.commit, 'images': {r: self.image(r) for r in artifact.ROLES}})
        print('Both pinned images built.')

    def make_compose(self, images):
        for key in ('mysql_image', 'redis_image'):
            require(re.fullmatch(r'[A-Za-z0-9./:_-]+@sha256:[a-f0-9]{64}', self.cfg[key]), 'pinned data image digest required')
        passwords = {k: secrets.token_hex(32) for k in ('mysql_root', 'mysql', 'redis', 'auth', 'video', 'admin')}
        data_key = base64.b64encode(secrets.token_bytes(32)).decode()
        services = {
            'mysql': {'image': self.cfg['mysql_image'], 'environment': {
                'MYSQL_ROOT_PASSWORD': passwords['mysql_root'], 'MYSQL_DATABASE': 'laiai',
                'MYSQL_USER': 'laiai', 'MYSQL_PASSWORD': passwords['mysql']},
                'volumes': ['mysql-data:/var/lib/mysql', str(self.source / 'infra/mysql/my.cnf') + ':/etc/mysql/conf.d/laiai.cnf:ro'],
                'healthcheck': {'test': ['CMD-SHELL', 'MYSQL_PWD=$$MYSQL_ROOT_PASSWORD mysqladmin ping -h localhost -uroot --silent'],
                                'interval': '5s', 'timeout': '5s', 'retries': 40}, 'mem_limit': '2g'},
            'redis': {'image': self.cfg['redis_image'],
                'entrypoint': ['sh', '-ec', 'umask 077; cat /etc/redis/redis.conf > /tmp/stage-redis.conf; printf "\\nrequirepass %s\\n" "$$REDISCLI_AUTH" >> /tmp/stage-redis.conf; chown redis:redis /tmp/stage-redis.conf; exec /usr/local/bin/docker-entrypoint.sh redis-server /tmp/stage-redis.conf'],
                'command': [], 'tmpfs': ['/tmp:size=10M'],
                'environment': {'REDISCLI_AUTH': passwords['redis']},
                'volumes': ['redis-data:/data', str(self.source / 'infra/redis/redis.conf') + ':/etc/redis/redis.conf:ro'],
                'healthcheck': {'test': ['CMD', 'redis-cli', 'ping'], 'interval': '5s', 'timeout': '5s', 'retries': 20},
                'mem_limit': '512m'},
            'core': {'image': images['core'], 'environment': {
                'LAIAI_DSN': 'laiai:' + passwords['mysql'] + '@tcp(mysql:3306)/laiai?parseTime=true&charset=utf8mb4&loc=UTC',
                'LAIAI_AUTH_SECRET': passwords['auth'], 'LAIAI_VIDEO_HMAC_SECRET': passwords['video'],
                'LAIAI_ADMIN_TOKEN': passwords['admin'],
                'LAIAI_DATA_KEYS': 'stage:' + data_key, 'LAIAI_DATA_KEY_ACTIVE': 'stage',
                'LAIAI_REDIS_ADDR': 'redis:6379', 'LAIAI_REDIS_PASSWORD': passwords['redis'],
                'LAIAI_LISTEN_ADDR': ':8080'}, 'read_only': True, 'tmpfs': ['/tmp:size=10M'],
                'cap_drop': ['ALL'], 'security_opt': ['no-new-privileges:true'], 'mem_limit': '2g',
                'depends_on': {'mysql': {'condition': 'service_healthy'}, 'redis': {'condition': 'service_healthy'}},
                'healthcheck': {'test': ['CMD', 'wget', '-q', '-O', '/dev/null', 'http://127.0.0.1:8080/healthz'],
                                'interval': '5s', 'timeout': '5s', 'retries': 40, 'start_period': '90s'}},
            'caddy': {'image': images['caddy'], 'ports': ['127.0.0.1:' + str(self.port) + ':80'],
                'environment': {'LAIAI_DOMAIN': ':80'},
                'volumes': [str(self.source / 'infra/Caddyfile') + ':/etc/caddy/Caddyfile:ro',
                            'caddy-data:/data', 'caddy-config:/config'],
                'depends_on': {'core': {'condition': 'service_healthy'}}, 'mem_limit': '512m'}}
        for service in services.values():
            service['networks'] = ['isolated']
            service['restart'] = 'unless-stopped'
        # Docker 20 does not publish ports for an internal-only container.
        # Only the public entrypoint joins ingress; app/data services retain no
        # outbound route, and the published socket remains host-loopback-only.
        services['caddy']['networks'] = ['isolated', 'ingress']
        return {'services': services, 'volumes': {key: {} for key in ('mysql-data', 'redis-data', 'caddy-data', 'caddy-config')},
                'networks': {'isolated': {'internal': True}, 'ingress': {}}}

    def deploy(self):
        self.source_check()
        images = {role: self.image(role) for role in artifact.ROLES}
        if self.deploy_path.exists():
            deployed = json.loads(self.deploy_path.read_text())
            require(deployed['config_sha256'] == self.fingerprint and deployed['images'] == images,
                    'existing staging deployment does not match config/images')
            require(sha(self.compose_path.read_bytes()) == deployed['compose_sha256'], 'staging compose changed')
        else:
            require(not call(['docker', 'ps', '-aq', '--filter', 'label=com.docker.compose.project=' + self.project]).strip(),
                    'unowned existing Compose project; refusing to adopt')
            for kind in ('volume', 'network'):
                require(not call(['docker', kind, 'ls', '-q', '--filter', 'label=com.docker.compose.project=' + self.project]).strip(),
                        'unowned existing staging data/network; refusing reuse')
            with socket.socket() as sock:
                sock.bind(('127.0.0.1', self.port))
            write(self.compose_path, self.make_compose(images), exclusive=True)
            write(self.deploy_path, {'schema': 1, 'commit': self.commit, 'project': self.project,
                'images': images, 'config_sha256': self.fingerprint,
                'compose_sha256': sha(self.compose_path.read_bytes())}, exclusive=True)
        # Pulling the pinned MySQL image on a new host may take minutes; the
        # readiness deadline itself remains 300s and all checks stay mandatory.
        self.compose('up', '-d', '--no-build', '--wait', '--wait-timeout', '300', timeout=1800)
        self.running()
        self.wait_http_health()
        print('Isolated staging ready at 127.0.0.1:' + str(self.port))

    def running(self, legacy_ingress=False):
        deployed = json.loads(self.deploy_path.read_text())
        require(deployed['commit'] == self.commit and deployed['config_sha256'] == self.fingerprint,
                'staging deployment/config identity changed')
        require(sha(self.compose_path.read_bytes()) == deployed['compose_sha256'], 'staging compose changed')
        config = json.loads(self.compose_path.read_text())
        has_ingress = 'ingress' in config['networks']
        require(has_ingress or legacy_ingress, 'legacy internal-only ingress requires explicit repair-ingress')
        found = {}
        for role in ('core', 'caddy', 'mysql', 'redis'):
            ids = self.compose('ps', '-q', role).decode().split()
            require(len(ids) == 1, 'expected one staging container per service')
            item = self.inspect(ids[0])[0]
            labels = item['Config']['Labels']
            require(labels.get('com.docker.compose.project') == self.project and labels.get('com.docker.compose.service') == role,
                    'staging container ownership mismatch')
            require(item['State']['Running'] and item['State'].get('Health', {}).get('Status', 'healthy') == 'healthy',
                    'staging service unhealthy: ' + role)
            networks = item['NetworkSettings']['Networks']
            expected_networks = {self.project + '_isolated'}
            if role == 'caddy' and has_ingress:
                expected_networks.add(self.project + '_ingress')
                ingress = json.loads(call(['docker', 'network', 'inspect', self.project + '_ingress'], log=self.log))[0]
                require(ingress.get('Internal') is False and ingress.get('Driver') == 'bridge'
                        and ingress.get('Labels', {}).get('com.docker.compose.project') == self.project,
                        'staging ingress bridge is not owned')
            require(set(networks) == expected_networks, 'staging container network membership mismatch')
            network = json.loads(call(['docker', 'network', 'inspect', self.project + '_isolated'], log=self.log))[0]
            require(network.get('Internal') is True and network.get('Labels', {}).get('com.docker.compose.project') == self.project,
                    'staging network is not isolated/owned')
            if role in ('mysql', 'redis'):
                target = '/var/lib/mysql' if role == 'mysql' else '/data'
                mount = next((m for m in item['Mounts'] if m['Destination'] == target), None)
                require(mount and mount['Type'] == 'volume' and mount.get('Name') == self.project + '_' + role + '-data',
                        'staging database/storage volume is not isolated')
            if role == 'caddy':
                ports = item['HostConfig'].get('PortBindings', {})
                require(ports == {'80/tcp': [{'HostIp': '127.0.0.1', 'HostPort': str(self.port)}]}, 'staging port is not loopback-only')
                if has_ingress:
                    actual_ports = item['NetworkSettings'].get('Ports', {})
                    require(actual_ports.get('80/tcp') == [{'HostIp': '127.0.0.1', 'HostPort': str(self.port)}],
                            'staging HTTP socket was not actually published')
                    require(all(not bindings for port, bindings in actual_ports.items() if port != '80/tcp'),
                            'unexpected staging socket published')
            if role in artifact.ROLES:
                require(item['Image'] == deployed['images'][role] == self.image(role), 'running image differs from pinned candidate')
            found[role] = item['Id']
        return deployed, found

    def repair_ingress(self):
        """Repair only an owned, unaccepted legacy internal-only entrypoint."""
        self.source_check()
        if self.result_path.exists():
            failed = json.loads(self.result_path.read_text())
            require(failed.get('passed') is False, 'cannot reconfigure a passed or ambiguous acceptance')
        else:
            failed = None
        deployed, before = self.running(legacy_ingress=True)
        compose = json.loads(self.compose_path.read_text())
        if 'ingress' in compose['networks']:
            print('Owned staging ingress already configured; run full acceptance.')
            return
        require(compose['networks'] == {'isolated': {'internal': True}}
                and all(service.get('networks') == ['isolated'] for service in compose['services'].values()),
                'only the known legacy staging topology may be repaired')
        # Archive the actual failed evidence before replacing its current pointer.
        if failed is not None:
            archived = self.private / ('acceptance.failed-' + sha(self.result_path.read_bytes())[:16] + '.json')
            write(archived, failed, exclusive=True)
        write(self.result_path, {'passed': False, 'commit': self.commit,
                                'status': 'configuration_changed_full_retest_required'})
        old_hash = deployed['compose_sha256']
        compose['networks']['ingress'] = {}
        compose['services']['caddy']['networks'] = ['isolated', 'ingress']
        write(self.compose_path, compose)
        deployed['compose_sha256'] = sha(self.compose_path.read_bytes())
        write(self.deploy_path, deployed)
        write(self.private / 'ingress-repair.json', {'commit': self.commit, 'images': deployed['images'],
            'old_compose_sha256': old_hash, 'new_compose_sha256': deployed['compose_sha256'],
            'change': 'caddy_only_add_owned_ingress_bridge', 'acceptance_passed': False}, exclusive=True)
        self.compose('up', '-d', '--no-deps', '--no-build', '--wait', '--wait-timeout', '300', 'caddy', timeout=420)
        unused, after = self.running()
        require(all(before[role] == after[role] for role in ('mysql', 'redis', 'core')),
                'ingress repair unexpectedly replaced app/data services')
        self.wait_http_health()
        print('Owned staging ingress repaired and HTTP reachable; run full acceptance before publish.')

    def prepare_tests(self):
        """Explicitly bootstrap a pre-acceptance stage created by an older tool.

        Only the fresh administrative secret may be added; image identities,
        source, isolated database volumes, and all existing secrets stay fixed.
        This is configuration preparation, never an acceptance/report rewrite.
        """
        require(not self.result_path.exists(), 'test preparation cannot change a candidate after acceptance started')
        deployed, before = self.running(legacy_ingress=True)
        compose = json.loads(self.compose_path.read_text())
        env = compose['services']['core']['environment']
        if env.get('LAIAI_ADMIN_TOKEN'):
            print('Staging test administration already configured.')
            return
        previous_hash = deployed['compose_sha256']
        env['LAIAI_ADMIN_TOKEN'] = secrets.token_hex(32)
        write(self.compose_path, compose)
        deployed['compose_sha256'] = sha(self.compose_path.read_bytes())
        write(self.deploy_path, deployed)
        write(self.private / 'test-preparation.json', {'commit': self.commit,
            'old_compose_sha256': previous_hash, 'new_compose_sha256': deployed['compose_sha256'],
            'images': deployed['images'], 'change': 'add_fresh_staging_admin_token', 'passed': False}, exclusive=True)
        self.compose('up', '-d', '--no-deps', '--no-build', '--wait', '--wait-timeout', '300', 'core', timeout=420)
        unused, after = self.running(legacy_ingress=True)
        require(all(before[role] == after[role] for role in ('mysql', 'redis', 'caddy')),
                'test preparation unexpectedly replaced other services')
        print('Staging test administration prepared; run actual acceptance next.')

    def request(self, method, path, body=None, token=None, expected=200, admin=None):
        headers = {'Content-Type': 'application/json'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        if admin:
            headers['X-Admin-Token'] = admin
        req = urllib.request.Request('http://127.0.0.1:' + str(self.port) + path,
            data=canonical(body) if body is not None else None, headers=headers, method=method)
        try:
            response = self.http.open(req, timeout=30)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read(8 * 1024 * 1024)
            status = response.status
        require(status == expected, method + ' ' + path + ': expected ' + str(expected) + ', got ' + str(status))
        return raw

    def json_request(self, *args, **kwargs):
        return json.loads(self.request(*args, **kwargs))

    def wait_http_health(self):
        # Caddy has no container healthcheck, so Docker's "running" is not proof
        # of an accessible host socket. Probe the actual loopback HTTP route.
        for attempt in range(30):
            try:
                if self.json_request('GET', '/healthz').get('status') == 'ok':
                    return
            except (urllib.error.URLError, ValueError, TimeoutError):
                pass
            if attempt < 29:
                time.sleep(1)
        raise ValueError('staging loopback health did not become reachable')

    def test(self):
        self.source_check()
        deployed, containers = self.running()
        write(self.result_path, {'passed': False, 'commit': self.commit, 'status': 'running'})
        checks, evidence = {}, {}
        health = self.json_request('GET', '/healthz')
        require(health.get('status') == 'ok', 'health body mismatch')
        checks['health'] = True
        index = self.request('GET', '/')
        image_index = self.compose('exec', '-T', 'caddy', 'cat', '/srv/console/index.html', log_output=False)
        require(index == image_index, 'served index differs from image')
        assets = sorted(set(re.findall(rb'(?:src|href)="(/assets/[^"?]+\.(?:js|css))"', index)))
        require(assets and any(asset.endswith(b'.js') for asset in assets), 'frontend asset links missing')
        hashes = {'/index.html': sha(index)}
        for asset in assets:
            path = asset.decode()
            require(re.fullmatch(r'/assets/[A-Za-z0-9_.-]+\.(js|css)', path), 'unsafe asset path')
            body = self.request('GET', path)
            image_body = self.compose('exec', '-T', 'caddy', 'cat', '/srv/console' + path, log_output=False)
            require(body == image_body, 'served frontend asset differs from image')
            hashes[path] = sha(body)
        evidence['frontend_sha256'] = hashes
        checks['frontend'] = True
        for unused in range(2):
            output = self.compose('exec', '-T', 'core', '/app/migrate', '-dir', '/app/migrations')
            require(b'up to date' in output and b'applied:' not in output, 'migration replay applied unexpected changes')
        checks['migration_replay'] = True
        doctor = self.compose('exec', '-T', 'core', '/app/schema-doctor', '-dir', '/app/migrations').decode()
        require('OK: schema at head' in doctor and 'no drift' in doctor, 'schema doctor not clean')
        evidence['schema_doctor'] = doctor.strip()
        checks['schema_doctor'] = True
        # Query the isolated database only; never create an upstream or a channel.
        channel_count = self.compose('exec', '-T', 'mysql', 'sh', '-ec',
            'MYSQL_PWD="$MYSQL_PASSWORD" mysql -ulaiai -N -B laiai -e "SELECT COUNT(*) FROM channels"').decode().strip()
        require(channel_count == '0', 'staging database contains upstream channels; refusing acceptance')
        # Only the isolated test installation skips real email delivery. The
        # random local administrative credential never leaves process memory.
        admin = json.loads(self.compose_path.read_text())['services']['core']['environment']['LAIAI_ADMIN_TOKEN']
        self.json_request('PUT', '/api/admin/settings', {'email_verification_enabled': 'false'}, admin=admin)
        evidence['test_email_verification_disabled'] = True
        tokens, orgs = [], []
        nonce = secrets.token_hex(8)
        for suffix in ('a', 'b'):
            credentials = {'email': 'stage-' + nonce + '-' + suffix + '@example.com', 'password': secrets.token_urlsafe(32)}
            registered = self.json_request('POST', '/api/auth/register', credentials, expected=201)
            login = self.json_request('POST', '/api/auth/login', credentials)
            token = login.get('token')
            require(token and not login.get('requires_2fa') and registered['org_id'] == login['org_id'], 'login identity mismatch')
            me = self.json_request('GET', '/api/me', token=token)
            require(me.get('role') == 'user', 'expected an ordinary test user')
            tokens.append(token)
            orgs.append(login['org_id'])
        require(orgs[0] != orgs[1], 'new users share organization')
        checks['registration_login'] = True
        evidence['test_org_ids'] = orgs
        balance = self.json_request('GET', '/api/balance', token=tokens[0])
        for key in ('balance', 'held'):
            require(key in balance and str(balance[key]) in ('0', '0.00000000'), 'fresh user balance not zero or field missing: ' + key)
        checks['balance'] = True
        readiness = self.json_request('GET', '/api/keys/video-access', token=tokens[0])
        require(readiness == {'state': 'not_configured', 'can_enable': True}, 'new org readiness incorrect')
        body = {'name': 'stage-acceptance', 'scopes': 'video:read video:write', 'quota_limit': '0.00000001',
                'model_limits': ['seedance-2.0-mini'], 'expires_in_days': 1, 'rpm_limit': 3}
        rejected = self.json_request('POST', '/api/keys', body, tokens[0], expected=403)
        require(rejected.get('code') == 'video_execution_consent_required', 'missing explicit-consent guard')
        created = []
        try:
            for consent in (True, False):
                payload = dict(body)
                if consent:
                    payload['enable_video_async'] = True
                result = self.json_request('POST', '/api/keys', payload, tokens[0], expected=201)
                require(result.get('key') and result.get('id'), 'key creation incomplete')
                created.append(result)
            ready = self.json_request('GET', '/api/keys/video-access', token=tokens[0])
            require(ready == {'state': 'enabled', 'can_enable': False}, 'consent not persisted/reusable')
            listed_raw = self.request('GET', '/api/keys', token=tokens[0])
            listed = json.loads(listed_raw)['keys']
            for key in created:
                require(key['key'].encode() not in listed_raw, 'key list leaks secret')
                row = next((v for v in listed if v['id'] == key['id']), None)
                require(row and row['scopes'] == body['scopes'] and row['model_limits'] == 'seedance-2.0-mini'
                        and row['rpm_limit'] == 3 and row['quota_limit'] == '0.00000001', 'key constraints changed')
                self.json_request('GET', '/v1/models', token=key['key'])
            checks['video_key_consent'] = True
            other = self.json_request('GET', '/api/keys', token=tokens[1])
            require(other.get('keys') == [], 'cross-org key visibility')
            for key in created:
                self.request('DELETE', '/api/keys/' + str(key['id']), token=tokens[1], expected=404)
            still = self.json_request('GET', '/api/keys', token=tokens[0])['keys']
            require(all(next(v for v in still if v['id'] == key['id'])['status'] == 1 for key in created), 'cross-org revoke changed key')
            other_ready = self.json_request('GET', '/api/keys/video-access', token=tokens[1])
            require(other_ready == {'state': 'not_configured', 'can_enable': True}, 'consent leaked across orgs')
            checks['tenant_isolation'] = True
        finally:
            for key in created:
                self.request('DELETE', '/api/keys/' + str(key['id']), token=tokens[0])
        final_keys = self.json_request('GET', '/api/keys', token=tokens[0])['keys']
        require(all(next(v for v in final_keys if v['id'] == key['id'])['status'] == 0 for key in created), 'revoke not persisted')
        for key in created:
            self.request('GET', '/v1/models', token=key['key'], expected=401)
        checks['key_revoke'] = True
        ending, current_containers = self.running()
        require(ending == deployed and current_containers == containers, 'services changed during acceptance')
        report = {'schema': 1, 'passed': True, 'commit': self.commit, 'branch': self.cfg['branch'], 'tag': self.tag,
            'local_images': deployed['images'], 'containers': containers, 'config_sha256': self.fingerprint,
            'compose_sha256': deployed['compose_sha256'], 'paid_calls': 0, 'isolated_database': True,
            'checks': checks, 'evidence': evidence, 'tested_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}
        write(self.result_path, report)
        print('Staging acceptance passed; paid calls: 0.')

    def registry_auth(self):
        config = Path(os.environ.get('DOCKER_CONFIG', str(Path.home() / '.docker'))) / 'config.json'
        value = json.loads(config.read_text())
        auths = value.get('auths', {})
        entry = auths.get(self.authority) or auths.get(self.registry) or {}
        require(entry.get('auth'), 'Docker registry login required; credentials are never accepted on argv')
        # Parse once to reject malformed credentials, without logging the value.
        decoded = base64.b64decode(entry['auth'], validate=True)
        require(b':' in decoded, 'invalid Docker registry credential')
        return 'Basic ' + entry['auth']

    def manifest(self, role, reference, auth, missing=False):
        request = urllib.request.Request(self.registry + '/v2/laiai/' + role + '/manifests/' + reference,
            headers={'Authorization': auth, 'Accept': 'application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json'})
        try:
            response = self.http.open(request, timeout=30)
        except urllib.error.HTTPError as error:
            if missing and error.code == 404:
                error.close()
                return None
            raise ValueError('Registry manifest request failed: HTTP ' + str(error.code)) from None
        with response:
            raw = response.read(8 * 1024 * 1024)
            digest = response.headers.get('Docker-Content-Digest')
        require(digest == 'sha256:' + sha(raw), 'registry digest does not match manifest bytes')
        data = json.loads(raw)
        require(data.get('schemaVersion') == 2 and data.get('config', {}).get('digest'), 'single-platform manifest required')
        return digest, data

    def publish(self):
        self.source_check()
        deployed, containers = self.running()
        report = json.loads(self.result_path.read_text())
        required = ('health', 'frontend', 'migration_replay', 'schema_doctor', 'registration_login', 'balance',
                    'video_key_consent', 'key_revoke', 'tenant_isolation')
        require(report.get('passed') is True and report.get('commit') == self.commit and report.get('tag') == self.tag
                and report.get('config_sha256') == self.fingerprint and report.get('compose_sha256') == deployed['compose_sha256']
                and report.get('local_images') == deployed['images'] and report.get('containers') == containers
                and type(report.get('paid_calls')) is int and report['paid_calls'] == 0 and report.get('isolated_database') is True
                and all(report.get('checks', {}).get(key) is True for key in required), 'exact running images need complete acceptance')
        auth = self.registry_auth()
        existing = {}
        # Check both tags before uploading anything. Never overwrite another image.
        for role in artifact.ROLES:
            existing[role] = self.manifest(role, self.tag, auth, missing=True)
            if existing[role]:
                require(existing[role][1]['config']['digest'] == deployed['images'][role], 'registry tag already belongs to a different image: ' + role)
        images = {}
        for role in artifact.ROLES:
            destination = self.authority + '/laiai/' + role
            if existing[role] is None:
                call(['docker', 'tag', deployed['images'][role], destination + ':' + self.tag], log=self.log)
                call(['docker', 'push', destination + ':' + self.tag], log=self.private / ('push-' + role + '.log'), timeout=1800)
            digest, manifest = self.manifest(role, self.tag, auth)
            require(manifest['config']['digest'] == deployed['images'][role], 'pushed image identity differs from tested image')
            pinned = destination + '@' + digest
            call(['docker', 'pull', pinned], log=self.log, timeout=600)
            actual = artifact.inspect_image(self.inspect(pinned), self.commit, pinned)
            require(actual == deployed['images'][role], 'pulled registry digest differs from accepted image')
            digest_again, by_digest = self.manifest(role, digest, auth)
            require(digest_again == digest and by_digest == manifest, 'registry immutable digest verification failed')
            images[role] = {'repository': 'laiai/' + role, 'digest': digest}
        ending, current_containers = self.running()
        require(ending == deployed and current_containers == containers, 'staging identity changed during publication')
        acceptance = copy.deepcopy(report)
        acceptance['images'] = images
        value = {'schema': 1, 'repository': artifact.REPOSITORY, 'branch': self.cfg['branch'], 'commit': self.commit,
                 'tag': self.tag, 'platform': 'linux/amd64', 'images': images, 'acceptance': acceptance,
                 'acceptance_sha256': sha(canonical(acceptance))}
        artifact.validate(value)
        target = self.root / 'artifact.json'
        if target.exists():
            require(json.loads(target.read_text()) == value, 'existing artifact differs; refusing overwrite')
        else:
            write(target, value, exclusive=True)
        print('Published verified artifact: ' + str(target))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True)
    parser.add_argument('command', choices=('build', 'deploy', 'prepare-tests', 'repair-ingress', 'test', 'publish'))
    args = parser.parse_args()
    require(fcntl is not None and hasattr(os, 'geteuid') and os.geteuid() == 0,
            'run on the isolated Linux build server as root')
    os.umask(0o077)
    path = Path(args.config)
    require(path.stat().st_uid == 0 and path.stat().st_mode & 0o077 == 0, 'stage config must be root-owned mode 0600')
    stage = Stage(json.loads(path.read_text()))
    with open('/var/lock/laiai-stage.lock', 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        getattr(stage, args.command.replace('-', '_'))()


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        # Subprocess and HTTP failures can carry secrets in their repr; never dump.
        message = str(error) if isinstance(error, ValueError) else type(error).__name__
        print('Staging failed: ' + message, file=sys.stderr)
        sys.exit(1)
