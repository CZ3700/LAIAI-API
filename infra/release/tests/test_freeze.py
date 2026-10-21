import copy
import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location('freeze_runtime', Path(__file__).parents[1] / 'freeze-runtime.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class RuntimeFreezeTest(unittest.TestCase):
    def setUp(self):
        self.source = {'project': 'infra', 'working_dir': '/root/current/infra',
                       'files': ['/root/current/infra/docker-compose.yml'], 'file_hashes': {}}
        self.config = {'name': 'infra', 'services': {}, 'volumes': {'old': {}}, 'networks': {'old': {}}}
        self.items = []
        for role in ('core', 'caddy', 'mysql', 'redis'):
            self.config['services'][role] = {'image': 'mutable:tag', 'build': {'context': '/old'},
                                             'environment': {'STALE': 'wrong'},
                                             'healthcheck': {'test': ['CMD-SHELL', 'echo $$SECRET']}}
            self.items.append({'Image': 'sha256:' + 'a' * 64,
                               'Config': {'Labels': {'com.docker.compose.service': role, 'com.docker.compose.project': 'infra'},
                                          'Env': ['SECRET=$SECRET${OTHER}$$suffix', 'PATH=/bin'],
                                          'Cmd': ['sh', '-c', 'printf "%s" "$SECRET"'], 'Entrypoint': None},
                               'Mounts': [], 'HostConfig': {'Tmpfs': {}},
                               'NetworkSettings': {'Networks': {'infra_laiai': {'Aliases': [role], 'IPAMConfig': None}}}})
        self.items[1]['Mounts'] = [
            {'Type': 'volume', 'Name': 'infra_caddy-data', 'Source': '/var/lib/docker/volumes/infra_caddy-data/_data',
             'Destination': '/data', 'RW': True},
            {'Type': 'bind', 'Source': '/root/releases/prior/active.json', 'Destination': '/etc/caddy/Caddyfile',
             'RW': False, 'Mode': 'ro', 'Propagation': 'rprivate'}]
        self.items[0]['HostConfig']['Tmpfs'] = {'/tmp': 'rw,size=10m'}

    def test_retains_runtime_values_and_dollar_literals(self):
        frozen, manifest = module.freeze(copy.deepcopy(self.config), self.source, self.items)
        core = frozen['services']['core']
        self.assertNotIn('build', core)
        self.assertEqual(core['image'], 'sha256:' + 'a' * 64)
        self.assertEqual(core['pull_policy'], 'never')
        self.assertEqual(core['environment']['SECRET'], '$$SECRET$${OTHER}$$$$suffix')
        self.assertEqual(core['command'], ['sh', '-c', 'printf "%s" "$$SECRET"'])
        self.assertIsNone(core['entrypoint'])
        self.assertNotIn('STALE', core['environment'])
        self.assertEqual(core['tmpfs'], ['/tmp:rw,size=10m'])
        # Already-rendered Compose health checks must not be escaped twice.
        self.assertEqual(core['healthcheck']['test'][1], 'echo $$SECRET')
        self.assertEqual(frozen['volumes'], {'runtime_volume_0': {'name': 'infra_caddy-data', 'external': True}})
        self.assertEqual(frozen['networks'], {'runtime_network_0': {'name': 'infra_laiai', 'external': True}})
        self.assertEqual(frozen['services']['caddy']['volumes'][1]['source'], '/root/releases/prior/active.json')
        self.assertTrue(frozen['services']['caddy']['volumes'][1]['read_only'])
        self.assertNotIn('$SECRET', str(manifest))

    def test_actual_environment_change_changes_manifest(self):
        _, before = module.freeze(copy.deepcopy(self.config), self.source, self.items)
        self.items[0]['Config']['Env'][0] = 'SECRET=changed'
        _, after = module.freeze(copy.deepcopy(self.config), self.source, self.items)
        self.assertNotEqual(before['services']['core']['environment_sha256'], after['services']['core']['environment_sha256'])

    def test_duplicate_or_wrong_project_containers_refused(self):
        with self.assertRaises(ValueError):
            module.freeze(copy.deepcopy(self.config), self.source, self.items + [self.items[0]])
        self.items[0]['Config']['Labels']['com.docker.compose.project'] = 'another-project'
        with self.assertRaises(ValueError):
            module.freeze(copy.deepcopy(self.config), self.source, self.items)

    def test_unsupported_topology_refused(self):
        self.config['services']['core']['network_mode'] = 'host'
        with self.assertRaises(ValueError):
            module.freeze(copy.deepcopy(self.config), self.source, self.items)


if __name__ == '__main__':
    unittest.main()
