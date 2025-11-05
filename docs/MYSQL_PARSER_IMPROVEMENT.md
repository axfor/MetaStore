# MySQL SQL Parser 改进方案

## 当前问题

### 现有实现的局限性

当前 `api/mysql/query.go` 使用**字符串匹配**方式解析 SQL：

```go
// 当前实现
func (h *MySQLHandler) parseWhereClause(query string) *whereClause {
    queryUpper := strings.ToUpper(query)
    whereIdx := strings.Index(queryUpper, "WHERE")
    // ... 字符串查找和切割
}
```

**存在的问题**：

1. ❌ **无法处理复杂语法**：
   - `WHERE key = 'value' AND value LIKE 'prefix%'` - 不支持
   - `WHERE key IN ('k1', 'k2', 'k3')` - 不支持
   - `WHERE (key = 'k1' OR key = 'k2')` - 不支持

2. ❌ **容易出错**：
   ```sql
   -- 这些会被错误解析：
   SELECT * FROM kv WHERE value = 'WHERE key = "test"'  -- value 中包含 WHERE
   SELECT * FROM kv WHERE key = 'user\'s key'           -- 转义字符
   ```

3. ❌ **扩展性差**：
   - 每增加一个操作符需要大量代码
   - 无法支持复杂表达式
   - 难以维护

4. ❌ **不符合 SQL 标准**：
   - 无法处理标准 SQL 语法
   - 与 MySQL 兼容性差

## 改进方案：使用 TiDB Parser

### 1. 方案选择

推荐使用 **TiDB Parser**（`github.com/pingcap/tidb/pkg/parser`）：

**优势**：
- ✅ **完整的 MySQL 语法支持**：与 MySQL 5.7/8.0 完全兼容
- ✅ **抽象语法树（AST）**：结构化的 SQL 表示
- ✅ **高性能**：经过 TiDB 生产环境验证
- ✅ **活跃维护**：PingCAP 官方维护
- ✅ **丰富的 API**：支持 visitor 模式遍历 AST

**其他选项对比**：

| Parser | 优点 | 缺点 |
|--------|------|------|
| TiDB Parser | MySQL 完全兼容，高性能 | 依赖较重（~10MB） |
| vitess sqlparser | 轻量级 | MySQL 兼容性一般 |
| xwb1989/sqlparser | 简单易用 | 不支持复杂语法 |

### 2. 架构设计

```
┌─────────────────────────────────────────────────┐
│           MySQL Protocol Layer                   │
│  (go-mysql-org/go-mysql)                        │
└────────────────┬────────────────────────────────┘
                 │ SQL Query String
                 ▼
┌─────────────────────────────────────────────────┐
│           SQL Parser Layer (NEW)                 │
│  github.com/pingcap/tidb/pkg/parser             │
├─────────────────────────────────────────────────┤
│ • Parse SQL → AST                               │
│ • Validate syntax                                │
│ • Extract query components                       │
└────────────────┬────────────────────────────────┘
                 │ QueryPlan struct
                 ▼
┌─────────────────────────────────────────────────┐
│        Query Execution Layer (Current)           │
│  api/mysql/query.go                          │
├─────────────────────────────────────────────────┤
│ • Execute query plan                            │
│ • Access KV store                               │
│ • Build result set                              │
└────────────────┬────────────────────────────────┘
                 │ mysql.Result
                 ▼
┌─────────────────────────────────────────────────┐
│           KV Store Layer                         │
│  internal/kvstore/store.go                      │
└─────────────────────────────────────────────────┘
```

### 3. 实现代码

#### 3.1 添加依赖

```bash
go get github.com/pingcap/tidb/pkg/parser
go get github.com/pingcap/tidb/pkg/parser/ast
```

#### 3.2 创建 SQL Parser 包

**文件**: `api/mysql/parser/parser.go`

