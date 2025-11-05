# MySQL API Quick Start Guide

## Overview

MetaStore now supports MySQL protocol access! You can use any standard MySQL client to connect and perform operations.

## Quick Start

### 1. Enable MySQL Protocol in Configuration

Create or edit `config.yaml`:

```yaml
server:
  cluster_id: 1
  member_id: 1
  listen_address: ":2379"

  mysql:
    enable: true          # Enable MySQL protocol
    address: ":3306"      # MySQL listen port
    username: "root"      # Username
    password: ""          # Password (empty for dev)
```

### 2. Start MetaStore

```bash
# With configuration file
./metastore -config config.yaml -storage memory

# Or with command line flags (MySQL enabled via config only)
./metastore -storage memory
```

### 3. Connect with MySQL Client

```bash
# Using MySQL CLI
mysql -h 127.0.0.1 -P 3306 -u root

# Or specify database
mysql -h 127.0.0.1 -P 3306 -u root metastore
```

### 4. Run SQL Commands

```sql
-- Show available databases
SHOW DATABASES;

-- Use the metastore database
USE metastore;

-- Show tables
SHOW TABLES;

-- Insert data
INSERT INTO kv (key, value) VALUES ('hello', 'world');

-- Query data
SELECT * FROM kv WHERE key = 'hello';

-- Update data
UPDATE kv SET value = 'mysql' WHERE key = 'hello';

-- Delete data
DELETE FROM kv WHERE key = 'hello';
```

## Examples

### Go Application

```go
package main

import (
    "database/sql"
    "fmt"
    "log"

    _ "github.com/go-sql-driver/mysql"
)

func main() {
    // Connect to MetaStore via MySQL protocol
    db, err := sql.Open("mysql", "root@tcp(127.0.0.1:3306)/metastore")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    // Insert
    _, err = db.Exec("INSERT INTO kv (key, value) VALUES ('mykey', 'myvalue')")
    if err != nil {
        log.Fatal(err)
    }

    // Query
    var key, value string
    err = db.QueryRow("SELECT * FROM kv WHERE key = 'mykey'").Scan(&key, &value)
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Key: %s, Value: %s\n", key, value)
}
```

### Python Application

```python
import mysql.connector

# Connect to MetaStore
conn = mysql.connector.connect(
    host='127.0.0.1',
    port=3306,
    user='root',
    password='',
    database='metastore'
)

cursor = conn.cursor()

# Insert
cursor.execute("INSERT INTO kv (key, value) VALUES (%s, %s)", ("mykey", "myvalue"))
conn.commit()

# Query
cursor.execute("SELECT * FROM kv WHERE key = %s", ("mykey",))
row = cursor.fetchone()
print(f"Key: {row[0]}, Value: {row[1]}")

# Cleanup
cursor.close()
conn.close()
```

### Java Application (JDBC)

```java
import java.sql.*;

public class MetaStoreExample {
    public static void main(String[] args) {
        String url = "jdbc:mysql://127.0.0.1:3306/metastore";
        String user = "root";
        String password = "";

        try (Connection conn = DriverManager.getConnection(url, user, password)) {
            // Insert
            String insertSQL = "INSERT INTO kv (key, value) VALUES (?, ?)";
            try (PreparedStatement pstmt = conn.prepareStatement(insertSQL)) {
                pstmt.setString(1, "mykey");
                pstmt.setString(2, "myvalue");
                pstmt.executeUpdate();
            }

            // Query
            String querySQL = "SELECT * FROM kv WHERE key = ?";
            try (PreparedStatement pstmt = conn.prepareStatement(querySQL)) {
                pstmt.setString(1, "mykey");
                try (ResultSet rs = pstmt.executeQuery()) {
                    if (rs.next()) {
                        System.out.println("Key: " + rs.getString("key"));
                        System.out.println("Value: " + rs.getString("value"));
                    }
                }
            }
        } catch (SQLException e) {
            e.printStackTrace();
        }
    }
}
```

## Cross-Protocol Data Access

