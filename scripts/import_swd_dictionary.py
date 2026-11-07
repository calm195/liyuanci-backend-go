#!/usr/bin/env python3
"""将 go.mod 锁定的 go-swd 原始内置词表转换成可嵌入目录。"""

import json
from pathlib import Path
import shutil
import subprocess


def main():
    root = Path(__file__).resolve().parent.parent
    package = "github.com/kirklin/go-swd"
    subprocess.run(["go", "mod", "download", package], cwd=root, check=True)
    module = json.loads(
        subprocess.check_output(["go", "list", "-m", "-json", package], cwd=root)
    )
    module_root = Path(module["Dir"])
    source = module_root / "dict"
    entries = set()
    for dictionary in sorted(source.glob("*.txt")):
        for line in dictionary.read_text(encoding="utf-8").split("\n"):
            word = line.strip()
            if word and not word.startswith("#"):
                entries.add((dictionary.stem, word))
    if not entries:
        raise ValueError("上游词库为空")
    destination = root / "internal/modules/moderation/builtin_catalog.tsv"
    lines = [f"{package}\t{module['Version']}"]
    lines.extend(f"{label}\t{word}" for label, word in sorted(entries))
    destination.write_text("\n".join(lines) + "\n", encoding="utf-8")
    shutil.copyfile(module_root / "LICENSE", destination.with_suffix(".LICENSE"))


if __name__ == "__main__":
    main()