```go
package parser

import (
    "fmt"
    "strings"

    "github.com/pingcap/tidb/pkg/parser"
    "github.com/pingcap/tidb/pkg/parser/ast"
    _ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

// QueryPlan represents a parsed SQL query execution plan
type QueryPlan struct {
    Type        QueryType
    TableName   string
    Columns     []string        // SELECT columns
    WhereExpr   *WhereCondition // WHERE clause
    Limit       int64
    Offset      int64
}

type QueryType int

const (
    QueryTypeSelect QueryType = iota
    QueryTypeInsert
    QueryTypeUpdate
    QueryTypeDelete
)

// WhereCondition represents WHERE clause conditions
type WhereCondition struct {
    Type     ConditionType
    Key      string        // For simple key = 'value'
    Value    string
    Operator string        // =, >, <, >=, <=, LIKE, IN
    IsLike   bool
    Prefix   string        // For LIKE 'prefix%'
    Children []*WhereCondition // For AND/OR
}

type ConditionType int

const (
    ConditionTypeSimple ConditionType = iota // key = 'value'
    ConditionTypeAnd                         // expr AND expr
    ConditionTypeOr                          // expr OR expr
    ConditionTypeIn                          // key IN (...)
)

// SQLParser wraps TiDB parser
type SQLParser struct {
    parser *parser.Parser
}

// NewSQLParser creates a new SQL parser
func NewSQLParser() *SQLParser {
    return &SQLParser{
        parser: parser.New(),
    }
}

// Parse parses a SQL query and returns a query plan
func (p *SQLParser) Parse(sql string) (*QueryPlan, error) {
    stmts, _, err := p.parser.Parse(sql, "", "")
    if err != nil {
        return nil, fmt.Errorf("failed to parse SQL: %w", err)
    }

    if len(stmts) == 0 {
        return nil, fmt.Errorf("no statement found")
    }

    stmt := stmts[0]

    switch stmt := stmt.(type) {
    case *ast.SelectStmt:
        return p.parseSelectStmt(stmt)
    case *ast.InsertStmt:
        return p.parseInsertStmt(stmt)
    case *ast.UpdateStmt:
        return p.parseUpdateStmt(stmt)
    case *ast.DeleteStmt:
        return p.parseDeleteStmt(stmt)
    default:
        return nil, fmt.Errorf("unsupported statement type: %T", stmt)
    }
}

// parseSelectStmt parses a SELECT statement
func (p *SQLParser) parseSelectStmt(stmt *ast.SelectStmt) (*QueryPlan, error) {
    plan := &QueryPlan{
        Type: QueryTypeSelect,
    }

    // Parse table name
    if stmt.From != nil {
        if tableSource, ok := stmt.From.TableRefs.Left.(*ast.TableSource); ok {
            if tableName, ok := tableSource.Source.(*ast.TableName); ok {
                plan.TableName = tableName.Name.L
            }
        }
    }

    // Parse SELECT columns
    if stmt.Fields != nil {
        for _, field := range stmt.Fields.Fields {
            if field.WildCard != nil {
                // SELECT *
                plan.Columns = []string{"key", "value"}
                break
            }
            if colName, ok := field.Expr.(*ast.ColumnNameExpr); ok {
                plan.Columns = append(plan.Columns, colName.Name.Name.L)
            }
        }
    }

    // Parse WHERE clause
    if stmt.Where != nil {
        whereExpr, err := p.parseWhereExpr(stmt.Where)
        if err != nil {
            return nil, err
        }
        plan.WhereExpr = whereExpr
    }

    // Parse LIMIT
    if stmt.Limit != nil {
        if count, ok := stmt.Limit.Count.(*ast.ValueExpr); ok {
            plan.Limit = count.GetInt64()
        }
        if offset, ok := stmt.Limit.Offset.(*ast.ValueExpr); ok {
            plan.Offset = offset.GetInt64()
        }
    }

    return plan, nil
}

// parseWhereExpr parses WHERE expression recursively
func (p *SQLParser) parseWhereExpr(expr ast.ExprNode) (*WhereCondition, error) {
    switch expr := expr.(type) {
    case *ast.BinaryOperationExpr:
        return p.parseBinaryOp(expr)
    case *ast.PatternInExpr:
        return p.parseInExpr(expr)
    case *ast.PatternLikeExpr:
        return p.parseLikeExpr(expr)
    default:
        return nil, fmt.Errorf("unsupported WHERE expression: %T", expr)
    }
}

// parseBinaryOp parses binary operations (=, AND, OR, etc.)
func (p *SQLParser) parseBinaryOp(expr *ast.BinaryOperationExpr) (*WhereCondition, error) {
    switch expr.Op {
    case ast.LogicAnd:
        left, err := p.parseWhereExpr(expr.L)
        if err != nil {
            return nil, err
        }
        right, err := p.parseWhereExpr(expr.R)
        if err != nil {
            return nil, err
        }
        return &WhereCondition{
            Type:     ConditionTypeAnd,
            Children: []*WhereCondition{left, right},
        }, nil

    case ast.LogicOr:
        left, err := p.parseWhereExpr(expr.L)
        if err != nil {
            return nil, err
        }
        right, err := p.parseWhereExpr(expr.R)
        if err != nil {
            return nil, err
        }
        return &WhereCondition{
            Type:     ConditionTypeOr,
            Children: []*WhereCondition{left, right},
        }, nil

    case ast.EQ, ast.NE, ast.LT, ast.LE, ast.GT, ast.GE:
        // Simple comparison: key = 'value'
        colName, ok := expr.L.(*ast.ColumnNameExpr)
        if !ok {
            return nil, fmt.Errorf("left side must be column name")
        }

        value, ok := expr.R.(*ast.ValueExpr)
        if !ok {
            return nil, fmt.Errorf("right side must be value")
        }

        return &WhereCondition{
            Type:     ConditionTypeSimple,
            Key:      colName.Name.Name.L,
            Value:    value.GetString(),
            Operator: expr.Op.String(),
        }, nil

    default:
        return nil, fmt.Errorf("unsupported binary operator: %s", expr.Op)
    }
}

// parseLikeExpr parses LIKE expression
func (p *SQLParser) parseLikeExpr(expr *ast.PatternLikeExpr) (*WhereCondition, error) {
    colName, ok := expr.Expr.(*ast.ColumnNameExpr)
    if !ok {
        return nil, fmt.Errorf("LIKE left side must be column name")
    }

    pattern, ok := expr.Pattern.(*ast.ValueExpr)
    if !ok {
        return nil, fmt.Errorf("LIKE pattern must be value")
    }

    patternStr := pattern.GetString()
    prefix := strings.TrimRight(patternStr, "%")

    return &WhereCondition{
        Type:     ConditionTypeSimple,
        Key:      colName.Name.Name.L,
        IsLike:   true,
        Prefix:   prefix,
        Operator: "LIKE",
    }, nil
}

// parseInExpr parses IN expression
func (p *SQLParser) parseInExpr(expr *ast.PatternInExpr) (*WhereCondition, error) {
    colName, ok := expr.Expr.(*ast.ColumnNameExpr)
    if !ok {
        return nil, fmt.Errorf("IN left side must be column name")
    }

    // TODO: Parse list of values
    return &WhereCondition{
        Type:     ConditionTypeIn,
        Key:      colName.Name.Name.L,
        Operator: "IN",
    }, nil
}

// parseInsertStmt parses INSERT statement
func (p *SQLParser) parseInsertStmt(stmt *ast.InsertStmt) (*QueryPlan, error) {
    // TODO: Implement INSERT parsing
    return &QueryPlan{Type: QueryTypeInsert}, nil
}

// parseUpdateStmt parses UPDATE statement
func (p *SQLParser) parseUpdateStmt(stmt *ast.UpdateStmt) (*QueryPlan, error) {
    // TODO: Implement UPDATE parsing
    return &QueryPlan{Type: QueryTypeUpdate}, nil
}

// parseDeleteStmt parses DELETE statement
func (p *SQLParser) parseDeleteStmt(stmt *ast.DeleteStmt) (*QueryPlan, error) {
    // TODO: Implement DELETE parsing
    return &QueryPlan{Type: QueryTypeDelete}, nil
}
```

