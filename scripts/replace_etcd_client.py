#!/usr/bin/env python3
"""
Replace clientv3.New(clientv3.Config{...}) with NewEtcdClient(...)
to suppress lease keep-alive warnings in tests.
"""

import re
import sys
from pathlib import Path

def replace_clientv3_new(content):
    """Replace clientv3.New patterns with NewEtcdClient."""

    # Pattern to match clientv3.New(clientv3.Config{...}) across multiple lines
    pattern = r'clientv3\.New\(clientv3\.Config\{\s*Endpoints:\s*(\[[^\]]+\]),\s*DialTimeout:\s*([^,\}]+),?\s*\}\)'

    def replacer(match):
        endpoints = match.group(1).strip()
        timeout = match.group(2).strip().rstrip(',')
        return f'NewEtcdClient({endpoints}, {timeout})'

    return re.sub(pattern, replacer, content, flags=re.DOTALL)

def process_file(filepath):
    """Process a single file."""
    try:
        with open(filepath, 'r', encoding='utf-8') as f:
            original = f.read()

        modified = replace_clientv3_new(original)

        if modified != original:
            with open(filepath, 'w', encoding='utf-8') as f:
                f.write(modified)
            return True
        return False
    except Exception as e:
        print(f"Error processing {filepath}: {e}", file=sys.stderr)
        return False

def main():
    """Main function."""
    test_dir = Path('test')
    test_files = list(test_dir.glob('*_test.go'))

    print(f"Processing {len(test_files)} test files...")

    modified_count = 0
    for filepath in test_files:
        if process_file(filepath):
            print(f"  Modified: {filepath}")
            modified_count += 1

    print(f"\nTotal files modified: {modified_count}")

    # Count NewEtcdClient usage
    total_new_usage = 0
    for filepath in test_files:
        with open(filepath, 'r') as f:
            total_new_usage += f.read().count('NewEtcdClient')

    print(f"Total NewEtcdClient calls: {total_new_usage}")

if __name__ == '__main__':
    main()
