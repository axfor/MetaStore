#!/usr/bin/env python3
"""
批量替换测试文件中的 clientv3.New() 调用为 NewEtcdClient()
这样可以抑制 lease keep-alive 警告日志
"""

import re
import os
from pathlib import Path

def replace_in_file(filepath):
    """替换单个文件中的 clientv3.New() 调用"""
    with open(filepath, 'r', encoding='utf-8') as f:
        content = f.read()

    original = content

    # 模式：匹配 clientv3.New(clientv3.Config{...})
    # 提取 Endpoints 和 DialTimeout
    pattern = r'clientv3\.New\(clientv3\.Config\{\s*Endpoints:\s*(\[[^\]]+\]),\s*DialTimeout:\s*([^,\}]+),?\s*\}\)'

    def replacer(match):
        endpoints = match.group(1).strip()
        timeout = match.group(2).strip().rstrip(',')
        return f'NewEtcdClient({endpoints}, {timeout})'

    content = re.sub(pattern, replacer, content, flags=re.MULTILINE | re.DOTALL)

    if content != original:
        with open(filepath, 'w', encoding='utf-8') as f:
            f.write(content)
        return True
    return False

def main():
    test_dir = Path('test')
    test_files = list(test_dir.glob('*_test.go'))

    modified_count = 0
    modified_files = []

    for filepath in test_files:
        if replace_in_file(filepath):
            modified_count += 1
            modified_files.append(filepath.name)
            print(f'✓ 修改: {filepath.name}')

    print(f'\n总共修改了 {modified_count} 个文件')
    if modified_files:
        print('\n修改的文件列表:')
        for name in modified_files:
            print(f'  - {name}')

if __name__ == '__main__':
    main()