#### 3.3 集成到 MySQLHandler

**修改**: `api/mysql/query.go`

```go
import (
    // ... existing imports
    "metaStore/api/mysql/parser"
)

type MySQLHandler struct {
    // ... existing fields
    sqlParser *parser.SQLParser  // Add SQL parser
}

func NewMySQLHandler(store kvstore.Store, authProvider *AuthProvider) *MySQLHandler {
    return &MySQLHandler{
        store:        store,
        authProvider: authProvider,
        sessions:     make(map[uint32]*Session),
        sqlParser:    parser.NewSQLParser(),  // Initialize parser
    }
}

// handleSelect - 新实现使用 TiDB Parser
func (h *MySQLHandler) handleSelect(ctx context.Context, query string) (*mysql.Result, error) {
    // Parse SQL using TiDB parser
    plan, err := h.sqlParser.Parse(query)
    if err != nil {
        // Fallback to legacy parser for special queries
        queryUpper := strings.ToUpper(query)
        if strings.Contains(queryUpper, "@@") || strings.Contains(queryUpper, "VERSION()") {
            return h.handleSystemSelect(ctx, query)
        }
        if !strings.Contains(queryUpper, " FROM ") {
            return h.handleConstantSelect(ctx, query)
        }
        return nil, mysql.NewError(mysql.ER_PARSE_ERROR, fmt.Sprintf("SQL parse error: %v", err))
    }

    // Execute query plan
    return h.executePlan(ctx, plan)
}

// executePlan executes a parsed query plan
func (h *MySQLHandler) executePlan(ctx context.Context, plan *parser.QueryPlan) (*mysql.Result, error) {
    switch plan.Type {
    case parser.QueryTypeSelect:
        return h.executeSelect(ctx, plan)
    case parser.QueryTypeInsert:
        return h.executeInsert(ctx, plan)
    case parser.QueryTypeUpdate:
        return h.executeUpdate(ctx, plan)
    case parser.QueryTypeDelete:
        return h.executeDelete(ctx, plan)
    default:
        return nil, mysql.NewError(mysql.ER_NOT_SUPPORTED_YET, "query type not supported")
    }
}

// executeSelect executes a SELECT query plan
func (h *MySQLHandler) executeSelect(ctx context.Context, plan *parser.QueryPlan) (*mysql.Result, error) {
    // Determine read revision
    tx := h.getTransaction()
    var readRevision int64 = 0
    if tx != nil && tx.active {
        readRevision = tx.startRev
    }

    var resp *kvstore.RangeResponse
    var err error

    // Execute based on WHERE condition
    if plan.WhereExpr == nil {
        // No WHERE - scan all
        limit := plan.Limit
        if limit == 0 {
            limit = 100
        }
        resp, err = h.store.Range(ctx, "", "\x00", limit, readRevision)
    } else {
        resp, err = h.executeWhereCondition(ctx, plan.WhereExpr, readRevision)
    }

    if err != nil {
        return nil, err
    }

    // Build result set with selected columns
    return h.buildResultSet(resp.Kvs, plan.Columns)
}

// executeWhereCondition executes WHERE condition recursively
func (h *MySQLHandler) executeWhereCondition(ctx context.Context, cond *parser.WhereCondition, rev int64) (*kvstore.RangeResponse, error) {
    switch cond.Type {
    case parser.ConditionTypeSimple:
        if cond.IsLike {
            // LIKE query
            endKey := h.getPrefixEndKey(cond.Prefix)
            return h.store.Range(ctx, cond.Prefix, endKey, 1000, rev)
        } else if cond.Operator == "=" {
            // Exact match
            return h.store.Range(ctx, cond.Value, "", 1, rev)
        }
        // TODO: Handle other operators (>, <, etc.)

    case parser.ConditionTypeAnd:
        // Execute AND: intersect results
        // TODO: Optimize by executing most selective condition first
        return h.executeAndCondition(ctx, cond.Children, rev)

    case parser.ConditionTypeOr:
        // Execute OR: union results
        return h.executeOrCondition(ctx, cond.Children, rev)

    case parser.ConditionTypeIn:
        // Execute IN: union of exact matches
        // TODO: Implement IN execution
    }

    return nil, fmt.Errorf("unsupported condition type: %v", cond.Type)
}
```

