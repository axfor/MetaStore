#!/bin/bash

# Script to replace clientv3.New() calls with NewEtcdClient() helper
# This suppresses lease keep-alive warnings during tests

echo "Replacing clientv3.New() calls with NewEtcdClient() helper..."

# Find all test files
for file in test/*_test.go; do
    # Skip if file doesn't exist
    [ -f "$file" ] || continue

    # Check if file contains clientv3.New
    if grep -q "clientv3.New(clientv3.Config{" "$file"; then
        echo "Processing: $file"

        # Use perl for multi-line replacement
        # This pattern matches: clientv3.New(clientv3.Config{ ... })
        perl -i -0pe 's/clientv3\.New\(clientv3\.Config\{\s*Endpoints:\s*(\[[^\]]+\]),\s*DialTimeout:\s*([^,\s]+),?\s*\}\)/NewEtcdClient($1, $2)/gs' "$file"
    fi
done

echo "Done! Modified files."
echo ""
echo "Verifying changes..."
grep -n "NewEtcdClient" test/*_test.go | wc -l
echo "instances of NewEtcdClient found."
