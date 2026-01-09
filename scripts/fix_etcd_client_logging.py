#!/usr/bin/env python3
"""
Script to replace clientv3.New() calls with the new NewEtcdClient() helper function
that suppresses lease keep-alive warnings.
"""

import re
import glob
import os

def process_file(filepath):
    """Process a single test file to replace clientv3.New() calls."""
    with open(filepath, 'r') as f:
        content = f.read()

    original_content = content

    # Pattern 1: Simple clientv3.New with Config on same line
    # Example: clientv3.New(clientv3.Config{
    #            Endpoints:   []string{addr},
    #            DialTimeout: 5 * time.Second,
    #          })

    # Find all occurrences
    pattern = r'clientv3\.New\(clientv3\.Config\{([^}]+)\}\)'

    def replace_match(match):
        config_content = match.group(1)

        # Extract endpoints and dial timeout
        endpoints_match = re.search(r'Endpoints:\s*(\[[^\]]+\])', config_content)
        timeout_match = re.search(r'DialTimeout:\s*([^,\n]+)', config_content)

        if not endpoints_match or not timeout_match:
            # If we can't parse it, leave it as is
            return match.group(0)

        endpoints = endpoints_match.group(1).strip()
        timeout = timeout_match.group(1).strip().rstrip(',')

        return f'NewEtcdClient({endpoints}, {timeout})'

    content = re.sub(pattern, replace_match, content, flags=re.DOTALL)

    # Only write if changed
    if content != original_content:
        with open(filepath, 'w') as f:
            f.write(content)
        return True
    return False

def main():
    """Process all test files in the test directory."""
    test_files = glob.glob('test/*_test.go')

    modified_count = 0
    for filepath in test_files:
        if process_file(filepath):
            print(f'Modified: {filepath}')
            modified_count += 1

    print(f'\nTotal files modified: {modified_count}')

if __name__ == '__main__':
    main()