### 4. 测试用例

```go
package parser

import (
    "testing"
    "github.com/stretchr/testify/assert"
)

func TestSQLParser(t *testing.T) {
    parser := NewSQLParser()

    tests := []struct {
        name     string
        sql      string
        wantCols []string
        wantErr  bool
    }{
        {
            name:     "Simple SELECT *",
            sql:      "SELECT * FROM kv",
            wantCols: []string{"key", "value"},
        },
        {
            name:     "SELECT specific columns",
            sql:      "SELECT key, value FROM kv",
            wantCols: []string{"key", "value"},
        },
        {
            name:     "WHERE with =",
            sql:      "SELECT * FROM kv WHERE key = 'test'",
            wantCols: []string{"key", "value"},
        },
        {
            name:     "WHERE with LIKE",
            sql:      "SELECT * FROM kv WHERE key LIKE 'prefix%'",
            wantCols: []string{"key", "value"},
        },
        {
            name:     "Complex WHERE",
            sql:      "SELECT * FROM kv WHERE key = 'k1' AND value LIKE 'v%'",
            wantCols: []string{"key", "value"},
        },
        {
            name:     "WHERE with OR",
            sql:      "SELECT * FROM kv WHERE key = 'k1' OR key = 'k2'",
            wantCols: []string{"key", "value"},
        },
        {
            name:     "WHERE with IN",
            sql:      "SELECT * FROM kv WHERE key IN ('k1', 'k2', 'k3')",
            wantCols: []string{"key", "value"},
        },
        {
            name:     "LIMIT and OFFSET",
            sql:      "SELECT * FROM kv LIMIT 10 OFFSET 5",
            wantCols: []string{"key", "value"},
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            plan, err := parser.Parse(tt.sql)
            if tt.wantErr {
                assert.Error(t, err)
                return
            }
            assert.NoError(t, err)
            assert.Equal(t, tt.wantCols, plan.Columns)
        })
    }
}
```

