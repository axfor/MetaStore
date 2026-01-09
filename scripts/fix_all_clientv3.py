#!/usr/bin/env python3
import re
from pathlib import Path

def replace_clientv3_new(content):
    """替换 clientv3.New() 调用，支持制表符和空格"""
    # 匹配跨多行的 clientv3.New(clientv3.Config{...})
    # 使用 \s 来匹配任何空白字符（包括制表符和空格）
    pattern = r'clientv3\.New\(clientv3\.Config\{' \
              r'\s+Endpoints:\s+(\[[^\]]+\]),' \
              r'\s+DialTimeout:\s+([^,\n]+),' \
              r'\s+\}\)'

    replacement = r'NewEtcdClient(\1, \2)'

    return re.sub(pattern, replacement, content, flags=re.MULTILINE)

def main():
    test_dir = Path('test')
    files = list(test_dir.glob('*_test.go'))

    modified_count = 0

    for filepath in files:
        with open(filepath, 'r', encoding='utf-8') as f:
            original = f.read()

        modified = replace_clientv3_new(original)

        if modified != original:
            with open(filepath, 'w', encoding='utf-8') as f:
                f.write(modified)
            modified_count += 1
            print(f'✓ 修改: {filepath.name}')

    print(f'\n总共修改: {modified_count} 个文件')

    # 验证
    total_remaining = 0
    for filepath in files:
        with open(filepath, 'r') as f:
            count = f.read().count('clientv3.New(clientv3.Config{')
            if count > 0:
                total_remaining += count
                print(f'  {filepath.name}: 剩余 {count} 处')

    print(f'\n剩余未替换: {total_remaining} 处')

if __name__ == '__main__':
    main()
