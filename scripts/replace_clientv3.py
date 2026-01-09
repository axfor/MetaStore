#!/usr/bin/env python3
"""逐文件替换 clientv3.New() 调用"""

import re
from pathlib import Path

def process_file(filepath):
    with open(filepath, 'r') as f:
        content = f.read()

    original = content

    # 替换模式：处理多行格式
    # clientv3.New(clientv3.Config{
    #     Endpoints:   [...],
    #     DialTimeout: ...,
    # })

    pattern = r'clientv3\.New\(clientv3\.Config\{\s*Endpoints:\s*(\[[^\]]+\]),\s*DialTimeout:\s*([^,\}]+),?\s*\}\)'

    def replace(match):
        endpoints = match.group(1)
        timeout = match.group(2).strip()
        return f'NewEtcdClient({endpoints}, {timeout})'

    content = re.sub(pattern, replace, content, flags=re.DOTALL)

    if content != original:
        with open(filepath, 'w') as f:
            f.write(content)
        return True
    return False

test_dir = Path('test')
modified = []

for test_file in sorted(test_dir.glob('*_test.go')):
    if process_file(test_file):
        modified.append(test_file.name)
        print(f'✓ {test_file.name}')

print(f'\n修改了 {len(modified)} 个文件')

# 验证
remaining = 0
for test_file in test_dir.glob('*_test.go'):
    with open(test_file) as f:
        count = f.read().count('clientv3.New(clientv3.Config{')
        remaining += count

print(f'剩余 clientv3.New() 调用: {remaining}')
