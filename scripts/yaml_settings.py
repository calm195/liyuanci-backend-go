"""严格YAML读写：私有值不输出，禁止变量展开、重复键和多文档。"""
from pathlib import Path
import os
import sys
import tempfile
from config_comments import annotate

ROOT = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(ROOT / 'scripts/vendor'), str(ROOT / '.local/tools/python')]
try:
    import yaml
except ImportError:
    raise RuntimeError('缺少PyYAML；请将scripts/requirements.txt安装到.local/tools/python') from None


class Loader(yaml.SafeLoader):
    """限制配置输入，保留原始标量类型，不执行自定义标签。"""

    def compose_node(self, parent, index):
        if self.check_event(yaml.AliasEvent):
            raise ValueError('配置不支持YAML别名')
        return super().compose_node(parent, index)

    def construct_mapping(self, node, deep=False):
        result = {}
        for key_node, value_node in node.value:
            key = self.construct_object(key_node, deep=deep)
            if not isinstance(key, str) or key in result:
                raise ValueError('配置键必须为不重复的字符串')
            result[key] = self.construct_object(value_node, deep=deep)
        return result


def loads(text):
    """读取一份块式YAML，拒绝旧JSON内容及超过1MiB的输入。"""
    if len(text.encode()) > 1 << 20 or text.lstrip().startswith(('{', '[')):
        raise ValueError('YAML格式无效')
    try:
        result = yaml.load(text, Loader=Loader)
    except (yaml.YAMLError, ValueError, RecursionError):
        raise ValueError('YAML配置无效，请核对格式、重复键和字段类型') from None
    if result is None:
        return {}
    if not isinstance(result, dict):
        raise ValueError('YAML根节点必须为映射')
    return result


def read(path):
    """只接收YAML扩展名，不回退读取旧配置。"""
    path = Path(path)
    if path.suffix.lower() not in ('.yaml', '.yml'):
        raise ValueError('配置文件必须使用.yaml或.yml')
    return loads(path.read_text())


class Dumper(yaml.SafeDumper):
    """PEM、ACL和原生文本用可读多行块表示。"""


def represent_string(dumper, value):
    return dumper.represent_scalar('tag:yaml.org,2002:str', value, style='|' if '\n' in value else None)


Dumper.add_representer(str, represent_string)


def dumps(value, comment='私有运行配置；保留历史密钥与凭据，不提交版本控制。'):
    """生成普通YAML；写入方的领域模板负责具体字段注释。"""
    return '# ' + comment + '\n' + annotate(yaml.dump(value, Dumper=Dumper, allow_unicode=True, sort_keys=False, default_flow_style=False, width=110))


def write(path, value, mode=0o600, comment='私有运行配置；保留历史密钥与凭据，不提交版本控制。'):
    """同目录原子替换，临时文件始终0600；不读取或生成凭据。"""
    path = Path(path)
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix='.' + path.name + '-', dir=path.parent)
    pending = Path(name)
    try:
        with os.fdopen(fd, 'w') as output:
            output.write(dumps(value, comment))
        pending.chmod(mode)
        pending.replace(path)
    finally:
        pending.unlink(missing_ok=True)


def process_environment():
    """仅继承工具所需宿主环境；旧应用和运维变量不进入子进程。"""
    return {key: value for key, value in os.environ.items()
            if not key.startswith(('APP_', 'LIYUANCI_', 'PG', 'COMPOSE_'))}


def layers(path):
    """公共配置与环境覆盖递归合并，大小写统一；不读取终端变量。"""
    def normalize(value):
        if isinstance(value, dict):
            out = {}
            for key, item in value.items():
                lowered = key.lower()
                if lowered in out or item is None:
                    raise ValueError('配置存在重复键或空值')
                out[lowered] = normalize(item)
            return out
        if isinstance(value, list):
            return [normalize(item) for item in value]
        return value

    def merge(left, right):
        for key, value in right.items():
            if isinstance(value, dict) and isinstance(left.get(key), dict):
                merge(left[key], value)
            else:
                left[key] = value
        return left

    path = Path(path).resolve()
    common = path.parent / 'common.yaml'
    result = normalize(read(common))
    if path != common:
        merge(result, normalize(read(path)))
    return result
