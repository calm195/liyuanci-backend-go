"""词库显式初始化保留既有条目、完整快照及并发发布保护。"""
import json
from pathlib import Path
import unittest

from init_wordlists import initialize


class WordlistInitializationTests(unittest.TestCase):
    def test_merge_and_repeat_preserve_existing_versions(self):
        seeds = json.loads((Path(__file__).resolve().parents[1] / 'configs/examples/moderation-wordlists.json').read_text())
        state = {kind: {'kind': kind, 'version': 3, 'effective_id': kind + '-old'} for kind in seeds}
        versions = {kind + '-old': {'entries': ['已有专属规则']} for kind in seeds}
        writes = []

        def call(method, path, data=None):
            if method == 'GET' and path.startswith('/wordlists?'):
                return {'heads': [dict(x) for x in state.values()]}
            if method == 'GET':
                return versions[path.split('/')[-1]]
            writes.append((path, data))
            if path.endswith('/versions'):
                head = state[data['kind']]
                self.assertEqual(data['expected_version'], head['version'])
                head['version'] += 1
                identifier = data['kind'] + '-new'
                versions[identifier] = dict(data)
                return {'id': identifier, 'version': 1}
            identifier = path.split('/')[-2]
            kind = versions[identifier]['kind']
            self.assertEqual(data['expected_head_version'], state[kind]['version'])
            state[kind]['version'] += 1
            state[kind]['effective_id'] = identifier
            return {'id': identifier}

        result = initialize(call, seeds)
        self.assertEqual(len(writes), 6)
        self.assertTrue(all(x['changed'] for x in result))
        for kind in seeds:
            self.assertIn('已有专属规则', versions[kind + '-new']['entries'])
            self.assertEqual(versions[kind + '-old']['entries'], ['已有专属规则'])
            self.assertTrue(set(seeds[kind]).issubset(versions[kind + '-new']['entries']))
        again = initialize(call, seeds)
        self.assertTrue(all(not x['changed'] for x in again))
        self.assertEqual(len(writes), 6)

    def test_conflicting_publication_is_not_force_retried(self):
        calls = []

        def call(method, path, data=None):
            calls.append(path)
            if method == 'GET':
                return {'heads': [{'kind': 'crisis', 'version': 1, 'effective_id': None}]}
            if path.endswith('/versions'):
                return {'id': 'draft', 'version': 1}
            raise ValueError('版本冲突')

        with self.assertRaises(ValueError):
            initialize(call, {'crisis': ['想自杀']})
        self.assertEqual(len(calls), 3)


if __name__ == '__main__':
    unittest.main()