Data written via any protocol can be read from any other protocol:

```bash
# Write via HTTP
curl -X PUT http://localhost:9121/mykey -d "value_from_http"

# Read via MySQL
mysql -h 127.0.0.1 -P 3306 -u root -e "SELECT * FROM metastore.kv WHERE key = 'mykey'"

# Write via etcd
etcdctl --endpoints=localhost:2379 put etcd_key etcd_value

# Read via MySQL
mysql -h 127.0.0.1 -P 3306 -u root -e "SELECT * FROM metastore.kv WHERE key = 'etcd_key'"
```

## Supported SQL Commands

| Command | Example | Notes |
|---------|---------|-------|
| `INSERT` | `INSERT INTO kv (key, value) VALUES ('k1', 'v1')` | Add new key-value pair |
| `SELECT` | `SELECT * FROM kv WHERE key = 'k1'` | Query by key |
| `UPDATE` | `UPDATE kv SET value = 'v2' WHERE key = 'k1'` | Update value |
| `DELETE` | `DELETE FROM kv WHERE key = 'k1'` | Remove key |
| `BEGIN` | `BEGIN;` | Start transaction |
| `COMMIT` | `COMMIT;` | Commit transaction |
| `ROLLBACK` | `ROLLBACK;` | Rollback transaction |
| `SHOW DATABASES` | `SHOW DATABASES;` | List databases |
| `SHOW TABLES` | `SHOW TABLES;` | List tables |
| `DESCRIBE` | `DESCRIBE kv;` | Show table schema |
| `USE` | `USE metastore;` | Select database |

## Configuration Options

| Option | Default | Description |
|--------|---------|-------------|
| `mysql.enable` | `false` | Enable MySQL protocol server |
| `mysql.address` | `:3306` | Listen address for MySQL |
| `mysql.username` | `root` | Authentication username |
| `mysql.password` | `` | Authentication password |

## Testing

### Manual Testing

```bash
# Start MetaStore with MySQL enabled
./metastore -config config.yaml -storage memory

# In another terminal, connect and test
mysql -h 127.0.0.1 -P 3306 -u root -e "INSERT INTO metastore.kv (key, value) VALUES ('test', 'works')"
mysql -h 127.0.0.1 -P 3306 -u root -e "SELECT * FROM metastore.kv WHERE key = 'test'"
```

### Automated Testing

```bash
# Run MySQL API integration tests
go test -v ./test -run TestMySQLMemory

# Run cross-protocol consistency tests
go test -v ./test -run TestCrossProtocol
```

## Troubleshooting

### Connection Refused

**Problem:** `ERROR 2003 (HY000): Can't connect to MySQL server`

**Solution:**
- Ensure MetaStore is running with MySQL enabled
- Check the listen address in configuration
- Verify firewall allows connections on port 3306

### Authentication Failed

**Problem:** `ERROR 1045 (28000): Access denied for user 'root'@'localhost'`

**Solution:**
- Check username and password in configuration
- Verify credentials match your connection string
- Default password is empty for development

### Command Not Supported

**Problem:** `ERROR 1047 (HY000): Unknown command`

**Solution:**
- Check the list of supported SQL commands
- Some advanced SQL features are not yet implemented
- Use simple SQL syntax for KV operations

## Performance Tips

1. **Connection Pooling**: Reuse database connections instead of creating new ones
2. **Batch Operations**: Use transactions for multiple operations
3. **Efficient Queries**: Use `WHERE key = 'specific_key'` for fast lookups
4. **Concurrent Access**: MySQL protocol supports thousands of concurrent connections

## Next Steps

- Read full documentation: [MYSQL_API_IMPLEMENTATION.md](MYSQL_API_IMPLEMENTATION.md)
- Check HTTP API documentation for comparison
- Explore etcd protocol compatibility
- Review cross-protocol consistency tests

## Support

For issues or questions:
- Check the documentation in `docs/`
- Review test files in `test/mysql_api_*.go`
- Open an issue on GitHub