### 5. 迁移计划

#### Phase 1: 基础实现（1-2 周）
- [ ] 添加 TiDB Parser 依赖
- [ ] 实现 `parser` 包基础功能
- [ ] 支持简单 SELECT 查询（=, LIKE）
- [ ] 编写单元测试

#### Phase 2: 功能增强（1 周）
- [ ] 支持复杂 WHERE（AND, OR, IN）
- [ ] 支持 LIMIT, OFFSET
- [ ] 支持 ORDER BY
- [ ] 集成测试

#### Phase 3: 完整功能（1-2 周）
- [ ] 支持 INSERT, UPDATE, DELETE
- [ ] 支持事务相关语法
- [ ] 性能优化
- [ ] 兼容性测试

#### Phase 4: 平滑切换（1 周）
- [ ] 保留旧解析器作为 fallback
- [ ] 灰度测试
- [ ] 全面切换
- [ ] 删除旧代码

### 6. 优势总结

**使用 TiDB Parser 后**：

| 特性 | 当前实现 | TiDB Parser |
|------|---------|-------------|
| 复杂 WHERE | ❌ 不支持 | ✅ 完全支持 |
| SQL 标准兼容 | ❌ 部分支持 | ✅ MySQL 兼容 |
| 扩展性 | ❌ 差 | ✅ 优秀 |
| 维护成本 | ❌ 高 | ✅ 低 |
| 错误处理 | ❌ 简单 | ✅ 完善 |
| 性能 | ✅ 快 | ✅ 快 |
| 代码量 | ~200 行 | ~100 行 |

**支持的新语法**：
```sql
-- 复杂条件
WHERE key = 'k1' AND (value LIKE 'prefix%' OR value = 'exact')

-- IN 查询
WHERE key IN ('k1', 'k2', 'k3')

-- 范围查询
WHERE key > 'k1' AND key < 'k99'

-- 排序和分页
ORDER BY key ASC LIMIT 10 OFFSET 20

-- 子查询（未来）
WHERE key IN (SELECT key FROM other_table)
```

### 7. 参考资料

- TiDB Parser 文档: https://github.com/pingcap/tidb/tree/master/pkg/parser
- TiDB Parser 示例: https://github.com/pingcap/parser/tree/master/_example
- AST 结构文档: https://pkg.go.dev/github.com/pingcap/tidb/pkg/parser/ast
